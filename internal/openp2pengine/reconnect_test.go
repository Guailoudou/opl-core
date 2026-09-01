package openp2p

import (
	"testing"
	"time"
)

func TestCrashSchedulesBoundedBackoff(t *testing.T) {
	pn := &P2PNetwork{
		running:              true,
		restartCh:            make(chan bool, 1),
		shutdownCh:           make(chan struct{}),
		loginMaxDelaySeconds: DefaultLoginMaxDelaySeconds,
	}
	pn.close(true)
	select {
	case delayed := <-pn.restartCh:
		if !delayed {
			t.Fatal("crash scheduled an immediate restart")
		}
	case <-time.After(time.Second):
		t.Fatal("crash did not schedule a restart")
	}
	for i := 0; i < 100; i++ {
		delay := pn.reconnectDelay()
		if delay < ClientAPITimeout || delay >= ClientAPITimeout+DefaultLoginMaxDelaySeconds*time.Second {
			t.Fatalf("restart delay is out of bounds: %s", delay)
		}
	}
}
