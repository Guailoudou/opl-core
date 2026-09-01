package invite

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	original := Invite{
		FixedRoomKey: true,
		HostUID:      "0123456789abcdef",
		PairPort:     25673,
		RoomKey:      [12]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11},
	}
	code, err := Encode(original)
	if err != nil {
		t.Fatal(err)
	}
	const expected = "OPL2.AQEjRWeJq83vZEkAAQIDBAUGBwgJCgs_FMSY"
	if code != expected {
		t.Fatalf("invite vector changed: got %q, want %q", code, expected)
	}
	decoded, err := Decode(code)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != original {
		t.Fatalf("round trip mismatch: %#v", decoded)
	}
	if len(code) > 48 {
		t.Fatalf("invite too long: %d", len(code))
	}
}

func TestRejectsDamagedInvite(t *testing.T) {
	key := [12]byte{1}
	code, err := Encode(Invite{HostUID: "0123456789abcdef", PairPort: 1, RoomKey: key})
	if err != nil {
		t.Fatal(err)
	}
	last := code[len(code)-1]
	replacement := byte('A')
	if last == replacement {
		replacement = 'B'
	}
	_, err = Decode(code[:len(code)-1] + string(replacement))
	if !errors.Is(err, ErrFormat) {
		t.Fatalf("expected ErrFormat, got %v", err)
	}
}

func TestRejectsInvalidFields(t *testing.T) {
	validKey := [12]byte{1}
	for _, value := range []Invite{
		{HostUID: "bad", PairPort: 1, RoomKey: validKey},
		{HostUID: "0123456789ABCDEF", PairPort: 1, RoomKey: validKey},
		{HostUID: "0123456789abcdef", PairPort: 0, RoomKey: validKey},
		{HostUID: "0123456789abcdef", PairPort: 1},
	} {
		if _, err := Encode(value); !errors.Is(err, ErrFormat) {
			t.Fatalf("expected ErrFormat for %#v, got %v", value, err)
		}
	}
}

func TestRejectsInvalidDecodedFields(t *testing.T) {
	valid, _ := Encode(Invite{HostUID: "0123456789abcdef", PairPort: 25673, RoomKey: [12]byte{1}})
	payload, _ := base64.RawURLEncoding.DecodeString(valid[len(Prefix):])
	invalid := [][]byte{append([]byte(nil), payload[:len(payload)-1]...)}
	for _, mutate := range []func([]byte){
		func(value []byte) { value[0] = 0x80 },
		func(value []byte) { binary.BigEndian.PutUint16(value[9:11], 0) },
		func(value []byte) {
			for i := 11; i < 23; i++ {
				value[i] = 0
			}
		},
	} {
		value := append([]byte(nil), payload...)
		mutate(value)
		binary.BigEndian.PutUint32(value[23:], crc32.ChecksumIEEE(value[:23]))
		invalid = append(invalid, value)
	}
	for _, value := range invalid {
		code := Prefix + base64.RawURLEncoding.EncodeToString(value)
		if _, err := Decode(code); !errors.Is(err, ErrFormat) {
			t.Fatalf("invalid decoded payload was accepted: %x, %v", value, err)
		}
	}
}
