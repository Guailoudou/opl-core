package webapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Guailoudou/opl-core/internal/api"
	"github.com/Guailoudou/opl-core/internal/management"
	"github.com/gorilla/websocket"
)

type Server struct {
	services api.Services
	webDir   string
	origins  map[string]bool
	access   func(string) bool
	stop     chan struct{}
	once     sync.Once
}

func New(services api.Services, webDir string, origins []string) *Server {
	allowed := map[string]bool{"http://127.0.0.1:26780": true, "http://localhost:26780": true}
	for _, origin := range origins {
		if value := strings.TrimSpace(origin); value != "" {
			allowed[value] = true
		}
	}
	return &Server{services: services, webDir: webDir, origins: allowed, access: localHost, stop: make(chan struct{})}
}

func (s *Server) SetAccessChecker(check func(string) bool) {
	if check != nil {
		s.access = check
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","product":"opl-core","apiVersion":1}`))
	})
	mux.HandleFunc("/ws", s.websocket)
	mux.HandleFunc("/content/notice", proxy("https://file.gldhn.top/file/json/notice.json"))
	mux.HandleFunc("/content/update", proxy("https://file.gldhn.top/file/json/update.json"))
	mux.HandleFunc("/content/thank", proxy("https://file.gldhn.top/file/json/thank.json"))
	mux.HandleFunc("/content/preset", proxy("https://file.gldhn.top/file/json/preset.json"))
	mux.Handle("/", http.FileServer(http.Dir(s.webDir)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			client = r.RemoteAddr
		}
		if !s.access(client) {
			http.Error(w, "client IP is not allowed", http.StatusForbidden)
			return
		}
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if !localHost(host) {
			http.Error(w, "local access only", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'")
		mux.ServeHTTP(w, r)
	})
}

func proxy(source string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		client := &http.Client{Timeout: 10 * time.Second}
		response, err := client.Get(source)
		if err != nil {
			http.Error(w, "remote content unavailable", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			http.Error(w, "remote content unavailable", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.Copy(w, io.LimitReader(response.Body, 1<<20))
	}
}

func (s *Server) Done() <-chan struct{} { return s.stop }

func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if s.origins[origin] {
			return true
		}
		parsed, err := url.Parse(origin)
		if err != nil || !localHost(parsed.Hostname()) {
			return false
		}
		requestHost, _, splitErr := net.SplitHostPort(r.Host)
		if splitErr != nil {
			requestHost = r.Host
		}
		originIP := net.ParseIP(parsed.Hostname())
		return (originIP != nil && originIP.IsLoopback()) || parsed.Hostname() == "localhost" || parsed.Hostname() == requestHost
	}}
	connection, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer connection.Close()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var writeMu sync.Mutex
	write := func(value any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return connection.WriteJSON(value)
	}
	if err := write(api.Hello(runtime.GOOS + "-" + runtime.GOARCH)); err != nil {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var previous []byte
		for {
			select {
			case <-ticker.C:
				state, err := s.services.GetState()
				if err != nil {
					continue
				}
				current, _ := json.Marshal(state)
				if bytes.Equal(previous, current) {
					continue
				}
				previous = current
				if write(map[string]any{"event": "core.changed", "state": state}) != nil {
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		messageType, data, err := connection.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.TextMessage {
			continue
		}
		go func(data []byte) {
			response, shutdown := api.HandleMessage(ctx, s.services, data)
			writeMu.Lock()
			err := connection.WriteMessage(websocket.TextMessage, response)
			writeMu.Unlock()
			if err != nil {
				cancel()
				return
			}
			if shutdown {
				s.once.Do(func() { close(s.stop) })
			}
		}(append([]byte(nil), data...))
	}
}

func Listen(address string, handler http.Handler) (*http.Server, net.Listener, error) {
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return nil, nil, err
	}
	host, _, _ := net.SplitHostPort(listener.Addr().String())
	if !localHost(host) {
		listener.Close()
		return nil, nil, fmt.Errorf("management server address is not available on this device")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	return server, listener, nil
}

func localHost(host string) bool {
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || management.Available(ip.String()))
}
