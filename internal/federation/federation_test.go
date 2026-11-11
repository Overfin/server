package federation

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	good, _ := NewCode("http://10.1.2.3:8080", k.Public(), now)
	for _, text := range []string{"", "overfin-pair-1.", "overfin-pair-2." + strings.TrimPrefix(good.String(), codePrefix),
		"overfin-pair-1.!!!!", good.String()[:40], "overfin-pair-1." + strings.Repeat("A", 5000)} {
		if _, err := ParseCode(text); err == nil {
			t.Errorf("code %q accepted", text)
		}
	}
	if _, err := ParseCode(good.String()); err != nil {
		t.Errorf("plain http on a private address refused: %v", err)
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
	for _, ok := range []string{"https://films.example.org", "http://192.0.2.10:8096", "https://[2001:db8::1]:8443"} {
		if checkAddress(ok) != nil {
			t.Errorf("%s refused", ok)
		}
	}
	for _, bad := range []string{"https://evil;script-src", "https://a,b", "https://ex.com'", "https://Films.example.org",
		"https://films.example.org:443", "http://films.example.org:80", "https://films.example.org/path", "ftp://films.example.org"} {
		if checkAddress(bad) == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if got := Origin("HTTPS://Films.Example.org:443/"); got != "https://films.example.org" {
		t.Errorf("origin: %q", got)
	}
}

// Only TMDB's own way of writing an ID is kept from a peer's index: no
// duplicates made of leading zeros, nothing that isn't a number.
func TestCanonicalTMDB(t *testing.T) {
	for id, want := range map[string]bool{"550": true, "0550": false, "00550": false, "abc": false, "0": false, "": false, "99999999999": false} {
		if canonicalTMDB(id) != want {
			t.Errorf("%q: %v", id, !want)
		}
	}
}

func TestSignedRequest(t *testing.T) {
	k := NewKey()
	v := &Verifier{Host: "films.example.org"}
	body := []byte(`{"a":1}`)
	req, nonce := request(k, "POST", "/api/v1/federation/ping?x=1", body, now)
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
	if !n.add("a", now, true) || n.add("a", now, true) {
		t.Fatal("a nonce must be accepted once")
	}
	n.add("b", now.Add(2*MaxSkew+2*time.Minute), true)
	if _, kept := n.seen["a"]; kept {
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
	if _, err := v.Check(req, nil, now); err != nil || len(v.Seen.seen) != 1 {
		t.Fatalf("a known server refused: %v", err)
	}
	var n Nonces
	for i := range maxNonces {
		n.add(fmt.Sprint(i), now, true)
	}
	if n.add("one more", now, true) {
		t.Fatal("past the cap, a nonce was remembered")
	}
	if !n.add("later", now.Add(2*MaxSkew+2*time.Minute), true) {
		t.Fatal("room didn't come back once the old ones went")
	}
}

func TestSignedResponse(t *testing.T) {
	k := NewKey()
	body := []byte(`{"ok":true}`)
	sig := SignResponse(k, "nonce1", 200, body)
	if err := CheckResponse(k.Public(), "nonce1", 200, body, sig); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"other nonce":  CheckResponse(k.Public(), "nonce2", 200, body, sig),
		"other status": CheckResponse(k.Public(), "nonce1", 500, body, sig),
		"other body":   CheckResponse(k.Public(), "nonce1", 200, []byte(`{}`), sig),
		"other key":    CheckResponse(NewKey().Public(), "nonce1", 200, body, sig),
		"no signature": CheckResponse(k.Public(), "nonce1", 200, body, ""),
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
	f.Add("overfin-pair-1.")
	f.Fuzz(func(t *testing.T, text string) {
		c, err := ParseCode(text)
		if err != nil {
			return
		}
		if checkAddress(c.Address) != nil || len(c.Key) != 32 || len(c.Secret) != SecretSize {
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
		n.add(fmt.Sprint("s", i), now, false)
	}
	if n.add("one more stranger", now, false) {
		t.Error("strangers past their cap")
	}
	if !n.add("a paired server", now, true) {
		t.Error("a paired server refused")
	}
	if n.add("a paired server", now, false) {
		t.Error("a nonce twice")
	}
}
