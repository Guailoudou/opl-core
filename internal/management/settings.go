package management

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const Loopback = "127.0.0.1"

type Interface struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

type Store struct {
	mu   sync.Mutex
	path string
}

type settingsFile struct {
	ManagementAddress string   `json:"managementAddress"`
	AllowedIPs        []string `json:"allowedIPs,omitempty"`
	raw               map[string]json.RawMessage
}

func NewStore(path string) *Store { return &Store{path: path} }

func (s *Store) Load() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings, err := s.load()
	return settings.ManagementAddress, err
}

func (s *Store) Save(address string) error {
	if !Available(address) {
		return errors.New("selected address is not available on this device")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	settings, err := s.load()
	if err != nil {
		return err
	}
	settings.ManagementAddress = address
	return s.save(settings)
}

func (s *Store) AllowedIPs(currentAddress string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings, err := s.load()
	if err != nil {
		return nil, err
	}
	if len(settings.AllowedIPs) == 0 {
		items, listErr := Interfaces()
		if listErr != nil {
			return nil, listErr
		}
		for _, item := range items {
			settings.AllowedIPs = append(settings.AllowedIPs, item.Address)
		}
	}
	return normalizeAllowed(settings.AllowedIPs, currentAddress)
}

func (s *Store) SaveAllowedIPs(values []string, currentAddress string) ([]string, error) {
	allowed, err := normalizeAllowed(values, currentAddress)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	settings, err := s.load()
	if err != nil {
		return nil, err
	}
	settings.AllowedIPs = allowed
	if err := s.save(settings); err != nil {
		return nil, err
	}
	return allowed, nil
}

func (s *Store) load() (settingsFile, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return settingsFile{ManagementAddress: Loopback}, nil
	}
	if err != nil {
		return settingsFile{}, err
	}
	var settings settingsFile
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return settingsFile{}, errors.New("invalid management settings")
	}
	if value, ok := raw["managementAddress"]; ok && json.Unmarshal(value, &settings.ManagementAddress) != nil {
		return settingsFile{}, errors.New("invalid management settings")
	}
	if value, ok := raw["allowedIPs"]; ok && json.Unmarshal(value, &settings.AllowedIPs) != nil {
		return settingsFile{}, errors.New("invalid management settings")
	}
	settings.raw = raw
	if settings.ManagementAddress == "" {
		settings.ManagementAddress = Loopback
	}
	return settings, nil
}

func (s *Store) save(settings settingsFile) error {
	raw := settings.raw
	if raw == nil {
		raw = make(map[string]json.RawMessage)
	}
	address, _ := json.Marshal(settings.ManagementAddress)
	raw["managementAddress"] = address
	if len(settings.AllowedIPs) == 0 {
		delete(raw, "allowedIPs")
	} else {
		allowed, _ := json.Marshal(settings.AllowedIPs)
		raw["allowedIPs"] = allowed
	}
	data, _ := json.MarshalIndent(raw, "", "  ")
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), "set.json.tmp-")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(append(data, '\n'))
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, s.path)
}

func normalizeAllowed(values []string, currentAddress string) ([]string, error) {
	result := make([]string, 0, len(values)+2)
	seen := make(map[string]bool, len(values)+2)
	for _, value := range append(append([]string(nil), values...), Loopback, currentAddress) {
		ip := net.ParseIP(value)
		if ip == nil || ip.To4() == nil {
			return nil, errors.New("access list only supports individual IPv4 addresses")
		}
		value = ip.String()
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result, nil
}

func Interfaces() ([]Interface, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	result := []Interface{{Name: "仅本机", Address: Loopback}}
	for _, item := range interfaces {
		if item.Flags&net.FlagUp == 0 || item.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := item.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.To4() != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
				result = append(result, Interface{Name: item.Name, Address: ip.String()})
			}
		}
	}
	sort.Slice(result[1:], func(i, j int) bool { return result[i+1].Name < result[j+1].Name })
	return result, nil
}

func Available(address string) bool {
	items, err := Interfaces()
	if err != nil {
		return false
	}
	for _, item := range items {
		if item.Address == address {
			return true
		}
	}
	return false
}
