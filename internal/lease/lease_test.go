package lease

import (
	"errors"
	"net/netip"
	"sync"
	"testing"
)

func TestAssignReconnectAndReuse(t *testing.T) {
	a, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	key := keyFor(1)
	first, created, err := a.Assign(key, "first")
	if err != nil || !created || first.IP != netip.MustParseAddr("10.0.23.2") {
		t.Fatalf("unexpected first lease: %#v %v %v", first, created, err)
	}
	reconnected, created, err := a.Assign(key, "renamed")
	if err != nil || created || reconnected.IP != first.IP || reconnected.Revision != first.Revision || reconnected.Name != "renamed" {
		t.Fatalf("unexpected reconnect: %#v %v %v", reconnected, created, err)
	}
	if _, ok := a.Remove(key); !ok {
		t.Fatal("remove failed")
	}
	reused, _, _ := a.Assign(keyFor(2), "second")
	if reused.IP != first.IP {
		t.Fatalf("expected address reuse, got %v", reused.IP)
	}
}

func TestConcurrentPoolIsUniqueAndBounded(t *testing.T) {
	a, _ := New(nil)
	var wg sync.WaitGroup
	errorsFound := make(chan error, 253)
	for i := 1; i <= 253; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := a.Assign(keyFor(i), "")
			if err != nil {
				errorsFound <- err
			}
		}(i)
	}
	wg.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	leasing := a.Snapshot()
	if len(leasing) != 253 {
		t.Fatalf("expected 253 leases, got %d", len(leasing))
	}
	for i, item := range leasing {
		expected := netip.AddrFrom4([4]byte{10, 0, 23, byte(i + 2)})
		if item.IP != expected {
			t.Fatalf("address %d: got %v, want %v", i, item.IP, expected)
		}
	}
	if _, _, err := a.Assign(keyFor(254), ""); !errors.Is(err, ErrFull) {
		t.Fatalf("expected ErrFull, got %v", err)
	}
}

func TestRestoreRejectsConflicts(t *testing.T) {
	ip := netip.MustParseAddr("10.0.23.2")
	_, err := New([]Lease{{PublicKey: keyFor(1), IP: ip}, {PublicKey: keyFor(2), IP: ip}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected conflict rejection, got %v", err)
	}
}

func keyFor(value int) [32]byte {
	var key [32]byte
	key[0] = byte(value)
	key[1] = byte(value >> 8)
	return key
}
