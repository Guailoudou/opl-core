package webapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Guailoudou/opl-core/internal/api"
	"github.com/Guailoudou/opl-core/internal/core"
	"github.com/gorilla/websocket"
)

func TestRejectsDNSRebindingHost(t *testing.T) {
	server := New(structServices(), t.TempDir(), nil)
	request := httptest.NewRequest(http.MethodGet, "http://evil.example/health", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestAccessListRejectsUnknownClient(t *testing.T) {
	server := New(structServices(), t.TempDir(), nil)
	server.SetAccessChecker(func(address string) bool { return address == "192.168.1.10" })
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/health", nil)
	request.RemoteAddr = "192.168.1.11:50000"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("unknown client status = %d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/health", nil)
	request.RemoteAddr = "192.168.1.10:50000"
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("allowed client status = %d", recorder.Code)
	}
}

func structServices() api.Services { return api.Services{} }

func TestWebSocketCanShutDownCore(t *testing.T) {
	services := api.Services{GetState: func() (core.State, error) { return core.State{}, nil }, Shutdown: func() error { return nil }}
	server := New(services, t.TempDir(), nil)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	header := http.Header{"Origin": []string{"http://127.0.0.1:12345"}}
	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws", header)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, _, err := connection.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	if err := connection.WriteJSON(map[string]any{"id": 1, "method": "core.shutdown"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := connection.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Done():
	case <-time.After(time.Second):
		t.Fatal("shutdown event was not emitted")
	}
}

func TestWebConsoleContainsTunnelAndNetworkWorkflows(t *testing.T) {
	for _, file := range []string{"index.html", "app.js"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "web", file))
		if err != nil || !utf8.Valid(data) {
			t.Fatalf("%s is not valid UTF-8: %v", file, err)
		}
		text := string(data)
		for _, required := range []string{"newTunnel", "quickAdd", "toggleP2P", "hostToggle", "joinToggle", "networkError"} {
			if !strings.Contains(text, required) {
				t.Fatalf("%s is missing %s", file, required)
			}
		}
	}
}
