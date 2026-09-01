package device

import (
	"crypto/ecdh"
	cryptorand "crypto/rand"
	"errors"
	"os"

	"github.com/Guailoudou/opl-core/internal/secret"
)

var ErrInvalid = errors.New("invalid device key")

type KeyPair struct {
	Private [32]byte
	Public  [32]byte
}

func LoadOrCreate(path string, fixed bool) (KeyPair, error) {
	if fixed {
		if data, err := secret.Load(path); err == nil {
			return FromPrivate(data)
		} else if !errors.Is(err, os.ErrNotExist) {
			return KeyPair{}, err
		}
	}
	value, err := Generate()
	if err == nil && fixed {
		err = secret.Save(path, value.Private[:])
	}
	return value, err
}

func Generate() (KeyPair, error) {
	private, err := ecdh.X25519().GenerateKey(cryptorand.Reader)
	if err != nil {
		return KeyPair{}, err
	}
	return fromECDH(private), nil
}

func FromPrivate(data []byte) (KeyPair, error) {
	if len(data) != 32 {
		return KeyPair{}, ErrInvalid
	}
	private, err := ecdh.X25519().NewPrivateKey(data)
	if err != nil {
		return KeyPair{}, ErrInvalid
	}
	return fromECDH(private), nil
}

func fromECDH(private *ecdh.PrivateKey) KeyPair {
	var value KeyPair
	copy(value.Private[:], private.Bytes())
	copy(value.Public[:], private.PublicKey().Bytes())
	return value
}
