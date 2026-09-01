package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Guailoudou/opl-core/internal/api"
	"github.com/Guailoudou/opl-core/internal/core"
	"github.com/Guailoudou/opl-core/internal/management"
	"github.com/Guailoudou/opl-core/internal/webapi"
)

func main() {
	stdio := flag.Bool("stdio", false, "use legacy stdin/stdout JSON protocol")
	daemon := flag.Bool("daemon", false, "run the background core")
	foreground := flag.Bool("foreground", false, "run the core in the current process")
	listen := flag.String("listen", "", "management address")
	flag.Parse()
	dataDir, err := coreDataDir()
	if err != nil {
		fatal(err)
	}
	settings := management.NewStore(filepath.Join(dataDir, "set.json"))
	selectedAddress, loadErr := settings.Load()
	if loadErr != nil || !management.Available(selectedAddress) {
		selectedAddress = management.Loopback
	}
	if *listen == "" {
		*listen = net.JoinHostPort(selectedAddress, "26780")
	}
	if !validManagementAddress(*listen) {
		fatal(fmt.Errorf("management address must belong to an available local IPv4 interface"))
	}
	listenHost, _, _ := net.SplitHostPort(*listen)
	allowedIPs, err := settings.AllowedIPs(listenHost)
	if err != nil {
		fatal(err)
	}
	var accessMu sync.RWMutex
	allowed := make(map[string]bool, len(allowedIPs))
	setAllowed := func(values []string) {
		accessMu.Lock()
		allowed = make(map[string]bool, len(values))
		for _, value := range values {
			allowed[value] = true
		}
		accessMu.Unlock()
	}
	setAllowed(allowedIPs)
	if *daemon {
		redirectDaemonLog()
	}
	if !*stdio && !*daemon && !*foreground {
		if !coreRunning(*listen) {
			logPath, err := startDaemon(*listen)
			if err != nil {
				fatal(err)
			}
			deadline := time.Now().Add(15 * time.Second)
			for !coreRunning(*listen) && time.Now().Before(deadline) {
				time.Sleep(150 * time.Millisecond)
			}
			if !coreRunning(*listen) {
				fatal(fmt.Errorf("Core 启动失败，日志：%s", logPath))
			}
		}
		if err := openBrowser("http://" + *listen + "/"); err != nil {
			fatal(err)
		}
		return
	}
	coreRuntime, err := core.New(core.Options{HostUID: os.Getenv("OPL_HOST_UID"), DataDir: dataDir, OpenP2PConfig: os.Getenv("OPL_OPENP2P_CONFIG")})
	if err != nil {
		fatal(err)
	}
	rebind := make(chan string, 1)
	var currentAddress atomic.Value
	currentAddress.Store(*listen)
	managementInfo := func(address string) (api.ManagementInfo, error) {
		items, err := management.Interfaces()
		if err != nil {
			return api.ManagementInfo{}, err
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return api.ManagementInfo{}, err
		}
		result := api.ManagementInfo{Address: host, URL: "http://" + net.JoinHostPort(host, port) + "/"}
		for _, item := range items {
			result.Interfaces = append(result.Interfaces, api.ManagementInterface{Name: item.Name, Address: item.Address})
		}
		result.AllowedIPs, err = settings.AllowedIPs(host)
		return result, err
	}
	services := api.Services{
		Rooms: coreRuntime.Rooms(), GetState: coreRuntime.State, ListTunnels: coreRuntime.ListTunnels,
		ReplaceTunnels: coreRuntime.ReplaceTunnels, Join: coreRuntime.Join,
		GetConfig: coreRuntime.GetConfig, ReplaceConfig: coreRuntime.ReplaceConfig,
		StartOpenP2P: coreRuntime.StartOpenP2P, StopOpenP2P: coreRuntime.StopOpenP2P,
		Leave: coreRuntime.Leave, Shutdown: coreRuntime.Close, ReadLogs: coreRuntime.Logs,
		GetManagement: func() (api.ManagementInfo, error) { return managementInfo(currentAddress.Load().(string)) },
		SetManagement: func(address string) (api.ManagementInfo, error) {
			if err := settings.Save(address); err != nil {
				return api.ManagementInfo{}, err
			}
			_, port, _ := net.SplitHostPort(currentAddress.Load().(string))
			next := net.JoinHostPort(address, port)
			values, err := settings.AllowedIPs(address)
			if err != nil {
				return api.ManagementInfo{}, err
			}
			setAllowed(values)
			result, err := managementInfo(next)
			if err == nil {
				go func() { time.Sleep(300 * time.Millisecond); rebind <- next }()
			}
			return result, err
		},
		SetAllowedIPs: func(values []string) (api.ManagementInfo, error) {
			address := currentAddress.Load().(string)
			host, _, _ := net.SplitHostPort(address)
			next, err := settings.SaveAllowedIPs(values, host)
			if err != nil {
				return api.ManagementInfo{}, err
			}
			setAllowed(next)
			return managementInfo(address)
		},
	}
	if *stdio {
		if err := api.RunWithServices(os.Stdin, os.Stdout, runtime.GOOS+"-"+runtime.GOARCH, services); err != nil {
			fatal(err)
		}
		return
	}
	webDir, err := findWebDir()
	if err != nil {
		fatal(err)
	}
	web := webapi.New(services, webDir, strings.Split(os.Getenv("OPL_WEB_ORIGIN"), ","))
	web.SetAccessChecker(func(address string) bool {
		ip := net.ParseIP(address)
		if ip == nil {
			return false
		}
		accessMu.RLock()
		defer accessMu.RUnlock()
		return allowed[ip.String()]
	})
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	exitTray := make(chan struct{})
	var exitOnce sync.Once
	var managementURL atomic.Value
	managementURL.Store("http://" + *listen + "/")
	closeTray := func() {}
	if *daemon {
		closeTray = startTray(func() { _ = openBrowser(managementURL.Load().(string)) }, func() { exitOnce.Do(func() { close(exitTray) }) })
	}
	defer closeTray()
	address := *listen
	running := true
	for running {
		server, listener, listenErr := webapi.Listen(address, web.Handler())
		if listenErr != nil {
			if address != net.JoinHostPort(management.Loopback, "26780") {
				address = net.JoinHostPort(management.Loopback, "26780")
				_ = settings.Save(management.Loopback)
				continue
			}
			fatal(listenErr)
		}
		currentAddress.Store(address)
		managementURL.Store("http://" + address + "/")
		fmt.Fprintf(os.Stderr, "OPL Core 管理页面：%s\n", managementURL.Load())
		serveError := make(chan error, 1)
		go func() { serveError <- server.Serve(listener) }()
		next := ""
		select {
		case <-signals:
			running = false
		case <-exitTray:
			running = false
		case <-web.Done():
			running = false
		case next = <-rebind:
		case serveErr := <-serveError:
			if serveErr != nil && serveErr != http.ErrServerClosed {
				fmt.Fprintln(os.Stderr, serveErr)
			}
			running = false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = server.Shutdown(ctx)
		cancel()
		if next != "" {
			address = next
		}
	}
	if err := coreRuntime.Close(); err != nil {
		fatal(err)
	}
}

func findWebDir() (string, error) {
	if configured := os.Getenv("OPL_WEB_DIR"); configured != "" {
		return configured, nil
	}
	executable, _ := os.Executable()
	for _, candidate := range []string{filepath.Join(filepath.Dir(executable), "web"), "web"} {
		if info, err := os.Stat(filepath.Join(candidate, "index.html")); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("web/index.html not found; set OPL_WEB_DIR")
}

func coreRunning(address string) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	response, err := client.Get("http://" + address + "/health")
	if err != nil {
		return false
	}
	defer response.Body.Close()
	var health struct {
		Status     string `json:"status"`
		Product    string `json:"product"`
		APIVersion int    `json:"apiVersion"`
	}
	return response.StatusCode == http.StatusOK && json.NewDecoder(response.Body).Decode(&health) == nil && health.Status == "ok" && health.Product == "opl-core" && health.APIVersion == api.APIVersion
}

func validManagementAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	return management.Available(host)
}

func coreDataDir() (string, error) {
	if directory := os.Getenv("OPL_DATA_DIR"); directory != "" {
		return directory, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "OPL", "core"), nil
}

func daemonLogPath() (string, error) {
	directory, err := coreDataDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	return filepath.Join(directory, "core.log"), nil
}

func redirectDaemonLog() {
	path, err := daemonLogPath()
	if err != nil {
		return
	}
	if info, err := os.Stat(path); err == nil && info.Size() > 5<<20 {
		_ = os.Rename(path, path+".1")
	}
	if log, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
		os.Stdout, os.Stderr = log, log
	}
}

func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
