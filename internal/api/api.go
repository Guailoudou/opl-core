package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/Guailoudou/opl-core/internal/core"
	"github.com/Guailoudou/opl-core/internal/invite"
	"github.com/Guailoudou/opl-core/internal/openp2p"
	"github.com/Guailoudou/opl-core/internal/pairing"
	"github.com/Guailoudou/opl-core/internal/room"
	"github.com/Guailoudou/opl-core/internal/secret"
	"github.com/Guailoudou/opl-core/internal/state"
)

const (
	APIVersion   = 1
	CoreVersion  = "2.0.0"
	maxLineBytes = 64 * 1024
)

type request struct {
	ID     uint64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type protocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type response struct {
	ID     uint64         `json:"id"`
	Result any            `json:"result,omitempty"`
	Error  *protocolError `json:"error,omitempty"`
}

type Services struct {
	Rooms          *room.Service
	GetState       func() (core.State, error)
	ListTunnels    func() ([]openp2p.App, error)
	ReplaceTunnels func([]openp2p.App) error
	GetConfig      func() (openp2p.Config, error)
	ReplaceConfig  func(openp2p.Config) error
	StartOpenP2P   func() error
	StopOpenP2P    func() error
	Join           func(context.Context, core.JoinOptions) (core.JoinResult, error)
	Leave          func() error
	Shutdown       func() error
	ReadLogs       func() (string, error)
	GetManagement  func() (ManagementInfo, error)
	SetManagement  func(string) (ManagementInfo, error)
	SetAllowedIPs  func([]string) (ManagementInfo, error)
}

type ManagementInterface struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

type ManagementInfo struct {
	Address    string                `json:"address"`
	URL        string                `json:"url"`
	Interfaces []ManagementInterface `json:"interfaces"`
	AllowedIPs []string              `json:"allowedIPs"`
}

type protocolWriter struct {
	mu      sync.Mutex
	encoder *json.Encoder
}

func (w *protocolWriter) encode(value any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.encoder.Encode(value)
}

func Run(input io.Reader, output io.Writer, platform string) error {
	rooms, err := room.New(room.Options{})
	if err != nil {
		return err
	}
	return RunWithRoom(input, output, platform, rooms)
}

func RunWithRoom(input io.Reader, output io.Writer, platform string, rooms *room.Service) error {
	return RunWithServices(input, output, platform, Services{Rooms: rooms})
}

func RunWithServices(input io.Reader, output io.Writer, platform string, services Services) error {
	if services.Rooms == nil {
		return errors.New("room service is required")
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	writer := &protocolWriter{encoder: encoder}
	if err := writer.encode(Hello(platform)); err != nil {
		return err
	}
	done := make(chan struct{})
	var eventWG, commandWG sync.WaitGroup
	eventWG.Add(1)
	go func() {
		defer eventWG.Done()
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		previous, _ := json.Marshal(services.Rooms.Snapshot())
		for {
			select {
			case <-ticker.C:
				snapshot := services.Rooms.Snapshot()
				current, _ := json.Marshal(snapshot)
				if !bytes.Equal(previous, current) {
					previous = current
					_ = writer.encode(map[string]any{"event": "room.changed", "room": snapshot})
				}
			case <-done:
				return
			}
		}
	}()
	defer func() { commandWG.Wait(); close(done); eventWG.Wait() }()

	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), maxLineBytes)
	for scanner.Scan() {
		var req request
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil || decoder.Decode(&struct{}{}) != io.EOF || req.ID == 0 || req.Method == "" {
			if err := writer.encode(response{ID: req.ID, Error: invalidRequest()}); err != nil {
				return err
			}
			continue
		}

		if req.Method == "room.join" {
			commandWG.Add(1)
			go func(req request) {
				defer commandWG.Done()
				result, commandErr, _ := dispatch(context.Background(), services, req)
				if commandErr != nil {
					_ = writer.encode(response{ID: req.ID, Error: commandErr})
				} else {
					_ = writer.encode(response{ID: req.ID, Result: result})
				}
			}(req)
			continue
		}

		result, commandErr, shutdown := dispatch(context.Background(), services, req)
		if commandErr != nil {
			if err := writer.encode(response{ID: req.ID, Error: commandErr}); err != nil {
				return err
			}
		} else if err := writer.encode(response{ID: req.ID, Result: result}); err != nil {
			return err
		}
		if shutdown {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read IPC: %w", err)
	}
	if services.Shutdown != nil {
		return services.Shutdown()
	}
	return services.Rooms.Stop()
}

func Hello(platform string) map[string]any {
	return map[string]any{
		"event": "hello", "apiVersion": APIVersion, "coreVersion": CoreVersion, "platform": platform,
		"capabilities": []string{"openp2p", "wireguard-hot-update", "invite-v2", "pairing-v2", "pairing-tcp-sync-v2", "room-v1", "fixed-secret", "lease-v1", "discovery-relay-v1", "websocket-v1", "logs-v1", "management-interface-v1", "management-access-list-v1", "windows-tray-v1"},
	}
}

// HandleMessage applies one WebSocket request using the same strict protocol
// and command dispatcher as the legacy stdio transport.
func HandleMessage(ctx context.Context, services Services, data []byte) ([]byte, bool) {
	var req request
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if len(data) > maxLineBytes || decoder.Decode(&req) != nil || decoder.Decode(&struct{}{}) != io.EOF || req.ID == 0 || req.Method == "" {
		encoded, _ := json.Marshal(response{ID: req.ID, Error: invalidRequest()})
		return encoded, false
	}
	result, commandErr, shutdown := dispatch(ctx, services, req)
	value := response{ID: req.ID, Result: result, Error: commandErr}
	encoded, _ := json.Marshal(value)
	return encoded, shutdown
}

func dispatch(ctx context.Context, services Services, req request) (any, *protocolError, bool) {
	rooms := services.Rooms
	switch req.Method {
	case "core.getState":
		if !emptyParams(req.Params) {
			return nil, invalidRequest(), false
		}
		if services.GetState != nil {
			value, err := services.GetState()
			return value, commandError(err), false
		}
		return rooms.Snapshot(), nil, false
	case "room.create":
		if !emptyParams(req.Params) {
			return nil, invalidRequest(), false
		}
		value, err := rooms.StartOrCreate(room.CreateOptions{FixedRoomKey: true, FixedHostKey: true})
		if err != nil {
			return nil, commandError(err), false
		}
		code, err := rooms.GetInvite()
		if err != nil {
			return nil, commandError(err), false
		}
		return map[string]any{"invite": code, "room": value}, nil, false
	case "room.start":
		if !emptyParams(req.Params) {
			return nil, invalidRequest(), false
		}
		value, err := rooms.Start()
		return value, commandError(err), false
	case "room.stop":
		if !emptyParams(req.Params) {
			return nil, invalidRequest(), false
		}
		err := rooms.Stop()
		return rooms.Snapshot(), commandError(err), false
	case "room.setJoinEnabled":
		var params struct {
			Enabled *bool `json:"enabled"`
		}
		if !decodeParams(req.Params, &params) || params.Enabled == nil {
			return nil, invalidRequest(), false
		}
		value, err := rooms.SetJoinEnabled(*params.Enabled)
		return value, commandError(err), false
	case "room.rotateKey":
		if !emptyParams(req.Params) {
			return nil, invalidRequest(), false
		}
		code, err := rooms.RotateKey()
		return map[string]string{"invite": code}, commandError(err), false
	case "room.getInvite":
		if !emptyParams(req.Params) {
			return nil, invalidRequest(), false
		}
		code, err := rooms.GetInvite()
		return map[string]string{"invite": code}, commandError(err), false
	case "room.removeMember":
		var params struct {
			PublicKey string `json:"publicKey"`
		}
		if !decodeParams(req.Params, &params) || params.PublicKey == "" {
			return nil, invalidRequest(), false
		}
		value, err := rooms.RemoveMember(params.PublicKey)
		return value, commandError(err), false
	case "room.blockMember":
		var params struct {
			PublicKey string `json:"publicKey"`
		}
		if !decodeParams(req.Params, &params) || params.PublicKey == "" {
			return nil, invalidRequest(), false
		}
		value, err := rooms.BlockMember(params.PublicKey)
		return value, commandError(err), false
	case "room.unblockUID":
		var params struct {
			UID string `json:"uid"`
		}
		if !decodeParams(req.Params, &params) || params.UID == "" {
			return nil, invalidRequest(), false
		}
		value, err := rooms.UnblockUID(params.UID)
		return value, commandError(err), false
	case "room.renameMember":
		var params struct {
			PublicKey string `json:"publicKey"`
			Name      string `json:"name"`
		}
		if !decodeParams(req.Params, &params) || params.PublicKey == "" {
			return nil, invalidRequest(), false
		}
		value, err := rooms.RenameMember(params.PublicKey, params.Name)
		return value, commandError(err), false
	case "room.join":
		var params struct {
			Invite string `json:"invite"`
			Name   string `json:"name"`
		}
		if !decodeParams(req.Params, &params) || params.Invite == "" {
			return nil, invalidRequest(), false
		}
		if services.Join == nil {
			return nil, notReady(), false
		}
		value, err := services.Join(ctx, core.JoinOptions{Invite: params.Invite, Name: params.Name, FixedDeviceKey: true})
		return value, commandError(err), false
	case "room.leave":
		if !emptyParams(req.Params) {
			return nil, invalidRequest(), false
		}
		if services.Leave == nil {
			return nil, notReady(), false
		}
		err := services.Leave()
		return rooms.Snapshot(), commandError(err), false
	case "tunnel.list":
		if !emptyParams(req.Params) {
			return nil, invalidRequest(), false
		}
		if services.ListTunnels == nil {
			return nil, notReady(), false
		}
		apps, err := services.ListTunnels()
		return apps, commandError(err), false
	case "tunnel.replace":
		var params struct {
			Apps []openp2p.App `json:"apps"`
		}
		if !decodeParams(req.Params, &params) || params.Apps == nil {
			return nil, invalidRequest(), false
		}
		if services.ReplaceTunnels == nil {
			return nil, notReady(), false
		}
		err := services.ReplaceTunnels(params.Apps)
		return params.Apps, commandError(err), false
	case "config.get":
		if !emptyParams(req.Params) || services.GetConfig == nil {
			return nil, invalidRequest(), false
		}
		value, err := services.GetConfig()
		return value, commandError(err), false
	case "config.replace":
		var params struct {
			Config *openp2p.Config `json:"config"`
		}
		if !decodeParams(req.Params, &params) || params.Config == nil || services.ReplaceConfig == nil {
			return nil, invalidRequest(), false
		}
		err := services.ReplaceConfig(*params.Config)
		return params.Config, commandError(err), false
	case "openp2p.start":
		if !emptyParams(req.Params) || services.StartOpenP2P == nil {
			return nil, invalidRequest(), false
		}
		err := services.StartOpenP2P()
		return map[string]string{"state": "starting"}, commandError(err), false
	case "openp2p.stop":
		if !emptyParams(req.Params) || services.StopOpenP2P == nil {
			return nil, invalidRequest(), false
		}
		err := services.StopOpenP2P()
		return map[string]string{"state": "stopped"}, commandError(err), false
	case "log.read":
		if !emptyParams(req.Params) || services.ReadLogs == nil {
			return nil, invalidRequest(), false
		}
		value, err := services.ReadLogs()
		return map[string]string{"text": value}, commandError(err), false
	case "management.get":
		if !emptyParams(req.Params) || services.GetManagement == nil {
			return nil, invalidRequest(), false
		}
		value, err := services.GetManagement()
		return value, commandError(err), false
	case "management.setAddress":
		var params struct {
			Address string `json:"address"`
		}
		if !decodeParams(req.Params, &params) || params.Address == "" || services.SetManagement == nil {
			return nil, invalidRequest(), false
		}
		value, err := services.SetManagement(params.Address)
		return value, commandError(err), false
	case "management.setAllowedIPs":
		var params struct {
			AllowedIPs []string `json:"allowedIPs"`
		}
		if !decodeParams(req.Params, &params) || params.AllowedIPs == nil || services.SetAllowedIPs == nil {
			return nil, invalidRequest(), false
		}
		value, err := services.SetAllowedIPs(params.AllowedIPs)
		return value, commandError(err), false
	case "core.shutdown":
		if !emptyParams(req.Params) {
			return nil, invalidRequest(), false
		}
		var err error
		if services.Shutdown != nil {
			err = services.Shutdown()
		} else {
			err = rooms.Stop()
		}
		return map[string]string{"state": "stopped"}, commandError(err), err == nil
	default:
		return nil, &protocolError{Code: "METHOD_NOT_FOUND", Message: "Unknown method"}, false
	}
}

func notReady() *protocolError {
	return &protocolError{Code: "OPENP2P_NOT_READY", Message: "OpenP2P is not ready"}
}

func decodeParams(raw json.RawMessage, target any) bool {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && decoder.Decode(&struct{}{}) == io.EOF
}

func emptyParams(raw json.RawMessage) bool {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true
	}
	var params struct{}
	return decodeParams(raw, &params)
}

func invalidRequest() *protocolError {
	return &protocolError{Code: "INVALID_REQUEST", Message: "Invalid request"}
}

func commandError(err error) *protocolError {
	if err == nil {
		return nil
	}
	code := "INTERNAL_ERROR"
	switch {
	case errors.Is(err, room.ErrInput):
		code = "INVALID_REQUEST"
	case errors.Is(err, invite.ErrFormat):
		code = "INVALID_INVITE"
	case errors.Is(err, invite.ErrVersion):
		code = "UNSUPPORTED_PROTOCOL"
	case errors.Is(err, pairing.ErrAuthFailed):
		code = "AUTH_FAILED"
	case errors.Is(err, pairing.ErrJoinPaused):
		code = "JOIN_DISABLED"
	case errors.Is(err, pairing.ErrRoomFull):
		code = "ROOM_FULL"
	case errors.Is(err, pairing.ErrMemberDisabled):
		code = "MEMBER_DISABLED"
	case errors.Is(err, core.ErrOpenP2PNotReady):
		code = "OPENP2P_NOT_READY"
	case errors.Is(err, core.ErrWireGuardFailed):
		code = "WIREGUARD_FAILED"
	case errors.Is(err, core.ErrWireGuardUpdate):
		code = "WIREGUARD_UPDATE_FAILED"
	case errors.Is(err, secret.ErrStore), errors.Is(err, secret.ErrUnavailable):
		code = "SECRET_STORE_FAILED"
	case errors.Is(err, openp2p.ErrConfig), errors.Is(err, state.ErrInvalid):
		code = "CONFIG_WRITE_FAILED"
	case errors.Is(err, os.ErrNotExist):
		code = "INVALID_STATE"
	case isPathError(err):
		code = "CONFIG_WRITE_FAILED"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		code = "TIMEOUT"
	case errors.Is(err, room.ErrRunning), errors.Is(err, room.ErrStopped), errors.Is(err, core.ErrNetworkActive):
		code = "INVALID_STATE"
	case errors.Is(err, room.ErrMember):
		code = "MEMBER_NOT_FOUND"
	default:
		var networkError *net.OpError
		if errors.As(err, &networkError) {
			if networkError.Op == "listen" {
				code = "PAIR_PORT_IN_USE"
			} else {
				code = "OPENP2P_NOT_READY"
			}
		}
	}
	return &protocolError{Code: code, Message: err.Error()}
}

func isPathError(err error) bool {
	var pathError *os.PathError
	return errors.As(err, &pathError)
}
