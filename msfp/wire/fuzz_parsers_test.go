package wire

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"filippo.io/edwards25519"
)

type fuzzBody struct {
	State  string            `json:"state"`
	Count  int64             `json:"count"`
	List   []string          `json:"list"`
	Inner  *fuzzBody         `json:"inner"`
	Labels map[string]string `json:"labels"`
	Any    any               `json:"any"`
}

// What DecodeJSON takes is always JSON encoding/json takes too, valid
// UTF-8, without a member twice at any depth; and its tree, written out
// again, reads the same.
func FuzzDecodeJSON(f *testing.F) {
	for _, s := range []string{`{}`, `{"state":"paired","count":3,"list":["a"]}`, `{"a":1,"a":2}`, `{"a":1,"a":2}`,
		`{"State":"x","state":"y"}`, `{"count":1e3}`, `{"count":-0}`, `{"count":9007199254740991}`, `"\ud800"`,
		`{"inner":{"inner":{"labels":{"k":"v","k":"w"}}}}`, "\xef\xbb\xbf{}", `{"any":[1,{"b":null}]}`, `[1,2`, `{"state":null}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		tree, err := strictTree(body, false)
		var v fuzzBody
		derr := DecodeJSON(body, &v)
		if (err == nil) != (derr == nil) && derr == nil {
			t.Fatalf("decoded what strictTree refused: %q", body)
		}
		if err != nil {
			return
		}
		if !json.Valid(body) || !utf8.Valid(body) {
			t.Fatalf("accepted what isn't JSON: %q", body)
		}
		if dupes(t, body) {
			t.Fatalf("accepted a member twice: %q", body)
		}
		again, err := json.Marshal(tree)
		if err != nil {
			t.Fatalf("its tree: %v", err)
		}
		if _, err := strictTree(again, false); err != nil {
			t.Fatalf("its tree written out (%q) refused: %v", again, err)
		}
	})
}

// dupes says whether a JSON text has an object with a member twice,
// read with encoding/json's decoder alone.
func dupes(t *testing.T, body []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(body))
	var walk func() bool
	walk = func() bool {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		d, ok := tok.(json.Delim)
		if !ok {
			return false
		}
		seen := map[string]bool{}
		for dec.More() {
			if d == '{' {
				k, _ := dec.Token()
				if seen[k.(string)] {
					return true
				}
				seen[k.(string)] = true
			}
			if walk() {
				return true
			}
		}
		_, _ = dec.Token()
		return false
	}
	return walk()
}

// eighth is 8⁻¹ mod L: [8][eighth]P is P exactly when P is in the
// prime-order subgroup.
var eighth = func() *edwards25519.Scalar {
	b := make([]byte, 32)
	b[0] = 8
	s, _ := edwards25519.NewScalar().SetCanonicalBytes(b)
	return edwards25519.NewScalar().Invert(s)
}()

// The point checks agree with another way of telling the prime-order
// subgroup, keys made honestly always pass, and what verify accepts
// crypto/ed25519 does too.
func FuzzPrimeOrderPoint(f *testing.F) {
	f.Add(make([]byte, 32), []byte("m"), make([]byte, 64))
	f.Add(bytes.Repeat([]byte{0xff}, 32), []byte{}, bytes.Repeat([]byte{1}, 64))
	id := edwards25519.NewIdentityPoint().Bytes()
	f.Add(id, []byte("x"), append(append([]byte{}, id...), make([]byte, 32)...))
	f.Fuzz(func(t *testing.T, b, msg, sig []byte) {
		got := primeOrderPoint(b)
		want := false
		if p, err := new(edwards25519.Point).SetBytes(b); err == nil && bytes.Equal(p.Bytes(), b) {
			q := new(edwards25519.Point).ScalarMult(eighth, p)
			q.MultByCofactor(q)
			want = q.Equal(p) == 1 && p.Equal(edwards25519.NewIdentityPoint()) == 0
		}
		if got != want {
			t.Fatalf("%x: %v, want %v", b, got, want)
		}
		if len(b) >= ed25519.SeedSize {
			k := ed25519.NewKeyFromSeed(b[:ed25519.SeedSize])
			pub := k.Public().(ed25519.PublicKey)
			if !ValidKey(pub) || !verify(pub, msg, ed25519.Sign(k, msg)) {
				t.Fatalf("an honest key or signature refused: seed %x", b[:32])
			}
		}
		if len(b) == 32 && verify(b, msg, sig) && !ed25519.Verify(b, msg, sig) {
			t.Fatalf("verify accepted what crypto/ed25519 refuses")
		}
	})
}

// An answer's media type (core section 6.3) is "-", or type/subtype in
// lower case, without white space or parameters; and the signature line
// built from it is the same as MediaType makes of the header.
func FuzzAnswerMediaType(f *testing.F) {
	for _, s := range []string{"", "application/json", "Application/JSON; charset=utf-8", "a/b/c", ";", " /x", "text/plain, a/b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, value string) {
		got, err := AnswerMediaType(http.Header{"Content-Type": {value}})
		if err != nil {
			return
		}
		if got != "-" && (strings.ToLower(got) != got || strings.Count(got, "/") != 1 || strings.ContainsAny(got, " \t;,")) {
			t.Fatalf("%q read as %q", value, got)
		}
		if MediaType(value) != got {
			t.Fatalf("%q: %q, but the signature line %q", value, got, MediaType(value))
		}
	})
}
