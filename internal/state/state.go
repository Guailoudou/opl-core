package state

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
)

const SchemaVersion = 1

var ErrInvalid = errors.New("invalid room state")

type Member struct {
	PublicKey     string `json:"publicKey"`
	IP            string `json:"ip"`
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	LeaseRevision uint64 `json:"leaseRevision"`
}

type Room struct {
	SchemaVersion  int      `json:"schemaVersion"`
	RoomID         string   `json:"roomId"`
	HostUID        string   `json:"hostUid"`
	PairPort       uint16   `json:"pairPort"`
	WireGuardPort  uint16   `json:"wireGuardPort"`
	RelayPort      uint16   `json:"discoveryRelayPort"`
	JoinEnabled    bool     `json:"joinEnabled"`
	Members        []Member `json:"members"`
	BlockedMembers []string `json:"blockedMembers,omitempty"`
	BlockedUIDs    []string `json:"blockedUids,omitempty"`
}

func Load(path string) (Room, error) {
	value, err := loadOne(path)
	if err == nil {
		return value, nil
	}
	backup, backupErr := loadOne(path + ".bak")
	if backupErr != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Room{}, err
		}
		return Room{}, fmt.Errorf("load room state: %w", err)
	}
	return backup, nil
}

func Save(path string, value Room) error {
	if err := validate(value); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if old, err := os.ReadFile(path); err == nil {
		var previous Room
		if json.Unmarshal(old, &previous) == nil && validate(previous) == nil {
			if err := writeAtomic(path+".bak", old); err != nil {
				return err
			}
		}
	}
	return writeAtomic(path, data)
}

func loadOne(path string) (Room, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Room{}, err
	}
	var value Room
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Room{}, ErrInvalid
	}
	if err := validate(value); err != nil {
		return Room{}, err
	}
	return value, nil
}

func validate(value Room) error {
	roomID, err := hex.DecodeString(value.RoomID)
	if value.SchemaVersion != SchemaVersion || err != nil || len(roomID) != 16 || value.PairPort == 0 || value.WireGuardPort == 0 || value.RelayPort == 0 {
		return ErrInvalid
	}
	uid, err := hex.DecodeString(value.HostUID)
	if err != nil || len(uid) != 8 || hex.EncodeToString(uid) != value.HostUID {
		return ErrInvalid
	}
	keys := make(map[string]bool, len(value.Members))
	ips := make(map[netip.Addr]bool, len(value.Members))
	for _, member := range value.Members {
		key, keyErr := base64.StdEncoding.DecodeString(member.PublicKey)
		ip, ipErr := netip.ParseAddr(member.IP)
		if keyErr != nil || len(key) != 32 || ipErr != nil || !validMemberIP(ip) || member.LeaseRevision == 0 || keys[member.PublicKey] || ips[ip] {
			return ErrInvalid
		}
		keys[member.PublicKey], ips[ip] = true, true
	}
	for _, encoded := range value.BlockedMembers {
		key, keyErr := base64.StdEncoding.DecodeString(encoded)
		if keyErr != nil || len(key) != 32 || keys[encoded] {
			return ErrInvalid
		}
		keys[encoded] = true
	}
	blockedUIDs := make(map[string]bool, len(value.BlockedUIDs))
	for _, blockedUID := range value.BlockedUIDs {
		decoded, decodeErr := hex.DecodeString(blockedUID)
		if decodeErr != nil || len(decoded) != 8 || hex.EncodeToString(decoded) != blockedUID || blockedUID == value.HostUID || blockedUIDs[blockedUID] {
			return ErrInvalid
		}
		blockedUIDs[blockedUID] = true
	}
	return nil
}

func validMemberIP(ip netip.Addr) bool {
	if !ip.Is4() {
		return false
	}
	b := ip.As4()
	return b[0] == 10 && b[1] == 0 && b[2] == 23 && b[3] >= 2 && b[3] <= 254
}

func writeAtomic(path string, data []byte) (err error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
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
	return os.Rename(temporaryName, path)
}
