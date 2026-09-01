package pairing

import (
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"net/netip"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestNetworkPairingAndJoinPause(t *testing.T) {
	roomKey := roomKeyForTest()
	hostKey := sha256.Sum256([]byte("host"))
	clientKey := sha256.Sum256([]byte("client"))
	var joinEnabled atomic.Bool
	joinEnabled.Store(true)
	var acknowledged atomic.Bool
	server, err := Listen("127.0.0.1:0", ServerConfig{
		RoomKey:            roomKey,
		HostPublicKey:      hostKey,
		WireGuardPort:      25674,
		DiscoveryRelayPort: 25675,
		JoinEnabled:        joinEnabled.Load,
		Assign: func(key [32]byte, name string, _ RoomKey, _ bool) (Assignment, error) {
			if key != clientKey || name != "player" {
				t.Fatal("assignment received wrong identity")
			}
			return Assignment{IP: netip.MustParseAddr("10.0.23.2"), Revision: 1, Created: true}, nil
		},
		PairingAcknowledged: func([32]byte, Assignment) { acknowledged.Store(true) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := Pair(ctx, server.Addr().String(), roomKey, clientKey, "player")
	deadline := time.Now().Add(time.Second)
	for !acknowledged.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err != nil || result.AssignedIP.String() != "10.0.23.2" || !acknowledged.Load() {
		t.Fatalf("pairing failed: %#v, %v", result, err)
	}

	joinEnabled.Store(false)
	_, err = Pair(ctx, server.Addr().String(), roomKey, clientKey, "player")
	if !errors.Is(err, ErrJoinPaused) {
		t.Fatalf("expected join pause, got %v", err)
	}
}

func TestPairConfigureMayOutliveDialContext(t *testing.T) {
	roomKey := roomKeyForTest()
	server, err := Listen("127.0.0.1:0", ServerConfig{
		RoomKey: roomKey, HostPublicKey: sha256.Sum256([]byte("host")),
		WireGuardPort: 1, DiscoveryRelayPort: 2, JoinEnabled: func() bool { return true },
		Assign: func([32]byte, string, RoomKey, bool) (Assignment, error) {
			return Assignment{IP: netip.MustParseAddr("10.0.23.2"), Revision: 1}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	result, err := PairWithConfigure(ctx, server.Addr().String(), roomKey, sha256.Sum256([]byte("client")), "player", func(PairResult) error {
		time.Sleep(750 * time.Millisecond)
		return nil
	})
	if err != nil || result.AssignedIP.String() != "10.0.23.2" {
		t.Fatalf("slow local configuration broke pairing: %#v, %v", result, err)
	}
}

func TestNetworkPairingRejectsWrongKey(t *testing.T) {
	server, err := Listen("127.0.0.1:0", ServerConfig{
		RoomKey:            roomKeyForTest(),
		HostPublicKey:      sha256.Sum256([]byte("host")),
		WireGuardPort:      1,
		DiscoveryRelayPort: 2,
		JoinEnabled:        func() bool { return true },
		Assign: func([32]byte, string, RoomKey, bool) (Assignment, error) {
			t.Fatal("invalid request reached allocator")
			return Assignment{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	wrongKey := roomKeyForTest()
	wrongKey[0]++
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = Pair(ctx, server.Addr().String(), wrongKey, sha256.Sum256([]byte("client")), "player")
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("expected authentication failure, got %v", err)
	}
}

func TestTCPStatsAndDeviceSnapshot(t *testing.T) {
	roomKey := roomKeyForTest()
	hostKey := sha256.Sum256([]byte("host"))
	firstKey := sha256.Sum256([]byte("first"))
	secondKey := sha256.Sum256([]byte("second"))
	server, err := Listen("127.0.0.1:0", ServerConfig{
		RoomKey: roomKey, HostPublicKey: hostKey, WireGuardPort: 25674, DiscoveryRelayPort: 25675,
		JoinEnabled: func() bool { return true },
		Assign: func(key [32]byte, _ string, _ RoomKey, _ bool) (Assignment, error) {
			if key == firstKey {
				return Assignment{IP: netip.MustParseAddr("10.0.23.2"), Revision: 1}, nil
			}
			return Assignment{IP: netip.MustParseAddr("10.0.23.3"), Revision: 2}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, first, err := PairWithSession(ctx, server.Addr().String(), roomKey, firstKey, "first", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	_, second, err := PairWithSession(ctx, server.Addr().String(), roomKey, secondKey, "second", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := first.Sync(ctx, StatsReport{UID: "1111111111111111", RxBytes: 10, TxBytes: 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Sync(ctx, StatsReport{UID: "2222222222222222", RxBytes: 30, TxBytes: 40}); err != nil {
		t.Fatal(err)
	}
	devices, err := first.Sync(ctx, StatsReport{UID: "1111111111111111", RxBytes: 50, TxBytes: 60})
	if err != nil || len(devices) != 2 {
		t.Fatalf("device sync failed: %#v, %v", devices, err)
	}
	if devices[0] != (Device{UID: "1111111111111111", VirtualIP: "10.0.23.2", RxBytes: 50, TxBytes: 60}) || devices[1] != (Device{UID: "2222222222222222", VirtualIP: "10.0.23.3", RxBytes: 30, TxBytes: 40}) {
		t.Fatalf("wrong device snapshot: %#v", devices)
	}
	_, replacement, err := PairWithSession(ctx, server.Addr().String(), roomKey, secondKey, "second", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replacement.Sync(ctx, StatsReport{UID: "2222222222222222", RxBytes: 70, TxBytes: 80}); err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
	devices, err = first.Sync(ctx, StatsReport{UID: "1111111111111111", RxBytes: 50, TxBytes: 60})
	if err != nil || len(devices) != 2 || devices[1].RxBytes != 70 {
		t.Fatalf("replacement session was removed by stale disconnect: %#v, %v", devices, err)
	}
	_ = replacement.Close()
	deadline := time.Now().Add(time.Second)
	for len(devices) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		devices, err = first.Sync(ctx, StatsReport{UID: "1111111111111111", RxBytes: 50, TxBytes: 60})
	}
	if err != nil || len(devices) != 1 {
		t.Fatalf("disconnected device remained online: %#v, %v", devices, err)
	}
}

func TestRoomKeyRotationKeepsControlSession(t *testing.T) {
	oldKey := roomKeyForTest()
	newKey := oldKey
	newKey[0]++
	hostKey := sha256.Sum256([]byte("rotation-host"))
	clientKey := sha256.Sum256([]byte("rotation-client"))
	memberPSK := DerivePSK(oldKey, hostKey, clientKey)
	var assignedWithNewKey atomic.Bool
	var assignedWithMemberKey atomic.Bool
	server, err := Listen("127.0.0.1:0", ServerConfig{
		RoomKey: oldKey, HostPublicKey: hostKey, WireGuardPort: 25674, DiscoveryRelayPort: 25675,
		JoinEnabled: func() bool { return true },
		MemberAuthKey: func(key [32]byte) ([32]byte, bool) {
			return memberPSK, key == clientKey
		},
		Assign: func(key [32]byte, _ string, pairingKey RoomKey, existingAuth bool) (Assignment, error) {
			if pairingKey == newKey {
				assignedWithNewKey.Store(true)
			}
			if key == clientKey && existingAuth {
				assignedWithMemberKey.Store(true)
			}
			return Assignment{IP: netip.MustParseAddr("10.0.23.2"), Revision: 1}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, control, err := PairWithSession(ctx, server.Addr().String(), oldKey, clientKey, "player", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Sync(ctx, StatsReport{UID: "1111111111111111"}); err != nil {
		t.Fatal(err)
	}
	server.SetRoomKey(newKey)
	if _, err := control.Sync(ctx, StatsReport{UID: "1111111111111111"}); err != nil {
		t.Fatalf("rotation closed the existing control connection: %v", err)
	}
	_ = control.Close()
	_, reconnected, err := ReconnectWithSession(ctx, server.Addr().String(), memberPSK, clientKey, "player")
	if err != nil || !assignedWithMemberKey.Load() {
		t.Fatalf("existing member could not reconnect after rotation: %v", err)
	}
	defer reconnected.Close()
	if _, err := reconnected.Sync(ctx, StatsReport{UID: "1111111111111111"}); err != nil {
		t.Fatalf("reconnected control session could not sync: %v", err)
	}
	if _, err := Pair(ctx, server.Addr().String(), oldKey, sha256.Sum256([]byte("old-invite")), "old"); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("old room key remained valid: %v", err)
	}
	if _, err := Pair(ctx, server.Addr().String(), newKey, sha256.Sum256([]byte("new-invite")), "new"); err != nil || !assignedWithNewKey.Load() {
		t.Fatalf("new room key was not used: %v", err)
	}
}

func TestSilentControlConnectionTimesOutAndGoesOffline(t *testing.T) {
	key := sha256.Sum256([]byte("silent"))
	left, right := net.Pipe()
	defer right.Close()
	connection := &deadlineRecordingConn{Conn: left}
	server := &Server{
		devices:      map[[32]byte]Device{key: {UID: "1111111111111111", VirtualIP: "10.0.23.2"}},
		deviceOwners: map[[32]byte]net.Conn{key: connection},
	}
	server.control(connection, key, netip.MustParseAddr("10.0.23.2"))

	if remaining := time.Until(connection.requested); remaining < 39*time.Second || remaining > 41*time.Second {
		t.Fatalf("control idle deadline = %v, want 40s", remaining)
	}
	if len(server.devices) != 0 || len(server.deviceOwners) != 0 {
		t.Fatal("silent control connection remained online")
	}
}

type deadlineRecordingConn struct {
	net.Conn
	requested time.Time
}

func (c *deadlineRecordingConn) SetDeadline(value time.Time) error {
	c.requested = value
	return c.Conn.SetDeadline(time.Now())
}

func TestRepeatedControlReconnectsLeaveNoWorkers(t *testing.T) {
	roomKey := roomKeyForTest()
	clientKey := sha256.Sum256([]byte("reconnecting-client"))
	server, err := Listen("127.0.0.1:0", ServerConfig{
		RoomKey: roomKey, HostPublicKey: sha256.Sum256([]byte("host")),
		WireGuardPort: 1, DiscoveryRelayPort: 2, JoinEnabled: func() bool { return true },
		Assign: func([32]byte, string, RoomKey, bool) (Assignment, error) {
			return Assignment{IP: netip.MustParseAddr("10.0.23.2"), Revision: 1}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		server.rate.mu.Lock()
		server.rate.tokens, server.rate.last = 10, time.Now()
		server.rate.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, session, pairErr := PairWithSession(ctx, server.Addr().String(), roomKey, clientKey, "player", nil)
		if pairErr == nil {
			_, pairErr = session.Sync(ctx, StatsReport{UID: "1111111111111111"})
			_ = session.Close()
		}
		cancel()
		if pairErr != nil {
			t.Fatalf("reconnect %d failed: %v", i, pairErr)
		}
	}
	waitForNoConnections(t, server)
	closeServerWithin(t, server)
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.connections) != 0 || len(server.devices) != 0 || len(server.deviceOwners) != 0 {
		t.Fatal("control connection state leaked after shutdown")
	}
}

func closeServerWithin(t *testing.T, server *Server) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- server.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("pairing workers did not stop")
	}
}

func waitForNoConnections(t *testing.T, server *Server) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		count := len(server.connections)
		server.mu.Unlock()
		if count == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d pairing connections remained open", count)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestThousandFailedConnectionsLeaveListenerUsable(t *testing.T) {
	var assigned atomic.Int32
	server, err := Listen("127.0.0.1:0", ServerConfig{
		RoomKey: roomKeyForTest(), HostPublicKey: sha256.Sum256([]byte("host")),
		WireGuardPort: 1, DiscoveryRelayPort: 2, JoinEnabled: func() bool { return true },
		Assign: func([32]byte, string, RoomKey, bool) (Assignment, error) {
			assigned.Add(1)
			return Assignment{IP: netip.MustParseAddr("10.0.23.2"), Revision: 1}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		connection, dialErr := net.DialTimeout("tcp4", server.Addr().String(), time.Second)
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		connection.Close()
	}
	waitForNoConnections(t, server)
	server.rate.mu.Lock()
	server.rate.tokens, server.rate.last = 10, time.Now()
	server.rate.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, err = Pair(ctx, server.Addr().String(), roomKeyForTest(), sha256.Sum256([]byte("client")), "player")
	cancel()
	if err != nil || assigned.Load() != 1 {
		t.Fatalf("listener unusable after failures: assigned=%d err=%v", assigned.Load(), err)
	}
	waitForNoConnections(t, server)
	closeServerWithin(t, server)
}

func TestEndurancePairingListener(t *testing.T) {
	if os.Getenv("OPL_ENDURANCE") == "" {
		t.Skip("set OPL_ENDURANCE=24h (or a shorter duration) for the release gate")
	}
	duration, err := time.ParseDuration(os.Getenv("OPL_ENDURANCE"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := Listen("127.0.0.1:0", ServerConfig{
		RoomKey: roomKeyForTest(), HostPublicKey: sha256.Sum256([]byte("host")),
		WireGuardPort: 1, DiscoveryRelayPort: 2, JoinEnabled: func() bool { return true },
		Assign: func([32]byte, string, RoomKey, bool) (Assignment, error) {
			return Assignment{IP: netip.MustParseAddr("10.0.23.2"), Revision: 1}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, pairErr := Pair(ctx, server.Addr().String(), roomKeyForTest(), sha256.Sum256([]byte("client")), "player")
		cancel()
		if pairErr != nil {
			t.Fatal(pairErr)
		}
		time.Sleep(minDuration(time.Minute, time.Until(deadline)))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, err = Pair(ctx, server.Addr().String(), roomKeyForTest(), sha256.Sum256([]byte("final-client")), "player")
	cancel()
	if err != nil {
		t.Fatalf("listener rejected member after %s: %v", duration, err)
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
