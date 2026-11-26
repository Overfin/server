package gateway

import (
	"net/netip"
	"testing"
)

// The trusted proxies' addresses as the environment gives them: ranges
// and single addresses, comma or space separated, ranges cut to their
// network; anything that isn't one refuses the whole list (a typo never
// trusts less, or more, than written).
func TestParsePrefixes(t *testing.T) {
	got, err := ParsePrefixes("10.0.0.0/8, 192.168.1.17/24 198.51.100.7,,  2001:db8::1 fd00::/8")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.0/8", "192.168.1.0/24", "198.51.100.7/32", "2001:db8::1/128", "fd00::/8"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i, w := range want {
		if got[i] != netip.MustParsePrefix(w) {
			t.Errorf("%d: %v, want %s", i, got[i], w)
		}
	}
	if got, err := ParsePrefixes(""); err != nil || len(got) != 0 {
		t.Errorf("none: %v %v", got, err)
	}
	for _, bad := range []string{"10.0.0.0/33", "example.org", "10.0.0.1-10.0.0.9", "10.0.0.0/8,localhost", "300.1.1.1"} {
		if got, err := ParsePrefixes(bad); err == nil {
			t.Errorf("%q: %v", bad, got)
		}
	}
}
