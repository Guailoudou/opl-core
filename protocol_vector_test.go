package oplcore_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"os"
	"testing"

	"github.com/Guailoudou/opl-core/internal/invite"
	"github.com/Guailoudou/opl-core/internal/pairing"
)

func TestProtocolV2Vector(t *testing.T) {
	var vector struct {
		Invite struct {
			FixedRoomKey bool   `json:"fixedRoomKey"`
			HostUID      string `json:"hostUID"`
			PairPort     uint16 `json:"pairPort"`
			RoomKeyHex   string `json:"roomKeyHex"`
			Encoded      string `json:"encoded"`
		} `json:"invite"`
		Pairing struct {
			RoomKeyText             string `json:"roomKeyText"`
			HostPublicKeySHA256Of   string `json:"hostPublicKeySHA256Of"`
			ClientPublicKeySHA256Of string `json:"clientPublicKeySHA256Of"`
			ServerNonceByte         byte   `json:"serverNonceByte"`
			ClientNonceByte         byte   `json:"clientNonceByte"`
			ClientName              string `json:"clientName"`
			AssignedIP              string `json:"assignedIP"`
			PrefixLength            byte   `json:"prefixLength"`
			WireGuardPort           uint16 `json:"wireGuardPort"`
			DiscoveryRelayPort      uint16 `json:"discoveryRelayPort"`
			LeaseRevision           uint64 `json:"leaseRevision"`
			RequestMACHex           string `json:"requestMACHex"`
			ResultMACHex            string `json:"resultMACHex"`
			PSKHex                  string `json:"pskHex"`
			AckHex                  string `json:"ackHex"`
		} `json:"pairing"`
	}
	data, err := os.ReadFile("testdata/protocol-v2.json")
	if err == nil {
		err = json.Unmarshal(data, &vector)
	}
	if err != nil {
		t.Fatalf("read protocol vector: %v", err)
	}

	roomKeyBytes, err := hex.DecodeString(vector.Invite.RoomKeyHex)
	if err != nil || len(roomKeyBytes) != 12 {
		t.Fatal("invalid invite room key vector")
	}
	var inviteKey [12]byte
	copy(inviteKey[:], roomKeyBytes)
	encoded, err := invite.Encode(invite.Invite{
		FixedRoomKey: vector.Invite.FixedRoomKey,
		HostUID:      vector.Invite.HostUID,
		PairPort:     vector.Invite.PairPort,
		RoomKey:      inviteKey,
	})
	if err != nil || encoded != vector.Invite.Encoded {
		t.Fatalf("invite vector mismatch: %q, %v", encoded, err)
	}

	var roomKey pairing.RoomKey
	if copy(roomKey[:], vector.Pairing.RoomKeyText) != len(roomKey) {
		t.Fatal("invalid pairing room key vector")
	}
	hostKey := sha256.Sum256([]byte(vector.Pairing.HostPublicKeySHA256Of))
	clientKey := sha256.Sum256([]byte(vector.Pairing.ClientPublicKeySHA256Of))
	challenge, err := pairing.NewServerChallenge(hostKey, bytes.NewReader(bytes.Repeat([]byte{vector.Pairing.ServerNonceByte}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	request, err := pairing.NewClientRequest(challenge, clientKey, vector.Pairing.ClientName, roomKey,
		bytes.NewReader(bytes.Repeat([]byte{vector.Pairing.ClientNonceByte}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	result, err := pairing.NewPairResult(challenge, request, netip.MustParseAddr(vector.Pairing.AssignedIP),
		vector.Pairing.PrefixLength, vector.Pairing.WireGuardPort, vector.Pairing.DiscoveryRelayPort,
		vector.Pairing.LeaseRevision, roomKey)
	if err != nil {
		t.Fatal(err)
	}
	assertHex(t, "request MAC", request.MAC[:], vector.Pairing.RequestMACHex)
	assertHex(t, "result MAC", result.MAC[:], vector.Pairing.ResultMACHex)
	psk := pairing.DerivePSK(roomKey, hostKey, clientKey)
	assertHex(t, "PSK", psk[:], vector.Pairing.PSKHex)
	ack := pairing.PairAck(roomKey, vector.Pairing.LeaseRevision)
	assertHex(t, "ack", ack[:], vector.Pairing.AckHex)
}

func assertHex(t *testing.T, name string, value []byte, expected string) {
	t.Helper()
	if hex.EncodeToString(value) != expected {
		t.Fatalf("%s vector mismatch", name)
	}
}
