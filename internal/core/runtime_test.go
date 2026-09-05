package core

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Guailoudou/opl-core/internal/invite"
	"github.com/Guailoudou/opl-core/internal/openp2p"
	"github.com/Guailoudou/opl-core/internal/pairing"
	"github.com/Guailoudou/opl-core/internal/room"
)

func TestOrdinaryOpenP2PBlocksRoomModes(t *testing.T) {
	rooms, err := room.New(room.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{rooms: rooms, ordinaryRunning: true}
	if err := runtime.StartOpenP2P(); !errors.Is(err, ErrNetworkActive) {
		t.Fatalf("duplicate ordinary start was not blocked: %v", err)
	}
	if _, err := runtime.Join(context.Background(), JoinOptions{}); !errors.Is(err, ErrNetworkActive) {
		t.Fatalf("join was not blocked: %v", err)
	}
	if err := runtime.startNetwork(room.NetworkConfig{}); !errors.Is(err, ErrNetworkActive) {
		t.Fatalf("host room was not blocked: %v", err)
	}
}

func TestDefaultConfigMatchesStandaloneDefaults(t *testing.T) {
	config := defaultConfig("0123456789abcdef")
	if config.Network.Node != "0123456789abcdef" || config.Network.Token == 0 || config.Network.ServerHost != "api.openp2p.cn" || config.Network.ShareBandwidth != 10 || config.Apps == nil {
		t.Fatalf("unexpected default config: %#v", config)
	}
}

func TestTailFileKeepsLatestBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.log")
	if err := os.WriteFile(path, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := tailFile(path, 4)
	if err != nil || string(data) != "6789" {
		t.Fatalf("tail = %q, %v", data, err)
	}
}

func TestMemberDisabledStopsPairingRetry(t *testing.T) {
	if !terminalPairError(pairing.ErrMemberDisabled) || terminalPairError(errors.New("temporary network failure")) {
		t.Fatal("member removal must stop pairing retries without treating transient failures as terminal")
	}
}

func TestLifecycleStateTransitions(t *testing.T) {
	runtime := &Runtime{}
	runtime.lifecycleState.Store("starting_openp2p")
	runtime.networkMode.Store("ordinary")
	runtime.onProcessState("online")
	if got := runtime.lifecycleState.Load(); got != "online" {
		t.Fatalf("ordinary lifecycle = %v", got)
	}
	runtime.networkMode.Store("host")
	runtime.setLifecycle("listening")
	runtime.onProcessState("faulted")
	runtime.onProcessState("online")
	if got := runtime.lifecycleState.Load(); got != "listening" {
		t.Fatalf("host recovery lifecycle = %v", got)
	}
	runtime.networkMode.Store("client")
	runtime.setLifecycle("joining")
	runtime.onProcessState("online")
	if got := runtime.lifecycleState.Load(); got != "joining" {
		t.Fatalf("client joined before pairing completed: %v", got)
	}
}

func TestMissingAndTemporaryMembershipKeysDoNotPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.secret")
	for _, fixed := range []bool{false, true} {
		if _, exists, err := loadMembershipKey(path, fixed); err != nil || exists {
			t.Fatalf("fixed=%v: missing membership key was not optional: exists=%v err=%v", fixed, exists, err)
		}
	}
}

func TestPairingThroughTemporaryOpenP2PApp(t *testing.T) {
	hostPort := freeTCPPort(t)
	rooms, _ := room.New(room.Options{DataDir: t.TempDir()})
	if _, err := rooms.Create(room.CreateOptions{HostUID: "0123456789abcdef", PairPort: hostPort}); err != nil {
		t.Fatal(err)
	}
	defer rooms.Stop()
	code, _ := rooms.GetInvite()
	invitation, _ := invite.Decode(code)

	proxy, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	proxyPort := uint16(proxy.Addr().(*net.TCPAddr).Port)
	app := openp2p.PairingApp(invitation.HostUID, invitation.PairPort, proxyPort)
	proxyDone := proxyTCPOnce(proxy, net.JoinHostPort(app.DstHost, fmt.Sprint(app.DstPort)))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	result, err := pairing.Pair(ctx, net.JoinHostPort("127.0.0.1", fmt.Sprint(app.SrcPort)), pairing.RoomKey(invitation.RoomKey), sha256.Sum256([]byte("client")), "player")
	cancel()
	if err != nil || result.AssignedIP.String() != "10.0.23.2" {
		t.Fatalf("pairing through temporary OpenP2P app failed: %#v %v", result, err)
	}
	if err := <-proxyDone; err != nil {
		t.Fatal(err)
	}
}

func TestDeviceSyncReconnectsWithMemberPSK(t *testing.T) {
	roomHash := sha256.Sum256([]byte("room"))
	var roomKey pairing.RoomKey
	copy(roomKey[:], roomHash[:])
	clientKey := sha256.Sum256([]byte("client"))
	memberPSK := sha256.Sum256([]byte("member-psk"))
	reports := make(chan pairing.Device, 1)
	server, err := pairing.Listen("127.0.0.1:0", pairing.ServerConfig{
		RoomKey: roomKey, HostPublicKey: sha256.Sum256([]byte("host")),
		WireGuardPort: 1, DiscoveryRelayPort: 2, JoinEnabled: func() bool { return true },
		MemberAuthKey: func(key [32]byte) ([32]byte, bool) { return memberPSK, key == clientKey },
		Assign: func([32]byte, string, pairing.RoomKey, bool) (pairing.Assignment, error) {
			return pairing.Assignment{IP: netip.MustParseAddr("10.0.23.2"), Revision: 1}, nil
		},
		ReportStats: func(_ [32]byte, device pairing.Device) { reports <- device },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	pairContext, cancelPair := context.WithTimeout(context.Background(), 2*time.Second)
	_, disconnected, err := pairing.PairWithSession(pairContext, server.Addr().String(), roomKey, clientKey, "player", nil)
	cancelPair()
	if err != nil {
		t.Fatal(err)
	}
	_ = disconnected.Close()

	runtime := &Runtime{clientSession: true, clientControl: disconnected}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	started := time.Now()
	go func() {
		runtime.syncDevices(ctx, server.Addr().String(), memberPSK, clientKey, "player", "0123456789abcdef", disconnected)
		close(done)
	}()
	select {
	case report := <-reports:
		elapsed := time.Since(started)
		if elapsed < 500*time.Millisecond || elapsed > 2*time.Second {
			t.Fatalf("reconnect report after %v, want about 1s", elapsed)
		}
		if report.UID != "0123456789abcdef" || report.VirtualIP != "10.0.23.2" {
			t.Fatalf("unexpected reconnect report: %#v", report)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("device control session did not reconnect")
	}
	deadline := time.Now().Add(time.Second)
	for {
		runtime.mu.Lock()
		devices := append([]pairing.Device(nil), runtime.clientDevices...)
		control := runtime.clientControl
		runtime.mu.Unlock()
		if len(devices) == 1 {
			_ = control.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reconnected device snapshot was not stored")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("device sync worker did not stop")
	}
}

func proxyTCPOnce(listener net.Listener, destination string) <-chan error {
	done := make(chan error, 1)
	go func() {
		client, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		upstream, err := net.Dial("tcp4", destination)
		if err != nil {
			client.Close()
			done <- err
			return
		}
		copies := make(chan error, 2)
		go func() { _, copyErr := io.Copy(upstream, client); copies <- copyErr }()
		go func() { _, copyErr := io.Copy(client, upstream); copies <- copyErr }()
		<-copies
		client.Close()
		upstream.Close()
		done <- nil
	}()
	return done
}

func freeTCPPort(t *testing.T) uint16 {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return uint16(listener.Addr().(*net.TCPAddr).Port)
}
