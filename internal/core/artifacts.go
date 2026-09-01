//go:build windows

package core

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

var ErrArtifact = errors.New("release component verification failed")

var wintunSHA256 = map[string]string{
	"386":   "d694fa46ab4cfebcb2632d094c7aa97278eef2f8052438621766d863ae98a931",
	"amd64": "e5da8447dc2c320edc0fc52fa01885c103de8c118481f683643cacc3220dafce",
	"arm64": "f7ba89005544be9d85231a9e0d5f23b2d15b3311667e2dad0debd344918a3f80",
}

func verifyReleaseComponents(directory string) error {
	path := filepath.Join(directory, "wintun.dll")
	expected, ok := wintunSHA256[runtime.GOARCH]
	if !ok {
		return fmt.Errorf("%w: unsupported Windows architecture %s", ErrArtifact, runtime.GOARCH)
	}
	if err := verifyFileHash(path, expected); err != nil {
		return fmt.Errorf("%w: %s", ErrArtifact, filepath.Base(path))
	}
	return nil
}

func verifyFileHash(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := hash.Sum(nil)
	want, err := hex.DecodeString(expected)
	if err != nil || len(want) != sha256.Size || subtle.ConstantTimeCompare(actual, want) != 1 {
		return ErrArtifact
	}
	return nil
}
