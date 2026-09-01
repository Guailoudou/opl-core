package invite

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"strings"
)

const (
	Prefix      = "OPL2."
	payloadSize = 27
	fixedFlag   = 1
)

var (
	ErrFormat  = errors.New("invalid OPL2 invite")
	ErrVersion = errors.New("unsupported invite version")
)

type Invite struct {
	FixedRoomKey bool
	HostUID      string
	PairPort     uint16
	RoomKey      [12]byte
}

func Encode(v Invite) (string, error) {
	uid, err := hex.DecodeString(v.HostUID)
	if err != nil || len(uid) != 8 || v.HostUID != strings.ToLower(v.HostUID) || v.PairPort == 0 || isZero(v.RoomKey[:]) {
		return "", ErrFormat
	}

	payload := make([]byte, payloadSize)
	if v.FixedRoomKey {
		payload[0] = fixedFlag
	}
	copy(payload[1:9], uid)
	binary.BigEndian.PutUint16(payload[9:11], v.PairPort)
	copy(payload[11:23], v.RoomKey[:])
	binary.BigEndian.PutUint32(payload[23:], crc32.ChecksumIEEE(payload[:23]))
	return Prefix + base64.RawURLEncoding.EncodeToString(payload), nil
}

func Decode(code string) (Invite, error) {
	if !strings.HasPrefix(code, Prefix) {
		return Invite{}, ErrVersion
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, Prefix))
	if err != nil || len(payload) != payloadSize || payload[0]&^fixedFlag != 0 {
		return Invite{}, ErrFormat
	}
	if crc32.ChecksumIEEE(payload[:23]) != binary.BigEndian.Uint32(payload[23:]) {
		return Invite{}, ErrFormat
	}

	var roomKey [12]byte
	copy(roomKey[:], payload[11:23])
	port := binary.BigEndian.Uint16(payload[9:11])
	if port == 0 || isZero(roomKey[:]) {
		return Invite{}, ErrFormat
	}

	return Invite{
		FixedRoomKey: payload[0]&fixedFlag != 0,
		HostUID:      hex.EncodeToString(payload[1:9]),
		PairPort:     port,
		RoomKey:      roomKey,
	}, nil
}

func isZero(value []byte) bool {
	var combined byte
	for _, b := range value {
		combined |= b
	}
	return combined == 0
}
