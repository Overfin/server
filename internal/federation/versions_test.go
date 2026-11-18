package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/overfin/server/internal/msfp"
	"github.com/overfin/server/internal/store"
)

// Once paired, each side knows from the other's hello which parts of MSFP
// they share (core section 10): here, all that Overfin speaks.
func TestPairedKnowVersions(t *testing.T) {
	_, b, bOnA, aOnB := pairedPair(t)
	a := bOnA
	for _, part := range []string{msfp.PartCatalog, msfp.PartPlayback, msfp.PartMedia, msfp.PartVideo} {
		if !b.s.Speaks(aOnB, part) {
			t.Errorf("B doesn't speak %s with A", part)
		}
	}
	if a.Versions == "" || aOnB.Versions == "" {
		t.Fatalf("versions not kept: %q, %q", a.Versions, aOnB.Versions)
	}
	if b.s.Speaks(aOnB, msfp.PartMusic) {
		t.Error("music, which Overfin doesn't list, is on")
	}
}

// A peer that lists catalog only (core section 17.1's B): its libraries
// are listed, without limits; no titles; play is answered not-supported,
// signed, and the caller makes no more such calls.
func TestCatalogOnly(t *testing.T) {
	ctx := context.Background()
	a, b, bOnA, aOnB := pairedPair(t)
	if err := a.s.Store.SetPeerVersions(ctx, bOnA.ID, `{"core":[1],"parts":{"catalog":[1]}}`); err != nil {
		t.Fatal(err)
	}
	var libs struct {
		Libraries []json.RawMessage `json:"libraries"`
		Limits    json.RawMessage   `json:"limits"`
	}
	if r, err := b.s.call(ctx, aOnB, librariesPath, struct{}{}, &libs); err != nil || r.Status != http.StatusOK || len(libs.Libraries) != 1 || libs.Limits != nil {
		t.Fatalf("libraries: %+v, %v, %+v", r, err, libs)
	}
	var titles titlesReading
	if r, err := b.s.call(ctx, aOnB, titlesPath, titlesRequest{Libraries: pub(t, a, "films")}, &titles); err != nil || r.Status != http.StatusOK || len(titles.Titles) != 0 || titles.Seq != 0 {
		t.Fatalf("titles: %+v, %v, %+v", r, err, titles)
	}
	// play, as the route that serves it reads it (PeerRequest): refused
	// not-supported, signed.
	body := []byte(`{"title":"movie/tmdb/949"}`)
	req := httptest.NewRequest("POST", a.url+playPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	nonce := msfp.SignRequest(req, body, b.s.Key, b.s.now())
	rec := httptest.NewRecorder()
	SignRefusals(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, _, ok := a.s.PeerRequest(w, r); ok {
			t.Error("play passed with playback off")
		}
	})).ServeHTTP(rec, req)
	if rec.Code != 404 || !bytes.Contains(rec.Body.Bytes(), []byte(`"not-supported"`)) ||
		msfp.CheckResponse(a.s.Key.Public(), nonce, 404, rec.Header().Get("Content-Type"), rec.Body.Bytes(), rec.Header().Get(msfp.HeaderSignature)) != nil {
		t.Fatalf("play: %d %s, signed %q", rec.Code, rec.Body, rec.Header().Get(msfp.HeaderSignature))
	}
	// The caller, told so, makes no more calls of that part until the
	// peer's next hello.
	// (Asked again just now: not in the background, which the test would
	// race with.)
	b.s.rechecked = map[string]time.Time{aOnB.ID: b.s.now()}
	b.s.refusedPart(aOnB, msfp.PartPlayback)
	if _, err := b.s.call(ctx, aOnB, playPath, map[string]string{"title": "movie/tmdb/949"}, &struct{}{}); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("play after not-supported: %v", err)
	}
	// Not "gone": a part off isn't the pairing.
	if got, _ := b.s.Store.PeerByID(ctx, aOnB.ID); got.State != store.PeerPaired {
		t.Errorf("A on B: %s", got.State)
	}
}

// Before any hello, no part is on: nothing is called.
func TestNoPartsBeforeHello(t *testing.T) {
	_, b, _, aOnB := pairedPair(t)
	p := *aOnB
	p.Versions = ""
	if _, err := b.s.TheirLibraries(context.Background(), &p); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("libraries before any hello: %v", err)
	}
	if !SpeaksCore(&p) {
		t.Error("no common core before any hello")
	}
	p.Versions = `{"core":[0],"parts":{"catalog":[0],"playback":[0],"media":[0]}}`
	if SpeaksCore(&p) {
		t.Error("a core in common with a server of version 0 only")
	}
}

// A signed 200 whose media type isn't application/json is refused, as a
// badly signed one (core section 6.3), and so is one whose Content-Type
// changed on the way.
func TestAnswerMediaType(t *testing.T) {
	_, b, _, aOnB := pairedPair(t)
	k := msfp.NewKey()
	served, signed := "", ""
	fake := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := []byte(`{"state":"paired","core":[1]}`)
		w.Header().Set("Content-Type", served)
		w.Header().Set(msfp.HeaderSignature, msfp.SignResponse(k, r.Header.Get(msfp.HeaderNonce), 200, signed, body))
		_, _ = w.Write(body)
	}))
	defer fake.Close()
	peer := *aOnB
	peer.Address, peer.PublicKey = fake.URL, k.Public()
	for _, c := range []struct {
		served, signed string
		ok             bool
	}{
		{"application/json", "application/json", true},
		{"application/json; charset=utf-8", "application/json", true},
		{"text/plain", "text/plain", false},
		{"text/plain", "application/json", false},
	} {
		served, signed = c.served, c.signed
		var out json.RawMessage
		if _, err := b.s.call(context.Background(), &peer, helloPath, helloMessage{State: "paired"}, &out); (err == nil) != c.ok {
			t.Errorf("served %q, signed %q: %v", c.served, c.signed, err)
		}
	}
}

// MSFP is HTTPS only: a code for a plain http address is refused, said as
// such, unless the test switch is on.
func TestPlainHTTPCode(t *testing.T) {
	ctx := context.Background()
	b := newServer(t, "B")
	plain := newServerOn(t, "A", false) // its address is http
	msfp.AllowPlainHTTPForTests(true)
	code, _, err := plain.s.NewCode(ctx, "")
	msfp.AllowPlainHTTPForTests(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.s.UseCode(ctx, code); !errors.Is(err, msfp.ErrPlainHTTP) {
		t.Fatalf("a plain http code: %v", err)
	}
	if _, _, err := plain.s.NewCode(ctx, ""); !errors.Is(err, msfp.ErrPlainHTTP) {
		t.Fatalf("a plain http code made: %v", err)
	}
}
