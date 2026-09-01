//go:build !windows

package secret

import (
	"bytes"
	"errors"
)

// Unix protects per-user secrets with directory/file permissions. Unlike
// Windows DPAPI this does not claim encryption at rest.
var unixHeader = []byte("OPL-PLAIN-1\x00")

func protect(value []byte) ([]byte, error) {
	return append(append([]byte(nil), unixHeader...), value...), nil
}

func unprotect(value []byte) ([]byte, error) {
	if !bytes.HasPrefix(value, unixHeader) {
		return nil, errors.New("invalid Unix secret format")
	}
	return append([]byte(nil), value[len(unixHeader):]...), nil
}
