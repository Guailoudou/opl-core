package room

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Guailoudou/opl-core/internal/invite"
	"github.com/Guailoudou/opl-core/internal/lease"
	"github.com/Guailoudou/opl-core/internal/pairing"
)

func TestExistingMemberAuthenticationPreservesPSK(t *testing.T) {
	key := sha256.Sum256([]byte("existing-member"))
	memberPSK := sha256.Sum256([]byte("existing-psk"))
	allocator, err := lease.New([]lease.Lease{{PublicKey: key, IP: netip.MustParseAddr("10.0.23.2"), Name: "old", Enabled: true, Revision: 1}})
	if err != nil {
		t.Fatal(err)
	}
	applyCalls := 0
	service := &Service{
		running: true, allocator: allocator, hostPublic: sha256.Sum256([]byte("host")),
		memberPSKs: map[[32]byte][32]byte{key: memberPSK}, memberStates: map[[32]byte]string{key: "configured"},
		pending: make(map[[32]byte]pendingPair), applyPeer: func(lease.Lease, [32]byte) error { applyCalls++; return nil },
	}
	assignment, err := service.assign(key, "renamed", pairing.RoomKey{1}, true)
	if err != nil || assignment.Created || service.memberPSKs[key] != memberPSK || applyCalls != 0 {
		t.Fatalf("member reconnect changed WireGuard state: assignment=%#v psk=%x apply=%d err=%v", assignment, service.memberPSKs[key], applyCalls, err)
	}
	service.pairingAcknowledged(key, assignment)
}

func TestFixedRoomPairAndRestart(t *testing.T) {
	port := freePort(t)
	directory := filepath.Join(t.TempDir(), "state")
	service, err := New(Options{DataDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(CreateOptions{HostUID: "0123456789abcdef", PairPort: port, FixedRoomKey: true, FixedHostKey: true})
	if err != nil || !created.Running {
		t.Fatalf("create failed: %#v, %v", created, err)
	}
	code, err := service.GetInvite()
	if err != nil {
		t.Fatal(err)
	}
	invitation, _ := invite.Decode(code)
	clientKey := sha256.Sum256([]byte("client"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	result, session, err := pairing.PairWithSession(ctx, net.JoinHostPort("127.0.0.1", portString(port)), pairing.RoomKey(invitation.RoomKey), clientKey, "player", nil)
	if err != nil || result.AssignedIP.String() != "10.0.23.2" {
		t.Fatalf("pair failed: %#v, %v", result, err)
	}
	if _, err := session.Sync(ctx, pairing.StatsReport{UID: "fedcba9876543210", RxBytes: 123, TxBytes: 456}); err != nil {
		t.Fatal(err)
	}
	snapshot := service.Snapshot()
	if len(snapshot.Members) != 1 || snapshot.Members[0].UID != "fedcba9876543210" || snapshot.Members[0].RxBytes != 123 || snapshot.Members[0].TxBytes != 456 {
		t.Fatalf("reported member stats missing: %#v", snapshot.Members)
	}
	_ = session.Close()
	cancel()
	if err := service.Stop(); err != nil {
		t.Fatal(err)
	}

	restarted, _ := New(Options{DataDir: directory})
	snapshot, err = restarted.StartOrCreate(CreateOptions{FixedRoomKey: true, FixedHostKey: true})
	if err != nil || len(snapshot.Members) != 1 || snapshot.Members[0].IP != "10.0.23.2" {
		t.Fatalf("restart failed: %#v, %v", snapshot, err)
	}
	defer restarted.Stop()
	restartedInvite, _ := restarted.GetInvite()
	if restartedInvite != code {
		t.Fatal("fixed invitation changed after restart")
	}
}

func TestTemporaryRoomPauseRemoveAndStop(t *testing.T) {
	port := freePort(t)
	var networkRunning, peerApplied, networkStopped atomic.Bool
	service, _ := New(Options{
		DataDir: t.TempDir(),
		StartNetwork: func(config NetworkConfig) error {
			if config.LocalIP.String() != "10.0.23.1" || config.ListenPort == 0 {
				t.Fatal("invalid network configuration")
			}
			networkRunning.Store(true)
			return nil
		},
		StopNetwork: func() error {
			networkRunning.Store(false)
			networkStopped.Store(true)
			return nil
		},
		ApplyPeer: func(lease.Lease, [32]byte) error {
			if !networkRunning.Load() {
				t.Fatal("peer applied before network start")
			}
			peerApplied.Store(true)
			return nil
		},
	})
	_, err := service.Create(CreateOptions{HostUID: "0123456789abcdef", PairPort: port})
	if err != nil {
		t.Fatal(err)
	}
	code, _ := service.GetInvite()
	invitation, _ := invite.Decode(code)
	clientKey := sha256.Sum256([]byte("client"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, err = pairing.Pair(ctx, net.JoinHostPort("127.0.0.1", portString(port)), pairing.RoomKey(invitation.RoomKey), clientKey, "player")
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if !peerApplied.Load() {
		t.Fatal("peer was not applied")
	}
	if _, err := service.SetJoinEnabled(false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	_, err = pairing.Pair(ctx, net.JoinHostPort("127.0.0.1", portString(port)), pairing.RoomKey(invitation.RoomKey), clientKey, "player")
	cancel()
	if err != nil {
		t.Fatalf("existing member could not reconnect while joining was paused: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	_, err = pairing.Pair(ctx, net.JoinHostPort("127.0.0.1", portString(port)), pairing.RoomKey(invitation.RoomKey), sha256.Sum256([]byte("new-client")), "new")
	cancel()
	if !errors.Is(err, pairing.ErrJoinPaused) {
		t.Fatalf("new member joined while joining was paused: %v", err)
	}
	memberPSK := service.memberPSKs[clientKey]
	rotatedCode, err := service.RotateKey()
	if err != nil || rotatedCode == code {
		t.Fatalf("temporary room key did not rotate: %v", err)
	}
	rotatedInvite, err := invite.Decode(rotatedCode)
	if err != nil || rotatedInvite.FixedRoomKey {
		t.Fatalf("rotated temporary invitation changed mode: %#v %v", rotatedInvite, err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	_, err = pairing.Pair(ctx, net.JoinHostPort("127.0.0.1", portString(port)), pairing.RoomKey(invitation.RoomKey), sha256.Sum256([]byte("old-invite-client")), "old")
	cancel()
	if !errors.Is(err, pairing.ErrAuthFailed) {
		t.Fatalf("old temporary invitation remained valid: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	_, reconnected, err := pairing.ReconnectWithSession(ctx, net.JoinHostPort("127.0.0.1", portString(port)), memberPSK, clientKey, "player")
	cancel()
	if err != nil {
		t.Fatalf("existing temporary member could not reconnect after rotation: %v", err)
	}
	_ = reconnected.Close()
	encoded := base64.StdEncoding.EncodeToString(clientKey[:])
	if snapshot, err := service.RemoveMember(encoded); err != nil || len(snapshot.Members) != 0 {
		t.Fatalf("remove failed: %#v, %v", snapshot, err)
	}
	if err := service.Stop(); err != nil {
		t.Fatal(err)
	}
	if !networkStopped.Load() {
		t.Fatal("network was not stopped")
	}
	if snapshot := service.Snapshot(); snapshot.Running || len(snapshot.Members) != 0 {
		t.Fatalf("temporary state survived stop: %#v", snapshot)
	}
}

func TestPeerApplyFailureRollsBackLease(t *testing.T) {
	port := freePort(t)
	service, _ := New(Options{
		DataDir:   t.TempDir(),
		ApplyPeer: func(lease.Lease, [32]byte) error { return errors.New("apply failed") },
	})
	if _, err := service.Create(CreateOptions{HostUID: "0123456789abcdef", PairPort: port}); err != nil {
		t.Fatal(err)
	}
	defer service.Stop()
	code, _ := service.GetInvite()
	decoded, _ := invite.Decode(code)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, err := pairing.Pair(ctx, net.JoinHostPort("127.0.0.1", portString(port)), pairing.RoomKey(decoded.RoomKey), sha256.Sum256([]byte("client")), "player")
	cancel()
	if err == nil || len(service.Snapshot().Members) != 0 {
		t.Fatalf("failed peer update retained a lease: members=%#v err=%v", service.Snapshot().Members, err)
	}
}

func TestFixedHostKeyDoesNotFixTemporaryRoomKey(t *testing.T) {
	directory := t.TempDir()
	first, _ := New(Options{DataDir: directory})
	_, err := first.Create(CreateOptions{HostUID: "0123456789abcdef", PairPort: freePort(t), FixedHostKey: true})
	if err != nil {
		t.Fatal(err)
	}
	firstInvite, _ := first.GetInvite()
	firstPublic := first.hostPublic
	if err := first.Stop(); err != nil {
		t.Fatal(err)
	}

	second, _ := New(Options{DataDir: directory})
	_, err = second.Create(CreateOptions{HostUID: "0123456789abcdef", PairPort: freePort(t), FixedHostKey: true})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Stop()
	secondInvite, _ := second.GetInvite()
	if second.hostPublic != firstPublic {
		t.Fatal("fixed host key changed")
	}
	firstDecoded, _ := invite.Decode(firstInvite)
	secondDecoded, _ := invite.Decode(secondInvite)
	if firstDecoded.RoomKey == secondDecoded.RoomKey {
		t.Fatal("temporary room key was unexpectedly fixed")
	}
}

func TestRemovedMemberIsBlockedAndIPIsReclaimed(t *testing.T) {
	directory := t.TempDir()
	port := freePort(t)
	service, _ := New(Options{DataDir: directory})
	_, err := service.Create(CreateOptions{HostUID: "0123456789abcdef", PairPort: port, FixedRoomKey: true, FixedHostKey: true})
	if err != nil {
		t.Fatal(err)
	}
	code, _ := service.GetInvite()
	decoded, _ := invite.Decode(code)
	firstKey := sha256.Sum256([]byte("removed-client"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, err = pairing.Pair(ctx, net.JoinHostPort("127.0.0.1", portString(port)), pairing.RoomKey(decoded.RoomKey), firstKey, "removed")
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.RemoveMember(base64.StdEncoding.EncodeToString(firstKey[:])); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	_, err = pairing.Pair(ctx, net.JoinHostPort("127.0.0.1", portString(port)), pairing.RoomKey(decoded.RoomKey), firstKey, "removed")
	cancel()
	if !errors.Is(err, pairing.ErrMemberDisabled) {
		t.Fatalf("removed member rejoined: %v", err)
	}
	secondKey := sha256.Sum256([]byte("replacement-client"))
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	result, err := pairing.Pair(ctx, net.JoinHostPort("127.0.0.1", portString(port)), pairing.RoomKey(decoded.RoomKey), secondKey, "replacement")
	cancel()
	if err != nil || result.AssignedIP.String() != "10.0.23.2" {
		t.Fatalf("IP was not reclaimed: %#v, %v", result, err)
	}
	if err := service.Stop(); err != nil {
		t.Fatal(err)
	}
	restarted, _ := New(Options{DataDir: directory})
	if _, err := restarted.Start(); err != nil {
		t.Fatal(err)
	}
	defer restarted.Stop()
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	_, err = pairing.Pair(ctx, net.JoinHostPort("127.0.0.1", portString(port)), pairing.RoomKey(decoded.RoomKey), firstKey, "removed")
	cancel()
	if !errors.Is(err, pairing.ErrMemberDisabled) {
		t.Fatalf("block did not survive restart: %v", err)
	}
}

func TestRotatedRoomRestoresExistingMemberPSK(t *testing.T) {
	directory := t.TempDir()
	port := freePort(t)
	var applied [32]byte
	service, _ := New(Options{DataDir: directory, ApplyPeer: func(_ lease.Lease, psk [32]byte) error { applied = psk; return nil }})
	_, err := service.Create(CreateOptions{HostUID: "0123456789abcdef", PairPort: port, FixedRoomKey: true, FixedHostKey: true})
	if err != nil {
		t.Fatal(err)
	}
	oldInvite, _ := service.GetInvite()
	decoded, _ := invite.Decode(oldInvite)
	clientKey := sha256.Sum256([]byte("client"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, control, err := pairing.PairWithSession(ctx, net.JoinHostPort("127.0.0.1", portString(port)), pairing.RoomKey(decoded.RoomKey), clientKey, "player", nil)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	if _, err := control.Sync(context.Background(), pairing.StatsReport{UID: "fedcba9876543210"}); err != nil {
		t.Fatal(err)
	}
	oldPSK := applied
	newInvite, err := service.RotateKey()
	if err != nil || newInvite == oldInvite || applied != oldPSK {
		t.Fatalf("rotation changed active peer: %v", err)
	}
	if _, err := control.Sync(context.Background(), pairing.StatsReport{UID: "fedcba9876543210"}); err != nil {
		t.Fatalf("rotation closed the existing control connection: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	_, err = pairing.Pair(ctx, net.JoinHostPort("127.0.0.1", portString(port)), pairing.RoomKey(decoded.RoomKey), sha256.Sum256([]byte("old-invite-client")), "old")
	cancel()
	if !errors.Is(err, pairing.ErrAuthFailed) {
		t.Fatalf("old invitation remained valid after rotation: %v", err)
	}
	if err := service.Stop(); err != nil {
		t.Fatal(err)
	}

	applied = [32]byte{}
	restarted, _ := New(Options{DataDir: directory, ApplyPeer: func(_ lease.Lease, psk [32]byte) error { applied = psk; return nil }})
	if _, err := restarted.Start(); err != nil {
		t.Fatal(err)
	}
	defer restarted.Stop()
	if applied != oldPSK {
		t.Fatal("restart did not restore the active member PSK")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	_, restartedControl, err := pairing.ReconnectWithSession(ctx, net.JoinHostPort("127.0.0.1", portString(port)), oldPSK, clientKey, "player")
	cancel()
	if err != nil || applied != oldPSK {
		t.Fatalf("fixed member did not reconnect with its persisted PSK: %v", err)
	}
	defer restartedControl.Close()
}

func freePort(t *testing.T) uint16 {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	listener.Close()
	return port
}

func portString(port uint16) string {
	return fmt.Sprint(port)
}
