package openp2p

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const internalPrefix = "_opl2_"

var ErrConfig = errors.New("invalid OpenP2P configuration")

type Network struct {
	Token          Uint64 `json:"Token"`
	Node           string `json:"Node"`
	User           string `json:"User"`
	ShareBandwidth int    `json:"ShareBandwidth"`
	ServerHost     string `json:"ServerHost"`
	ServerPort     int    `json:"ServerPort"`
	PublicIPPort   int    `json:"PublicIPPort"`
}

type Uint64 uint64

func (v *Uint64) UnmarshalJSON(data []byte) error {
	text := strings.Trim(string(data), `"`)
	parsed, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return ErrConfig
	}
	*v = Uint64(parsed)
	return nil
}

func (v Uint64) MarshalJSON() ([]byte, error) {
	// JSON numbers cannot safely carry uint64 values through browser JavaScript.
	return []byte(`"` + strconv.FormatUint(uint64(v), 10) + `"`), nil
}

type App struct {
	AppName          string `json:"AppName"`
	Protocol         string `json:"Protocol"`
	UnderlayProtocol string `json:"UnderlayProtocol,omitempty"`
	PunchPriority    int    `json:"PunchPriority,omitempty"`
	Whitelist        string `json:"Whitelist"`
	SrcPort          int    `json:"SrcPort"`
	PeerNode         string `json:"PeerNode"`
	DstPort          int    `json:"DstPort"`
	DstHost          string `json:"DstHost"`
	PeerUser         string `json:"PeerUser"`
	RelayNode        string `json:"RelayNode"`
	ForceRelay       int    `json:"ForceRelay,omitempty"`
	Enabled          int    `json:"Enabled"`
}

type Config struct {
	Network  Network `json:"Network"`
	Apps     []App   `json:"Apps"`
	LogLevel int     `json:"LogLevel"`
}

type Store struct {
	mu   sync.Mutex
	path string
}

func NewStore(path string) *Store { return &Store{path: path} }

func (s *Store) Load() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return load(s.path)
}

func (s *Store) UserApps() ([]App, error) {
	config, err := s.Load()
	if err != nil {
		return nil, err
	}
	return userApps(config.Apps), nil
}

func (s *Store) PublicConfig() (Config, error) {
	config, err := s.Load()
	if err == nil {
		config.Apps = userApps(config.Apps)
	}
	return config, err
}

func (s *Store) ReplaceUserApps(apps []App) error {
	for _, app := range apps {
		if isInternal(app) || validateApp(app) != nil {
			return ErrConfig
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := load(s.path)
	if err != nil {
		return err
	}
	internal := internalApps(config.Apps)
	config.Apps = append(append([]App(nil), apps...), internal...)
	return save(s.path, config)
}

func (s *Store) Replace(config Config) error {
	for _, app := range config.Apps {
		if isInternal(app) {
			return ErrConfig
		}
	}
	if err := validateConfig(config); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := load(s.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	config.Apps = append(append([]App(nil), config.Apps...), internalApps(current.Apps)...)
	return save(s.path, config)
}

func (s *Store) SetInternal(apps []App) error {
	for _, app := range apps {
		if !isInternal(app) || validateApp(app) != nil {
			return ErrConfig
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := load(s.path)
	if err != nil {
		return err
	}
	config.Apps = append(userApps(config.Apps), apps...)
	return save(s.path, config)
}

func (s *Store) ClearInternal() error { return s.SetInternal(nil) }

func PairingApp(hostUID string, destinationPort, sourcePort uint16) App {
	return App{
		AppName: internalPrefix + "pair_" + hostUID, Protocol: "tcp", PeerNode: hostUID,
		SrcPort: int(sourcePort), DstPort: int(destinationPort), DstHost: "127.0.0.1", Enabled: 1,
	}
}

func WireGuardApp(hostUID string, destinationPort, sourcePort uint16) App {
	return App{
		AppName: internalPrefix + "wg_" + hostUID, Protocol: "udp", PeerNode: hostUID,
		SrcPort: int(sourcePort), DstPort: int(destinationPort), DstHost: "127.0.0.1", Enabled: 1,
	}
}

func load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var config Config
	if err := decoder.Decode(&config); err != nil || decoder.Decode(&struct{}{}) != io.EOF || validateConfig(config) != nil {
		return Config{}, ErrConfig
	}
	if config.Apps == nil {
		config.Apps = []App{}
	}
	return config, nil
}

func save(path string, config Config) error {
	if err := validateConfig(config); err != nil {
		return err
	}
	// Keep the on-disk token numeric for compatibility with the embedded
	// OpenP2P engine. The public API still serializes Uint64 as a string so a
	// browser cannot lose precision while editing the configuration.
	disk := struct {
		Network struct {
			Token          uint64 `json:"Token"`
			Node           string `json:"Node"`
			User           string `json:"User"`
			ShareBandwidth int    `json:"ShareBandwidth"`
			ServerHost     string `json:"ServerHost"`
			ServerPort     int    `json:"ServerPort"`
			PublicIPPort   int    `json:"PublicIPPort"`
		} `json:"Network"`
		Apps     []App `json:"Apps"`
		LogLevel int   `json:"LogLevel"`
	}{Apps: config.Apps, LogLevel: config.LogLevel}
	disk.Network.Token = uint64(config.Network.Token)
	disk.Network.Node = config.Network.Node
	disk.Network.User = config.Network.User
	disk.Network.ShareBandwidth = config.Network.ShareBandwidth
	disk.Network.ServerHost = config.Network.ServerHost
	disk.Network.ServerPort = config.Network.ServerPort
	disk.Network.PublicIPPort = config.Network.PublicIPPort
	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func validateConfig(config Config) error {
	if config.Network.Node == "" || config.Network.ServerHost == "" || config.Network.ServerPort < 1 || config.Network.ServerPort > 65535 || config.Network.ShareBandwidth < 0 || config.Network.PublicIPPort < 0 || config.Network.PublicIPPort > 65535 {
		return ErrConfig
	}
	if config.Network.Token == 0 {
		return ErrConfig
	}
	seen := make(map[string]bool, len(config.Apps))
	for _, app := range config.Apps {
		if validateApp(app) != nil {
			return ErrConfig
		}
		key := fmt.Sprintf("%s/%d", app.Protocol, app.SrcPort)
		if app.Enabled == 1 && seen[key] {
			return ErrConfig
		}
		if app.Enabled == 1 {
			seen[key] = true
		}
	}
	return nil
}

func validateApp(app App) error {
	protocol := strings.ToLower(app.Protocol)
	if app.AppName == "" || (protocol != "tcp" && protocol != "udp") || app.SrcPort < 1 || app.SrcPort > 65535 || app.DstPort < 1 || app.DstPort > 65535 || app.DstHost == "" || (app.Enabled != 0 && app.Enabled != 1) {
		return ErrConfig
	}
	return nil
}

func isInternal(app App) bool { return strings.HasPrefix(app.AppName, internalPrefix) }

func userApps(apps []App) []App {
	result := make([]App, 0, len(apps))
	for _, app := range apps {
		if !isInternal(app) {
			result = append(result, app)
		}
	}
	return result
}

func internalApps(apps []App) []App {
	result := make([]App, 0, len(apps))
	for _, app := range apps {
		if isInternal(app) {
			result = append(result, app)
		}
	}
	return result
}
