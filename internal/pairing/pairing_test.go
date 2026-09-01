package pairing

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"
)

func TestPairingRoundTrip(t *testing.T) {
	roomKey := roomKeyForTest()
	hostKey := sha256.Sum256([]byte("host"))
	clientKey := sha256.Sum256([]byte("client"))
	challenge, err := NewServerChallenge(hostKey, bytes.NewReader(bytes.Repeat([]byte{1}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	request, err := NewClientRequest(challenge, clientKey, "玩家一", roomKey, bytes.NewReader(bytes.Repeat([]byte{2}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	requestBytes, err := request.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	parsedRequest, err := ParseClientRequest(requestBytes)
	if err != nil || !VerifyClientRequest(challenge, parsedRequest, roomKey) {
		t.Fatalf("request verification failed: %v", err)
	}

	result, err := NewPairResult(challenge, parsedRequest, netip.MustParseAddr("10.0.23.2"), 24, 25674, 25675, 1, roomKey)
	if err != nil {
		t.Fatal(err)
	}
	resultBytes, err := result.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	parsedResult, err := ParsePairResult(resultBytes)
	if err != nil || !VerifyPairResult(challenge, parsedRequest, parsedResult, roomKey) {
		t.Fatalf("result verification failed: %v", err)
	}

	ack := PairAck(roomKey, parsedResult.LeaseRevision)
	if !VerifyPairAck(roomKey, parsedResult.LeaseRevision, ack) {
		t.Fatal("ack verification failed")
	}
	if request.MAC != [32]byte{0x6f, 0x7f, 0x23, 0x06, 0x79, 0xad, 0x79, 0x6e, 0xd1, 0xe7, 0x66, 0x5d, 0x99, 0x8b, 0xb6, 0xb1, 0x9d, 0xbf, 0x29, 0xb5, 0x28, 0x97, 0xa5, 0x3d, 0xb2, 0x66, 0x4f, 0x1e, 0x7d, 0xd8, 0xca, 0xeb} {
		t.Fatal("request MAC vector changed")
	}
	if result.MAC != [32]byte{0x11, 0x25, 0x6e, 0x00, 0x81, 0x38, 0xf8, 0x64, 0x62, 0x40, 0x48, 0x60, 0xaf, 0x37, 0xef, 0x99, 0x1e, 0xa5, 0x6d, 0xdd, 0x52, 0x7b, 0xd6, 0xf1, 0x0c, 0x87, 0x94, 0x2c, 0xce, 0x4c, 0x79, 0x87} {
		t.Fatal("result MAC vector changed")
	}
	psk := DerivePSK(roomKey, hostKey, clientKey)
	if psk != [32]byte{0x7d, 0x64, 0x18, 0xa0, 0x46, 0xae, 0x50, 0x01, 0x40, 0x38, 0x40, 0xd7, 0xc1, 0x1b, 0x81, 0x5f, 0xb9, 0x05, 0x47, 0xe8, 0x3a, 0x1d, 0xbe, 0xa0, 0xd8, 0xf6, 0xa7, 0x46, 0xda, 0x62, 0xd6, 0x81} {
		t.Fatal("PSK vector changed")
	}
	if ack != [32]byte{0xf0, 0xeb, 0x8c, 0x05, 0x72, 0x54, 0xed, 0x35, 0x8b, 0xd4, 0x17, 0x53, 0xfd, 0x85, 0xfe, 0xfc, 0xae, 0xfb, 0x19, 0xea, 0x04, 0x6c, 0xc0, 0x5e, 0x67, 0xee, 0x35, 0x0b, 0x43, 0xb6, 0xb2, 0x94} {
		t.Fatal("ack vector changed")
	}
	wire := append(challenge.MarshalBinary(), requestBytes...)
	wire = append(wire, resultBytes...)
	wire = append(wire, ack[:]...)
	if bytes.Contains(wire, roomKey[:]) || bytes.Contains(wire, psk[:]) {
		t.Fatal("pairing wire data exposed RoomKey or PSK")
	}
}

func TestPairingRejectsTampering(t *testing.T) {
	roomKey := roomKeyForTest()
	hostKey := sha256.Sum256([]byte("host"))
	clientKey := sha256.Sum256([]byte("client"))
	challenge, _ := NewServerChallenge(hostKey, bytes.NewReader(bytes.Repeat([]byte{1}, 32)))
	request, _ := NewClientRequest(challenge, clientKey, "player", roomKey, bytes.NewReader(bytes.Repeat([]byte{2}, 32)))
	request.Name = "attacker"
	if VerifyClientRequest(challenge, request, roomKey) {
		t.Fatal("tampered request accepted")
	}
}

func TestPairingRejectsReplayAndTamperedResponse(t *testing.T) {
	roomKey := roomKeyForTest()
	hostKey := sha256.Sum256([]byte("host"))
	clientKey := sha256.Sum256([]byte("client"))
	first, _ := NewServerChallenge(hostKey, bytes.NewReader(bytes.Repeat([]byte{1}, 32)))
	second, _ := NewServerChallenge(hostKey, bytes.NewReader(bytes.Repeat([]byte{2}, 32)))
	request, _ := NewClientRequest(first, clientKey, "player", roomKey, bytes.NewReader(bytes.Repeat([]byte{3}, 32)))
	if VerifyClientRequest(second, request, roomKey) {
		t.Fatal("captured request was accepted for a new challenge")
	}
	result, _ := NewPairResult(first, request, netip.MustParseAddr("10.0.23.2"), 24, 25674, 25675, 1, roomKey)
	result.AssignedIP = netip.MustParseAddr("10.0.23.3")
	if VerifyPairResult(first, request, result, roomKey) {
		t.Fatal("tampered pairing response was accepted")
	}
}

func TestFrameRoundTripAndLimit(t *testing.T) {
	var stream bytes.Buffer
	payload := []byte("payload")
	if err := WriteFrame(&stream, FrameClientRequest, payload); err != nil {
		t.Fatal(err)
	}
	typeValue, decoded, err := ReadFrame(&stream)
	if err != nil {
		t.Fatal(err)
	}
	if typeValue != FrameClientRequest || !bytes.Equal(decoded, payload) {
		t.Fatal("frame mismatch")
	}
	if err := WriteFrame(&stream, 1, make([]byte, MaxFrameBytes)); !errors.Is(err, ErrProtocol) {
		t.Fatalf("expected size rejection, got %v", err)
	}
	if err := WriteFrame(&stream, 99, nil); !errors.Is(err, ErrProtocol) {
		t.Fatalf("expected type rejection, got %v", err)
	}
	var oversized [4]byte
	binary.BigEndian.PutUint32(oversized[:], MaxFrameBytes+1)
	if _, _, err := ReadFrame(bytes.NewReader(oversized[:])); !errors.Is(err, ErrProtocol) {
		t.Fatalf("expected oversized receive rejection, got %v", err)
	}
}

func TestRejectsInvalidNameAndResult(t *testing.T) {
	challenge := ServerChallenge{Version: ProtocolVersion, HostPublicKey: sha256.Sum256([]byte("host"))}
	_, err := NewClientRequest(challenge, sha256.Sum256([]byte("client")), string(bytes.Repeat([]byte{'a'}, MaxNameBytes+1)), roomKeyForTest(), bytes.NewReader(make([]byte, 32)))
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("expected invalid name rejection, got %v", err)
	}
	_, err = NewPairResult(challenge, ClientRequest{}, netip.Addr{}, 24, 1, 1, 1, roomKeyForTest())
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("expected invalid result rejection, got %v", err)
	}
}

func roomKeyForTest() RoomKey {
	var key RoomKey
	copy(key[:], "room-key-123")
	return key
}
