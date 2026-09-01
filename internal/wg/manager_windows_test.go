//go:build windows

package wg

import "testing"

func TestParsePeerStats(t *testing.T) {
	value := "private_key=hidden\nlisten_port=25674\n" +
		"public_key=0100000000000000000000000000000000000000000000000000000000000000\n" +
		"endpoint=127.0.0.1:1234\nlast_handshake_time_sec=1700000000\nlast_handshake_time_nsec=123\n" +
		"tx_bytes=2048\nrx_bytes=1024\npersistent_keepalive_interval=25\nerrno=0\n\n"
	stats, err := parsePeerStats(value)
	if err != nil || len(stats) != 1 || stats[0].PublicKey[0] != 1 || stats[0].RxBytes != 1024 || stats[0].TxBytes != 2048 || stats[0].LastHandshake.Unix() != 1700000000 {
		t.Fatalf("unexpected stats: %#v, %v", stats, err)
	}
}
