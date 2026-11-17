package federation

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/overfin/server/internal/msfp"
	"github.com/overfin/server/internal/outbound"
)

// A route that crashes frees its slots, and its refusal is signed: an
// unsigned 500 is no answer to a paired server.
func TestSignRefusalsCrash(t *testing.T) {
	freed := false
	h := SignRefusals(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := w.(*refusalWriter)
		rw.free = func() { freed = true }
		rw.sign = func(status int, body []byte) string { return "signed" }
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/federation/hello", nil))
	if !freed || rec.Code != 500 || rec.Header().Get(msfp.HeaderSignature) != "signed" {
		t.Fatalf("freed %v, %d, signature %q", freed, rec.Code, rec.Header().Get(msfp.HeaderSignature))
	}
}

// A peer's name, cleaned in core section 9.1's order.
func TestCleanNameOrder(t *testing.T) {
	for in, want := range map[string]string{
		"a\tb\u0085c":                    "a b c",
		"  Films\u200b de B \n":          "Films de B",
		strings.Repeat("a", 79) + " bcd": strings.Repeat("a", 79),
		"  ":                             "films.example.org",
		"x\u0000y":                       "xy",
	} {
		if got := cleanName(in, "https://films.example.org"); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// titles without its required libraries is refused, as a body that isn't
// one.
func TestTitlesWithoutLibraries(t *testing.T) {
	_, b, _, aOnB := pairedPair(t)
	var answer titlesAnswer
	r, err := b.s.call(t.Context(), aOnB, "/api/v1/federation/titles", map[string]any{"after": ""}, &answer)
	if err != nil || r.Status != http.StatusNotFound {
		t.Fatalf("%+v %v", r, err)
	}
	if r, err := b.s.call(t.Context(), aOnB, "/api/v1/federation/titles", map[string]any{"libraries": []string{}}, &answer); err != nil || r.Status != 200 {
		t.Fatalf("an empty list: %+v %v", r, err)
	}
}

// The side that ends a pairing remembers the other's key; the side told
// "ended" forgets the ender without remembering it (MSFP section 7.4,
// step 3): its next hello is a stranger's, not "unknown", signed.
func TestEndedForgottenOnTheEndingSideOnly(t *testing.T) {
	ctx := t.Context()
	a, b, bOnA, aOnB := pairedPair(t)
	if err := a.s.End(ctx, bOnA.ID); err != nil {
		t.Fatal(err)
	}
	if forgotten, _ := a.s.Store.PeerForgotten(ctx, bOnA.PublicKey); !forgotten {
		t.Error("A, which ended it, doesn't remember B")
	}
	if _, err := b.s.Store.PeerByID(ctx, aOnB.ID); err == nil {
		t.Fatal("B still has A")
	}
	if forgotten, _ := b.s.Store.PeerForgotten(ctx, aOnB.PublicKey); forgotten {
		t.Error("B, told it ended, remembers A as forgotten")
	}
}

// No content coding (MSFP section 3): peers are never asked for one, and
// an answer sent coded is refused like a badly signed one; "identity" is
// none.
func TestNoContentCoding(t *testing.T) {
	if !outbound.Transport().DisableCompression {
		t.Error("the transport asks for gzip")
	}
	_, b, _, aOnB := pairedPair(t)
	k := msfp.NewKey()
	coding := ""
	var asked []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Header.Get("Accept-Encoding"))
		body := []byte(`{"state":"paired"}`)
		if coding == "gzip" {
			var z bytes.Buffer
			zw := gzip.NewWriter(&z)
			_, _ = zw.Write(body)
			_ = zw.Close()
			body = z.Bytes()
		}
		if coding != "" {
			w.Header().Set("Content-Encoding", coding)
		}
		w.Header().Set(msfp.HeaderSignature, msfp.SignResponse(k, r.Header.Get(msfp.HeaderNonce), 200, body))
		_, _ = w.Write(body)
	}))
	defer fake.Close()
	b.s.HTTP = NewHTTPClient()
	peer := *aOnB
	peer.Address, peer.PublicKey = fake.URL, k.Public()
	var answer helloMessage
	for c, ok := range map[string]bool{"": true, "identity": true, "gzip": false, "br": false} {
		coding = c
		_, err := b.s.call(t.Context(), &peer, helloPath, helloMessage{State: "paired"}, &answer)
		if (err == nil) != ok {
			t.Errorf("Content-Encoding %q: %v", c, err)
		}
	}
	for _, a := range asked {
		if a != "" {
			t.Errorf("asked with Accept-Encoding %q", a)
		}
	}
}
