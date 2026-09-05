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
	if request.MAC != [32]byte{0xad, 0x80, 0xb7, 0x2f, 0x89, 0x07, 0x95, 0x29, 0x3d, 0xa4, 0x94, 0xf1, 0x2f, 0xa5, 0x81, 0x18, 0x11, 0xa4, 0xf3, 0xda, 0xa6, 0xfe, 0x66, 0x83, 0x76, 0xba, 0x08, 0x7e, 0x43, 0x3a, 0x10, 0x4b} {
		t.Fatalf("request MAC vector changed: %x", request.MAC[:])
	}
	if result.MAC != [32]byte{0x23, 0x82, 0xf5, 0x22, 0xf1, 0x20, 0x40, 0x54, 0x65, 0x69, 0x63, 0x17, 0x19, 0x95, 0x64, 0x9d, 0xd1, 0x18, 0x9f, 0x81, 0xe0, 0xc9, 0xf8, 0x90, 0x3c, 0x21, 0x21, 0xc4, 0xa0, 0xfd, 0xd2, 0xc5} {
		t.Fatalf("result MAC vector changed: %x", result.MAC[:])
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

func TestPairingAuthenticatesUID(t *testing.T) {
	roomKey := roomKeyForTest()
	hostKey := sha256.Sum256([]byte("host"))
	clientKey := sha256.Sum256([]byte("client"))
	challenge, _ := NewServerChallenge(hostKey, bytes.NewReader(bytes.Repeat([]byte{1}, 32)))
	request, err := newClientRequest(challenge, clientKey, "0123456789abcdef", "player", roomKey[:], bytes.NewReader(bytes.Repeat([]byte{2}, 32)))
	encoded, marshalErr := request.MarshalBinary()
	parsed, parseErr := ParseClientRequest(encoded)
	if err != nil || marshalErr != nil || parseErr != nil || parsed.UID != request.UID || !VerifyClientRequest(challenge, parsed, roomKey) {
		t.Fatal("authenticated UID did not round trip")
	}
	parsed.UID = "fedcba9876543210"
	if VerifyClientRequest(challenge, parsed, roomKey) {
		t.Fatal("tampered UID was accepted")
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
