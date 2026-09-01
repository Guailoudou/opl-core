package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Guailoudou/opl-core/internal/core"
	"github.com/Guailoudou/opl-core/internal/invite"
	"github.com/Guailoudou/opl-core/internal/openp2p"
	"github.com/Guailoudou/opl-core/internal/pairing"
	"github.com/Guailoudou/opl-core/internal/room"
	"github.com/Guailoudou/opl-core/internal/secret"
	"github.com/Guailoudou/opl-core/internal/state"
)

func TestHelloUnknownAndShutdown(t *testing.T) {
	input := strings.NewReader("{\"id\":1,\"method\":\"unknown\"}\n{\"id\":2,\"method\":\"core.shutdown\"}\n")
	var output bytes.Buffer
	if err := Run(input, &output, "windows-386"); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	var hello map[string]any
	if err := decoder.Decode(&hello); err != nil || hello["event"] != "hello" || hello["platform"] != "windows-386" {
		t.Fatalf("invalid hello: %#v, %v", hello, err)
	}
	var unknown response
	if err := decoder.Decode(&unknown); err != nil || unknown.ID != 1 || unknown.Error == nil || unknown.Error.Code != "METHOD_NOT_FOUND" {
		t.Fatalf("invalid unknown response: %#v, %v", unknown, err)
	}
	var shutdown response
	if err := decoder.Decode(&shutdown); err != nil || shutdown.ID != 2 || shutdown.Error != nil {
		t.Fatalf("invalid shutdown response: %#v, %v", shutdown, err)
	}
}

func TestEOFShutsDownCore(t *testing.T) {
	rooms, _ := room.New(room.Options{DataDir: t.TempDir()})
	shutdowns := 0
	var output bytes.Buffer
	if err := RunWithServices(strings.NewReader(""), &output, "test", Services{
		Rooms: rooms,
		Shutdown: func() error {
			shutdowns++
			return nil
		},
	}); err != nil || shutdowns != 1 {
		t.Fatalf("EOF did not shut down Core exactly once: shutdowns=%d err=%v", shutdowns, err)
	}
}

func TestTunnelCommandsUseBackend(t *testing.T) {
	rooms, _ := room.New(room.Options{DataDir: t.TempDir()})
	var replaced []openp2p.App
	input := strings.NewReader("{\"id\":1,\"method\":\"tunnel.list\"}\n" +
		"{\"id\":2,\"method\":\"tunnel.replace\",\"params\":{\"apps\":[{\"AppName\":\"game\",\"Protocol\":\"udp\",\"SrcPort\":7777,\"PeerNode\":\"fedcba9876543210\",\"DstPort\":7777,\"DstHost\":\"127.0.0.1\",\"Whitelist\":\"\",\"PeerUser\":\"\",\"RelayNode\":\"\",\"Enabled\":1}]}}\n" +
		"{\"id\":3,\"method\":\"core.shutdown\"}\n")
	var output bytes.Buffer
	err := RunWithServices(input, &output, "test", Services{
		Rooms: rooms,
		ListTunnels: func() ([]openp2p.App, error) {
			return []openp2p.App{}, nil
		},
		ReplaceTunnels: func(apps []openp2p.App) error {
			replaced = append([]openp2p.App(nil), apps...)
			return nil
		},
	})
	if err != nil || len(replaced) != 1 || replaced[0].AppName != "game" {
		t.Fatalf("tunnel backend mismatch: %#v, %v", replaced, err)
	}
}

func TestConfigAndProcessCommandsUseBackend(t *testing.T) {
	rooms, _ := room.New(room.Options{DataDir: t.TempDir()})
	config := openp2p.Config{Network: openp2p.Network{
		Token: 1, Node: "0123456789abcdef", ShareBandwidth: 10, ServerHost: "api.openp2p.cn", ServerPort: 27183,
	}, Apps: []openp2p.App{}, LogLevel: 1}
	configJSON, _ := json.Marshal(config)
	input := strings.NewReader(
		"{\"id\":1,\"method\":\"config.get\"}\n" +
			fmt.Sprintf("{\"id\":2,\"method\":\"config.replace\",\"params\":{\"config\":%s}}\n", configJSON) +
			"{\"id\":3,\"method\":\"openp2p.start\"}\n" +
			"{\"id\":4,\"method\":\"openp2p.stop\"}\n" +
			"{\"id\":5,\"method\":\"core.shutdown\"}\n")
	var output bytes.Buffer
	var replaced, started, stopped bool
	err := RunWithServices(input, &output, "test", Services{
		Rooms:         rooms,
		GetConfig:     func() (openp2p.Config, error) { return config, nil },
		ReplaceConfig: func(value openp2p.Config) error { replaced = value.Network.Node == config.Network.Node; return nil },
		StartOpenP2P:  func() error { started = true; return nil },
		StopOpenP2P:   func() error { stopped = true; return nil },
	})
	if err != nil || !replaced || !started || !stopped {
		t.Fatalf("config/process backend mismatch: %v, %v/%v/%v", err, replaced, started, stopped)
	}
}

func TestRoomCommands(t *testing.T) {
	rooms, _ := room.New(room.Options{DataDir: t.TempDir(), HostUID: "0123456789abcdef"})
	input := strings.NewReader(
		"{\"id\":1,\"method\":\"room.create\"}\n" +
			"{\"id\":2,\"method\":\"room.setJoinEnabled\",\"params\":{\"enabled\":false}}\n" +
			"{\"id\":3,\"method\":\"core.getState\"}\n" +
			"{\"id\":4,\"method\":\"core.shutdown\"}\n")
	var output bytes.Buffer
	if err := RunWithRoom(input, &output, "test", rooms); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	var hello map[string]any
	_ = decoder.Decode(&hello)
	var created struct {
		ID     uint64 `json:"id"`
		Result struct {
			Invite string `json:"invite"`
			Room   struct {
				FixedRoomKey  bool   `json:"fixedRoomKey"`
				FixedHostKey  bool   `json:"fixedHostKey"`
				PairPort      uint16 `json:"pairPort"`
				WireGuardPort uint16 `json:"wireGuardPort"`
			} `json:"room"`
		} `json:"result"`
	}
	if err := decoder.Decode(&created); err != nil || created.ID != 1 || len(created.Result.Invite) != 41 || !created.Result.Room.FixedRoomKey || !created.Result.Room.FixedHostKey || created.Result.Room.PairPort == 0 || created.Result.Room.WireGuardPort == 0 {
		t.Fatalf("invalid create response: %#v, %v", created, err)
	}
	var paused, snapshot response
	_ = decoder.Decode(&paused)
	if err := decoder.Decode(&snapshot); err != nil || snapshot.Error != nil {
		t.Fatalf("invalid state response: %#v, %v", snapshot, err)
	}
}

func TestRejectsMalformedRequest(t *testing.T) {
	for _, input := range []string{
		"{\"id\":1,\"method\":\"x\",\"extra\":true}\n",
		"{\"id\":1,\"method\":\"x\"} {\"id\":2,\"method\":\"x\"}\n",
	} {
		var output bytes.Buffer
		if err := Run(strings.NewReader(input), &output, "test"); err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(&output)
		var ignored map[string]any
		_ = decoder.Decode(&ignored)
		var result response
		if err := decoder.Decode(&result); err != nil || result.Error == nil || result.Error.Code != "INVALID_REQUEST" {
			t.Fatalf("invalid response: %#v, %v", result, err)
		}
	}
}

func TestStableErrorCodes(t *testing.T) {
	tests := []struct {
		err  error
		code string
	}{
		{invite.ErrFormat, "INVALID_INVITE"},
		{invite.ErrVersion, "UNSUPPORTED_PROTOCOL"},
		{pairing.ErrJoinPaused, "JOIN_DISABLED"},
		{pairing.ErrAuthFailed, "AUTH_FAILED"},
		{pairing.ErrRoomFull, "ROOM_FULL"},
		{pairing.ErrMemberDisabled, "MEMBER_DISABLED"},
		{core.ErrOpenP2PNotReady, "OPENP2P_NOT_READY"},
		{core.ErrWireGuardFailed, "WIREGUARD_FAILED"},
		{core.ErrWireGuardUpdate, "WIREGUARD_UPDATE_FAILED"},
		{secret.ErrStore, "SECRET_STORE_FAILED"},
		{openp2p.ErrConfig, "CONFIG_WRITE_FAILED"},
		{state.ErrInvalid, "CONFIG_WRITE_FAILED"},
		{&os.PathError{Op: "rename", Path: "config.json", Err: errors.New("denied")}, "CONFIG_WRITE_FAILED"},
		{context.DeadlineExceeded, "TIMEOUT"},
		{&net.OpError{Op: "listen", Net: "tcp4", Err: errors.New("in use")}, "PAIR_PORT_IN_USE"},
		{&net.OpError{Op: "dial", Net: "tcp4", Err: errors.New("refused")}, "OPENP2P_NOT_READY"},
	}
	for _, test := range tests {
		if got := commandError(test.err).Code; got != test.code {
			t.Errorf("%v: got %s, want %s", test.err, got, test.code)
		}
	}
}

func TestRoomChangedEvent(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	rooms, _ := room.New(room.Options{DataDir: t.TempDir()})
	done := make(chan error, 1)
	go func() { done <- RunWithRoom(inputReader, outputWriter, "test", rooms) }()
	decoder := json.NewDecoder(outputReader)
	var hello map[string]any
	if err := decoder.Decode(&hello); err != nil {
		t.Fatal(err)
	}
	port := freeTCPPort(t)
	if _, err := rooms.Create(room.CreateOptions{HostUID: "0123456789abcdef", PairPort: port}); err != nil {
		t.Fatal(err)
	}
	event := make(chan map[string]any, 1)
	go func() {
		var message map[string]any
		if decoder.Decode(&message) == nil {
			event <- message
		}
	}()
	select {
	case message := <-event:
		if message["event"] != "room.changed" {
			t.Fatalf("unexpected event: %#v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("room.changed event was not emitted")
	}
	inputWriter.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRoomJoinDoesNotBlockStateRequests(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	rooms, _ := room.New(room.Options{DataDir: t.TempDir()})
	releaseJoin := make(chan struct{})
	var releaseOnce sync.Once
	done := make(chan error, 1)
	go func() {
		done <- RunWithServices(inputReader, outputWriter, "test", Services{
			Rooms: rooms,
			Join: func(context.Context, core.JoinOptions) (core.JoinResult, error) {
				<-releaseJoin
				return core.JoinResult{}, context.Canceled
			},
			GetState: func() (core.State, error) { return core.State{JoinState: "retrying"}, nil },
			Shutdown: func() error { releaseOnce.Do(func() { close(releaseJoin) }); return nil },
		})
	}()
	decoder := json.NewDecoder(outputReader)
	var hello map[string]any
	if err := decoder.Decode(&hello); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(inputWriter, "{\"id\":1,\"method\":\"room.join\",\"params\":{\"invite\":\"OPL2-invalid\"}}\n{\"id\":2,\"method\":\"core.getState\"}\n")
	responseReady := make(chan response, 1)
	go func() {
		var reply response
		_ = decoder.Decode(&reply)
		responseReady <- reply
	}()
	select {
	case reply := <-responseReady:
		if reply.ID != 2 || reply.Error != nil {
			t.Fatalf("state request was blocked by join: %#v", reply)
		}
	case <-time.After(time.Second):
		t.Fatal("state request was blocked by join")
	}
	drained := make(chan struct{})
	go func() {
		var reply response
		_ = decoder.Decode(&reply)
		_ = decoder.Decode(&reply)
		close(drained)
	}()
	_, _ = io.WriteString(inputWriter, "{\"id\":3,\"method\":\"core.shutdown\"}\n")
	inputWriter.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Core did not stop after canceling join")
	}
	<-drained
}

func freeTCPPort(t *testing.T) uint16 {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return uint16(listener.Addr().(*net.TCPAddr).Port)
}
