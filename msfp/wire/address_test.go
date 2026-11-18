package wire

import (
	"testing"
	"time"
)

// While a server moves (core section 13.1), a request signed for its
// former host is accepted too, and said so; one for any other host is
// still refused, and so is the former one once the move is over.
func TestFormerHost(t *testing.T) {
	k := NewKey()
	body := []byte(`{"a":1}`)
	former := "films.example.org" // request signs for it
	v := &Verifier{Host: "new.example.org", Former: func() string { return former }}
	req, _ := request(k, "POST", "/p", body, now)
	got, err := v.Check(req, body, now)
	if err != nil || !got.Former {
		t.Fatalf("signed for the former host: %+v, %v", got, err)
	}
	v = &Verifier{Host: "films.example.org", Former: func() string { return "old.example.org" }}
	req, _ = request(k, "POST", "/p", body, now)
	if got, err := v.Check(req, body, now); err != nil || got.Former {
		t.Fatalf("signed for the current host: %+v, %v", got, err)
	}
	for _, old := range []string{"", "other.example.org"} {
		former = old
		v := &Verifier{Host: "new.example.org", Former: func() string { return former }}
		req, _ := request(k, "POST", "/p", body, now)
		if _, err := v.Check(req, body, now); err == nil {
			t.Errorf("former %q: a request for films.example.org accepted", old)
		}
	}
}

// The address a hello announces (core section 13.2, step 1).
func TestAnnounced(t *testing.T) {
	for _, c := range []struct {
		stored, address, want string
		bad                   bool
	}{
		{"https://a.example.org", "", "", false},
		{"https://a.example.org", "https://a.example.org", "", false},
		{"https://a.example.org", "https://b.example.org:8443", "https://b.example.org:8443", false},
		{"https://a.example.org", "https://b.example.org/", "", true},
		{"https://a.example.org", "https://B.example.org", "", true},
		{"https://a.example.org", "http://b.example.org", "", true},
		{"https://a.example.org", "https://b.example.org:443", "", true},
	} {
		got, err := Announced(c.stored, c.address)
		if got != c.want || (err != nil) != c.bad {
			t.Errorf("%q after %q: %q, %v", c.address, c.stored, got, err)
		}
	}
}

// Confirming a new address: the first hello at once, then at most once an
// hour while it fails, and the operator warned after a day of failures.
func TestMove(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	m := Move{Address: "https://b.example.org"}
	if !m.Due(start) || m.Warn(start) {
		t.Fatal("a new move: due, no warning")
	}
	m.Tried, m.Failing = start, start
	if m.Due(start.Add(59*time.Minute)) || !m.Due(start.Add(time.Hour)) {
		t.Error("retried within the hour, or not after it")
	}
	if m.Warn(start.Add(23*time.Hour)) || !m.Warn(start.Add(24*time.Hour)) {
		t.Error("warned before a day of failures, or not after it")
	}
	if got := Host("https://b.example.org:8443"); got != "b.example.org:8443" {
		t.Errorf("host: %q", got)
	}
}
