package msfp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var now = time.Unix(1_800_000_000, 0)

func TestKeyRoundTrip(t *testing.T) {
	k := NewKey()
	again, err := KeyFromSeed(k.Seed())
	if err != nil || !SameKey(k.Public(), again.Public()) {
		t.Fatalf("rebuilt key differs: %v", err)
	}
	if _, err := KeyFromSeed([]byte("short")); err == nil {
		t.Fatal("a short seed was accepted")
	}
}

func TestFingerprint(t *testing.T) {
	a, b := NewKey(), NewKey()
	fa := Fingerprint(a.Public())
	if len(fa) != 29 || strings.Count(fa, "-") != 5 {
		t.Fatalf("fingerprint %q: want 6 groups of 4", fa)
	}
	if fa != Fingerprint(a.Public()) || fa == Fingerprint(b.Public()) {
		t.Fatal("a fingerprint must be stable and differ between keys")
	}
}

func TestCodeRoundTrip(t *testing.T) {
	k := NewKey()
	c, err := NewCode("https://films.example.org", k.Public(), now)
	if err != nil {
		t.Fatal(err)
	}
	// Pasted from an email: broken over lines, with spaces around.
	text := c.String()
	pasted := "  " + text[:20] + "\n" + text[20:] + " \n"
	got, err := ParseCode(pasted)
	if err != nil {
		t.Fatal(err)
	}
	if got.Address != c.Address || !SameKey(got.Key, k.Public()) || !bytes.Equal(got.Secret, c.Secret) ||
		!got.Expires.Equal(now.Add(CodeLifetime)) {
		t.Fatalf("round trip: %+v", got)
	}
	c2, _ := NewCode("https://films.example.org", k.Public(), now)
	if bytes.Equal(c.Secret, c2.Secret) {
		t.Fatal("two codes share a secret")
	}
}

func TestCodeRefused(t *testing.T) {
	k := NewKey()
	for _, address := range []string{"", "ftp://x.org", "https://", "https://u:p@x.org",
		"https://x.org/path", "https://x.org/?q=1", "https://x.org/#f", "x.org",
		"https://" + strings.Repeat("a", 600) + ".org"} {
		if _, err := NewCode(address, k.Public(), now); err == nil {
			t.Errorf("address %q accepted", address)
		}
	}
	good, _ := NewCode("https://10.1.2.3:8080", k.Public(), now)
	for _, text := range []string{"", "msfp-pair-1.", "msfp-pair-2." + strings.TrimPrefix(good.String(), codePrefix),
		"overfin-pair-1." + strings.TrimPrefix(good.String(), codePrefix),
		"msfp-pair-1.!!!!", good.String()[:40], "msfp-pair-1." + strings.Repeat("A", 5000)} {
		if _, err := ParseCode(text); err == nil {
			t.Errorf("code %q accepted", text)
		}
	}
	if _, err := ParseCode(good.String()); err != nil {
		t.Errorf("a private address refused: %v", err)
	}
	// Plain http: HTTPS only, said as such, unless the test switch is on.
	plain := Code{Address: "http://10.1.2.3:8080", Key: k.Public(), Secret: good.Secret, Expires: good.Expires}
	if _, err := ParseCode(plain.String()); !errors.Is(err, ErrPlainHTTP) {
		t.Errorf("a plain http code: %v", err)
	}
	if _, err := NewCode(plain.Address, k.Public(), now); !errors.Is(err, ErrPlainHTTP) {
		t.Errorf("a plain http code made: %v", err)
	}
	AllowPlainHTTPForTests(true)
	_, err := ParseCode(plain.String())
	AllowPlainHTTPForTests(false)
	if err != nil {
		t.Errorf("a plain http code, the test switch on: %v", err)
	}
	// Past 779 characters after the prefix (584 bytes): refused before
	// it is decoded.
	if _, err := ParseCode(codePrefix + strings.Repeat("A", 780)); err == nil {
		t.Error("a code of 780 characters accepted")
	}
}

// request is a signed request from k to films.example.org.
func request(k Key, method, target string, body []byte, at time.Time) (*http.Request, string) {
	req := httptest.NewRequest(method, "https://films.example.org"+target, bytes.NewReader(body))
	return req, SignRequest(req, body, k, at)
}

// An address is what browsers send as an origin: lower case, no default
// port; anything a CSP or CORS header could be broken by is refused.
func TestAddresses(t *testing.T) {
	for _, ok := range []string{"https://films.example.org", "https://192.0.2.10:8096", "https://[2001:db8::1]:8443"} {
		if CheckAddress(ok) != nil {
			t.Errorf("%s refused", ok)
		}
	}
	for _, bad := range []string{"https://evil;script-src", "https://a,b", "https://ex.com'", "https://Films.example.org",
		"https://films.example.org:443", "http://films.example.org", "https://films.example.org/path", "ftp://films.example.org"} {
		if CheckAddress(bad) == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if got := Origin("HTTPS://Films.Example.org:443/"); got != "https://films.example.org" {
		t.Errorf("origin: %q", got)
	}
}

func TestSignedRequest(t *testing.T) {
	k := NewKey()
	v := &Verifier{Host: "films.example.org"}
	body := []byte(`{"a":1}`)
	req, nonce := request(k, "POST", "/api/v1/federation/ping", body, now)
	got, err := v.Check(req, body, now.Add(time.Second))
	if err != nil || !SameKey(got.Key, k.Public()) || got.Nonce != nonce {
		t.Fatalf("valid request refused: %v", err)
	}
	// The same request again: a replay.
	if _, err := v.Check(req, body, now.Add(2*time.Second)); err == nil {
		t.Fatal("a replayed request was accepted")
	}
}

func TestSignedRequestRefused(t *testing.T) {
	k := NewKey()
	body := []byte(`{"a":1}`)
	cases := map[string]func() (*http.Request, []byte, *Verifier, time.Time){
		"changed body": func() (*http.Request, []byte, *Verifier, time.Time) {
			req, _ := request(k, "POST", "/p", body, now)
			return req, []byte(`{"a":2}`), &Verifier{Host: "films.example.org"}, now
		},
		"changed path": func() (*http.Request, []byte, *Verifier, time.Time) {
			req, _ := request(k, "POST", "/p", body, now)
			req.URL.Path = "/q"
			return req, body, &Verifier{Host: "films.example.org"}, now
		},
		"changed method": func() (*http.Request, []byte, *Verifier, time.Time) {
			req, _ := request(k, "POST", "/p", body, now)
			req.Method = "PUT"
			return req, body, &Verifier{Host: "films.example.org"}, now
		},
		"meant for another server": func() (*http.Request, []byte, *Verifier, time.Time) {
			req, _ := request(k, "POST", "/p", body, now)
			return req, body, &Verifier{Host: "other.example.org"}, now
		},
		"too old": func() (*http.Request, []byte, *Verifier, time.Time) {
			req, _ := request(k, "POST", "/p", body, now)
			return req, body, &Verifier{Host: "films.example.org"}, now.Add(MaxSkew + time.Second)
		},
		"from the future": func() (*http.Request, []byte, *Verifier, time.Time) {
			req, _ := request(k, "POST", "/p", body, now.Add(MaxSkew+time.Second))
			return req, body, &Verifier{Host: "films.example.org"}, now
		},
		"another key named": func() (*http.Request, []byte, *Verifier, time.Time) {
			req, _ := request(k, "POST", "/p", body, now)
			req.Header.Set(HeaderKey, b64.EncodeToString(NewKey().Public()))
			return req, body, &Verifier{Host: "films.example.org"}, now
		},
		"unsigned": func() (*http.Request, []byte, *Verifier, time.Time) {
			req := httptest.NewRequest("POST", "https://films.example.org/p", nil)
			return req, nil, &Verifier{Host: "films.example.org"}, now
		},
	}
	for name, make := range cases {
		req, b, v, at := make()
		if _, err := v.Check(req, b, at); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNoncesForgotten(t *testing.T) {
	var n Nonces
	if !n.add("k", "a", now, true) || n.add("k", "a", now, true) {
		t.Fatal("a nonce must be accepted once")
	}
	if !n.add("other", "a", now, true) {
		t.Fatal("another key's nonce refused")
	}
	n.add("k", "b", now.Add(2*MaxSkew+2*time.Minute), true)
	if _, kept := n.seen["k"]["a"]; kept {
		t.Fatal("an old nonce was kept")
	}
}

// A request signed with a key this server doesn't hear (a stranger's, made
// up) is refused without its nonce being remembered: nobody fills the
// memory that way. The memory has a cap all the same.
func TestStrangersFillNoMemory(t *testing.T) {
	known := NewKey()
	v := &Verifier{Host: "films.example.org", Admit: func(_ *http.Request, key ed25519.PublicKey) (bool, bool) {
		k := SameKey(key, known.Public())
		return k, k
	}}
	for range 50 {
		req, _ := request(NewKey(), "POST", "/api/v1/federation/hello", nil, now)
		if _, err := v.Check(req, nil, now); err == nil {
			t.Fatal("a stranger's request was heard")
		}
	}
	if v.Seen != nil && len(v.Seen.seen) != 0 {
		t.Fatalf("%d nonces remembered for strangers", len(v.Seen.seen))
	}
	req, _ := request(known, "POST", "/api/v1/federation/hello", nil, now)
	if _, err := v.Check(req, nil, now); err != nil || len(v.Seen.seen[string(known.Public())]) != 1 {
		t.Fatalf("a known server refused: %v", err)
	}
	var n Nonces
	// One server asking far past its room (its refused requests count).
	for i := range 120_000 {
		n.add("k", fmt.Sprint(i), now, true)
	}
	if n.add("k", "one more", now, true) {
		t.Fatal("past the cap, a nonce was remembered")
	}
	// Another paired server isn't crowded out by the first.
	if !n.add("other", "one more", now, true) {
		t.Fatal("one server's nonces filled another's room")
	}
	if !n.add("k", "later", now.Add(2*MaxSkew+2*time.Minute), true) {
		t.Fatal("room didn't come back once the old ones went")
	}
}

func TestSignedResponse(t *testing.T) {
	k := NewKey()
	body := []byte(`{"ok":true}`)
	sig := SignResponse(k, "nonce1", 200, "application/json", body)
	if err := CheckResponse(k.Public(), "nonce1", 200, "application/json", body, sig); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"other nonce":      CheckResponse(k.Public(), "nonce2", 200, "application/json", body, sig),
		"other status":     CheckResponse(k.Public(), "nonce1", 500, "application/json", body, sig),
		"other media type": CheckResponse(k.Public(), "nonce1", 200, "text/html", body, sig),
		"no media type":    CheckResponse(k.Public(), "nonce1", 200, "", body, sig),
		"other body":       CheckResponse(k.Public(), "nonce1", 200, "application/json", []byte(`{}`), sig),
		"other key":        CheckResponse(NewKey().Public(), "nonce1", 200, "application/json", body, sig),
		"no signature":     CheckResponse(k.Public(), "nonce1", 200, "application/json", body, ""),
	} {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPairingProof(t *testing.T) {
	secret := bytes.Repeat([]byte{1}, SecretSize)
	b := NewKey()
	proof := PairingProof(secret, b.Public(), "https://b.example.org")
	if !CheckPairingProof(secret, b.Public(), "https://b.example.org", proof) {
		t.Fatal("valid proof refused")
	}
	if CheckPairingProof(secret, NewKey().Public(), "https://b.example.org", proof) {
		t.Fatal("a proof was reused for another key")
	}
	if CheckPairingProof(secret, b.Public(), "https://c.example.org", proof) {
		t.Fatal("a proof was reused for another address")
	}
	if CheckPairingProof(bytes.Repeat([]byte{2}, SecretSize), b.Public(), "https://b.example.org", proof) {
		t.Fatal("a proof passed with another secret")
	}
	if CheckPairingProof(secret, b.Public(), "https://b.example.org", "!!") {
		t.Fatal("garbage accepted")
	}
}

func FuzzParseCode(f *testing.F) {
	good, _ := NewCode("https://films.example.org", NewKey().Public(), now)
	f.Add(good.String())
	f.Add("msfp-pair-1.")
	f.Fuzz(func(t *testing.T, text string) {
		c, err := ParseCode(text)
		if err != nil {
			return
		}
		if CheckAddress(c.Address) != nil || len(c.Key) != 32 || len(c.Secret) != SecretSize {
			t.Fatalf("accepted a bad code: %+v", c)
		}
	})
}

// Strangers heard while a code is live fill a small memory of their own:
// a paired server is still heard when it's full.
func TestStrangersOwnMemory(t *testing.T) {
	n := &Nonces{}
	now := time.Now()
	for i := range maxStrangerNonces {
		n.add("k", fmt.Sprint("s", i), now, false)
	}
	if n.add("k", "one more stranger", now, false) {
		t.Error("strangers past their cap")
	}
	if !n.add("p", "a paired server", now, true) {
		t.Error("a paired server refused")
	}
	if n.add("p", "a paired server", now, false) {
		t.Error("a nonce twice")
	}
}

// The same signed request arriving many times at once (a replay racing the
// original): exactly one is accepted.
func TestReplayAtOnce(t *testing.T) {
	k := NewKey()
	v := &Verifier{Host: "films.example.org", Seen: &Nonces{}}
	body := []byte(`{"a":1}`)
	req, _ := request(k, "POST", "/api/v1/federation/play", body, now)
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := v.Check(req.Clone(context.Background()), body, now); err == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := accepted.Load(); n != 1 {
		t.Errorf("%d accepted", n)
	}
}

// A server can't act in another's name: a request it signed, claiming the
// other's key, is refused; the same nonce under each one's own key is two
// requests.
func TestAnotherServersName(t *testing.T) {
	a, b := NewKey(), NewKey()
	v := &Verifier{Host: "films.example.org", Seen: &Nonces{}}
	body := []byte(`{}`)
	req, _ := request(a, "POST", "/p", body, now)
	req.Header.Set(HeaderKey, b64.EncodeToString(b.Public()))
	if _, err := v.Check(req, body, now); err == nil {
		t.Error("a request signed by one server, in another's name, accepted")
	}
	ra, nonce := request(a, "POST", "/p", body, now)
	rb, _ := request(b, "POST", "/p", body, now)
	rb.Header.Set(HeaderNonce, nonce)
	msg := requestMessage("POST", "films.example.org", "/p", rb.Header.Get(HeaderDate), nonce, body)
	rb.Header.Set(HeaderSignature, b64.EncodeToString(b.sign(msg)))
	if _, err := v.Check(ra, body, now); err != nil {
		t.Errorf("a's request: %v", err)
	}
	if _, err := v.Check(rb, body, now); err != nil {
		t.Errorf("b's request with the same nonce: %v", err)
	}
}

// A pairing ended between two requests of the same server: the first
// passes, the next is answered as from a server no longer known.
func TestUnpairedMidway(t *testing.T) {
	k := NewKey()
	var paired atomic.Bool
	paired.Store(true)
	v := &Verifier{Host: "films.example.org", Seen: &Nonces{},
		Admit:     func(*http.Request, ed25519.PublicKey) (bool, bool) { return paired.Load(), paired.Load() },
		Forgotten: func(*http.Request, ed25519.PublicKey) bool { return !paired.Load() }}
	body := []byte(`{}`)
	first, _ := request(k, "POST", "/p", body, now)
	if _, err := v.Check(first, body, now); err != nil {
		t.Fatalf("while paired: %v", err)
	}
	paired.Store(false)
	next, _ := request(k, "POST", "/p", body, now)
	if _, err := v.Check(next, body, now); !errors.Is(err, ErrForgotten) {
		t.Errorf("after the pairing ended: %v", err)
	}
	// Its replay of the first, now: refused outright.
	if _, err := v.Check(first, body, now); err == nil || errors.Is(err, ErrForgotten) {
		t.Errorf("a replay after the pairing ended: %v", err)
	}
}

// Nonces are kept in memory only: after a restart, a request signed before
// it is refused, since the last process may have seen it (one read on an
// http pairing, replayed). Requests signed after the start pass, pairing
// included, right away.
func TestReplayAfterRestart(t *testing.T) {
	k := NewKey()
	body := []byte(`{"a":1}`)
	old, _ := request(k, "POST", "/api/v1/federation/play", body, now.Add(-time.Second))
	before := &Verifier{Host: "films.example.org", Seen: NewNonces(now.Add(-time.Hour))}
	if _, err := before.Check(old.Clone(context.Background()), body, now.Add(-time.Second)); err != nil {
		t.Fatalf("before the restart: %v", err)
	}
	// Started half a second into a second: a request dated that second may
	// have been signed before the start.
	started := now.Add(500 * time.Millisecond)
	after := &Verifier{Host: "films.example.org", Seen: NewNonces(started)}
	if _, err := after.Check(old.Clone(context.Background()), body, started.Add(time.Second)); err != ErrSignature {
		t.Errorf("replayed after a restart: %v", err)
	}
	sameSecond, _ := request(k, "POST", "/api/v1/federation/play", body, started)
	if _, err := after.Check(sameSecond, body, started); err != ErrSignature {
		t.Errorf("dated the second the memory began: %v", err)
	}
	fresh, _ := request(k, "POST", "/api/v1/federation/pair", body, now.Add(time.Second))
	if _, err := after.Check(fresh, body, now.Add(time.Second)); err != nil {
		t.Errorf("signed after the restart: %v", err)
	}
	// A memory that began on a whole second takes that second's requests.
	exact := &Verifier{Host: "films.example.org", Seen: NewNonces(now)}
	onTime, _ := request(k, "POST", "/api/v1/federation/play", body, now)
	if _, err := exact.Check(onTime, body, now); err != nil {
		t.Errorf("signed the second the memory began, on it: %v", err)
	}
}

// Every body is an object: "null", a list or a string isn't.
func TestDecodeJSONObjectsOnly(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `"x"`, `1`, `true`} {
		var v struct{}
		if DecodeJSON([]byte(body), &v) == nil {
			t.Errorf("%s accepted", body)
		}
	}
	var v struct{}
	if DecodeJSON([]byte(`{}`), &v) != nil {
		t.Error("{} refused")
	}
}

// A call with a query is refused like a bad signature, even signed over
// it (MSFP section 6.4, step 1).
func TestCheckRefusesQueries(t *testing.T) {
	k := NewKey()
	v := &Verifier{Host: "films.example.org"}
	for _, target := range []string{"/api/v1/federation/hello?x=1", "/api/v1/federation/hello?"} {
		req, _ := request(k, "POST", target, []byte(`{}`), now)
		if _, err := v.Check(req, []byte(`{}`), now); err == nil {
			t.Errorf("%s accepted", target)
		}
	}
	req, _ := request(k, "POST", "/api/v1/federation/hello", []byte(`{}`), now)
	if _, err := v.Check(req, []byte(`{}`), now); err != nil {
		t.Errorf("without a query: %v", err)
	}
}

// An answer's Content-Type (core section 6.3): one or none; doubled or
// unreadable fails like a bad signature; an empty one counts as none.
func TestAnswerMediaType(t *testing.T) {
	for _, c := range []struct {
		values []string
		want   string // "" for refused
	}{
		{nil, "-"},
		{[]string{""}, "-"},
		{[]string{"  "}, "-"},
		{[]string{"application/json"}, "application/json"},
		{[]string{"Application/JSON ; charset=utf-8"}, "application/json"},
		{[]string{"application/problem+json"}, "application/problem+json"},
		{[]string{"application/json", "application/json"}, ""},
		{[]string{"application/json", ""}, ""},
		{[]string{"application/json, text/html"}, ""},
		{[]string{"application"}, ""},
		{[]string{"/json"}, ""},
		{[]string{"application/json/x"}, ""},
		{[]string{"; charset=utf-8"}, ""},
		{[]string{"application/js on"}, ""},
		{[]string{"appl\u00e9cation/json"}, ""},
	} {
		h := http.Header{}
		if c.values != nil {
			h["Content-Type"] = c.values
		}
		got, err := AnswerMediaType(h)
		if c.want == "" {
			if !errors.Is(err, ErrSignature) {
				t.Errorf("%q: %q, %v; want refused", c.values, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: %q, %v; want %q", c.values, got, err, c.want)
		}
	}
}
