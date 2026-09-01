package lease

import (
	"errors"
	"net/netip"
	"sort"
	"sync"
)

var (
	ErrFull    = errors.New("room address pool is full")
	ErrInvalid = errors.New("invalid lease")
)

type Lease struct {
	PublicKey [32]byte
	IP        netip.Addr
	Name      string
	Enabled   bool
	Revision  uint64
}

type Allocator struct {
	mu       sync.Mutex
	byKey    map[[32]byte]Lease
	used     [256]bool
	revision uint64
}

func New(initial []Lease) (*Allocator, error) {
	a := &Allocator{byKey: make(map[[32]byte]Lease)}
	a.used[0], a.used[1], a.used[255] = true, true, true
	for _, item := range initial {
		octet, ok := memberOctet(item.IP)
		if !ok || isZeroKey(item.PublicKey) || a.used[octet] {
			return nil, ErrInvalid
		}
		if _, exists := a.byKey[item.PublicKey]; exists {
			return nil, ErrInvalid
		}
		a.used[octet] = true
		a.byKey[item.PublicKey] = item
		if item.Revision > a.revision {
			a.revision = item.Revision
		}
	}
	return a, nil
}

func (a *Allocator) Assign(publicKey [32]byte, name string) (Lease, bool, error) {
	if isZeroKey(publicKey) {
		return Lease{}, false, ErrInvalid
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if existing, ok := a.byKey[publicKey]; ok {
		if existing.Name != name {
			existing.Name = name
			a.byKey[publicKey] = existing
		}
		return existing, false, nil
	}
	for octet := 2; octet <= 254; octet++ {
		if a.used[octet] {
			continue
		}
		a.revision++
		item := Lease{
			PublicKey: publicKey,
			IP:        netip.AddrFrom4([4]byte{10, 0, 23, byte(octet)}),
			Name:      name,
			Enabled:   true,
			Revision:  a.revision,
		}
		a.used[octet] = true
		a.byKey[publicKey] = item
		return item, true, nil
	}
	return Lease{}, false, ErrFull
}

func (a *Allocator) Remove(publicKey [32]byte) (Lease, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	item, ok := a.byKey[publicKey]
	if !ok {
		return Lease{}, false
	}
	octet, _ := memberOctet(item.IP)
	a.used[octet] = false
	delete(a.byKey, publicKey)
	return item, true
}

func (a *Allocator) Restore(item Lease) error {
	octet, ok := memberOctet(item.IP)
	if !ok || isZeroKey(item.PublicKey) || item.Revision == 0 {
		return ErrInvalid
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	existing, exists := a.byKey[item.PublicKey]
	if exists && existing.IP != item.IP {
		return ErrInvalid
	}
	if a.used[octet] && (!exists || existing.IP != item.IP) {
		return ErrInvalid
	}
	a.used[octet] = true
	a.byKey[item.PublicKey] = item
	if item.Revision > a.revision {
		a.revision = item.Revision
	}
	return nil
}

func (a *Allocator) RemoveIfRevision(publicKey [32]byte, revision uint64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	item, ok := a.byKey[publicKey]
	if !ok || item.Revision != revision {
		return false
	}
	octet, _ := memberOctet(item.IP)
	a.used[octet] = false
	delete(a.byKey, publicKey)
	return true
}

func (a *Allocator) Snapshot() []Lease {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]Lease, 0, len(a.byKey))
	for _, item := range a.byKey {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].IP.Less(result[j].IP)
	})
	return result
}

func memberOctet(ip netip.Addr) (byte, bool) {
	if !ip.Is4() {
		return 0, false
	}
	v := ip.As4()
	return v[3], v[0] == 10 && v[1] == 0 && v[2] == 23 && v[3] >= 2 && v[3] <= 254
}

func isZeroKey(key [32]byte) bool {
	var combined byte
	for _, b := range key {
		combined |= b
	}
	return combined == 0
}
