package openp2p

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNewLoggerDoesNotExitWhenLogDirectoryIsUnavailable(t *testing.T) {
	path := t.TempDir()
	logPath := filepath.Join(path, "log", ProductName+logFileNames)
	if err := os.MkdirAll(logPath, 0700); err != nil {
		t.Fatal(err)
	}
	if logger := NewLogger(path, ProductName, LvINFO, 1024, LogFile); logger != nil {
		logger.close()
		t.Fatal("logger unexpectedly opened a directory as a file")
	}
}

func TestProtocolLogsExcludeSecrets(t *testing.T) {
	directory := t.TempDir()
	logger := NewLogger(directory, ProductName, LvDEBUG, 0, LogFile)
	if logger == nil {
		t.Fatal("logger did not open")
	}
	secret := uint64(18446744073709551001)
	request := ServerSideSaveMemApp{From: "0123456789abcdef", Node: "fedcba9876543210", TunnelID: 11, AppID: 17, AppKey: secret, SrcPort: 26674}
	logger.d("memapp %s", request.logSummary())
	logger.close()
	data, err := os.ReadFile(filepath.Join(directory, "log", ProductName+logFileNames))
	if err != nil {
		t.Fatal(err)
	}
	logText := string(data)
	if strings.Contains(logText, strconv.FormatUint(secret, 10)) || !strings.Contains(logText, "appID=17") {
		t.Fatalf("protocol log exposed a secret or omitted safe metadata: %q", logText)
	}
}
