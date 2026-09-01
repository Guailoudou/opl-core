//go:build windows

package secret

import (
	"fmt"
	"syscall"
	"unsafe"
)

const cryptprotectUIForbidden = 0x1

var (
	crypt32            = syscall.NewLazyDLL("crypt32.dll")
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	cryptProtectData   = crypt32.NewProc("CryptProtectData")
	cryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
	localFree          = kernel32.NewProc("LocalFree")
)

type dataBlob struct {
	size uint32
	data *byte
}

func protect(plaintext []byte) ([]byte, error) {
	return cryptData(cryptProtectData, plaintext)
}

func unprotect(ciphertext []byte) ([]byte, error) {
	return cryptData(cryptUnprotectData, ciphertext)
}

func cryptData(procedure *syscall.LazyProc, input []byte) ([]byte, error) {
	if len(input) == 0 {
		return nil, errorsNewEmpty()
	}
	in := blob(input)
	var out dataBlob
	result, _, callErr := procedure.Call(
		uintptr(unsafe.Pointer(&in)),
		0,
		0,
		0,
		0,
		cryptprotectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	if result == 0 {
		return nil, fmt.Errorf("DPAPI: %w", callErr)
	}
	defer localFree.Call(uintptr(unsafe.Pointer(out.data)))
	protected := (*[1 << 30]byte)(unsafe.Pointer(out.data))[:out.size:out.size]
	return append([]byte(nil), protected...), nil
}

func blob(value []byte) dataBlob {
	return dataBlob{size: uint32(len(value)), data: &value[0]}
}

func errorsNewEmpty() error { return fmt.Errorf("DPAPI input is empty") }
