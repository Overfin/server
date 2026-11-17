package gateway

import (
	"net/netip"
	"testing"
	"time"
)

// Addresses of one IPv6 /64 share a limit, and the memory of addresses
// has a cap.
func TestLimiterIPv6AndMemory(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	l := &limiter{now: func() time.Time { return now }}
	refused := 0
	for i := range Burst + 50 {
		if !l.allow(netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, 15: byte(i), 14: byte(i >> 8)})) {
			refused++
		}
	}
	if refused != 50 {
		t.Errorf("one /64, new address each time: %d refused, want 50", refused)
	}
	for i := range 2 * maxBuckets {
		l.allow(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}))
	}
	if n := len(l.buckets); n > maxBuckets {
		t.Errorf("%d addresses remembered", n)
	}
}
