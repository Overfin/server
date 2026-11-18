package msfp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/overfin/server/msfp"
	"github.com/overfin/server/msfp/store/memory"
)

// A is hosted under a path, and serves its discovery document: B finds
// its calls there when it redeems A's code, and A finds B's when B's
// address is stored; each calls the other at its base.
func TestDiscoveryUsed(t *testing.T) {
	ctx := context.Background()
	clock := newClock(1800000000)
	a := newServer(t, msfp.Config{Clock: clock, Prefix: "/srv/a", Discovery: true})
	b := newServer(t, msfp.Config{Clock: clock, Prefix: "/srv/b", Discovery: true})
	bOnA, aOnB := pair(t, a, b)
	waitAll(a, b)
	if p := peerRecord(t, b.store, aOnB.ID); p.Base != "/srv/a/api/v1/federation/" {
		t.Errorf("A's base on B: %q", p.Base)
	}
	if p := peerRecord(t, a.store, bOnA.ID); p.Base != "/srv/b/api/v1/federation/" {
		t.Errorf("B's base on A: %q", p.Base)
	}
	if _, err := b.eng.Check(ctx, aOnB.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.eng.Check(ctx, bOnA.ID); err != nil {
		t.Fatal(err)
	}
	for _, x := range a.exchanges("hello") {
		if !strings.HasPrefix(x.path, "/srv/a/api/v1/federation/") {
			t.Errorf("a call to A at %s", x.path)
		}
	}

	// Its document, as served; off, the path is as any that doesn't exist.
	res, err := a.srv.Client().Get(a.srv.URL + "/.well-known/msfp")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.NewDecoder(res.Body).Decode(&doc)
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/json" || doc["base"] != "/srv/a/api/v1/federation/" {
		t.Errorf("A's document: %d %v", res.StatusCode, doc)
	}
	a.eng.SetDiscovery(false)
	if res, _ := a.srv.Client().Get(a.srv.URL + "/.well-known/msfp"); res.StatusCode != 404 {
		t.Errorf("off: %d", res.StatusCode)
	}
	if base := b.eng.FetchBaseForTest(ctx, a.srv.URL); base != "" {
		t.Errorf("off: %q", base)
	}
	if sv, _ := a.eng.Server(ctx); sv.Discovery {
		t.Errorf("A: %+v", sv)
	}

	// A moves under another path: B's calls at the stored base fail; after
	// 3 of them it fetches A's document again, and calls it there.
	a.reopen(t, msfp.Config{Clock: clock, Store: a.store, Prefix: "/srv/c", Discovery: true})
	for range 3 {
		if _, err := b.eng.Check(ctx, aOnB.ID); !errors.Is(err, msfp.ErrUnreachable) {
			t.Fatalf("B to A at its old base: %v", err)
		}
	}
	waitAll(a, b)
	if p := peerRecord(t, b.store, aOnB.ID); p.Base != "/srv/c/api/v1/federation/" {
		t.Errorf("A's base on B: %q", p.Base)
	}
	if _, err := b.eng.Check(ctx, aOnB.ID); err != nil {
		t.Errorf("B to A at its new base: %v", err)
	}
}

// What a fetch of a discovery document takes: a valid one, of every
// base strict.json lists; anything else is the default base, and a
// redirect isn't followed.
func TestDiscoveryFetched(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var status int
	var contentType, body string
	var asked []string
	fake := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, r.Method+" "+r.URL.Path)
		if r.Header.Get("MSFP-Signature") != "" || r.Header.Get("MSFP-Key") != "" {
			w.WriteHeader(400)
			return
		}
		if status == http.StatusFound {
			w.Header().Set("Location", "/elsewhere")
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer fake.Close()
	b := newServer(t, msfp.Config{})
	serve := func(s int, ct, b string) {
		mu.Lock()
		status, contentType, body, asked = s, ct, b, nil
		mu.Unlock()
	}

	var cases struct {
		Bases []struct {
			Text   string
			Valid  bool
			Reason string
		}
	}
	raw, err := os.ReadFile("wire/testdata/v1/strict.json")
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(raw, &cases)
	if len(cases.Bases) == 0 {
		t.Fatal("no bases")
	}
	for _, c := range cases.Bases {
		doc, _ := json.Marshal(map[string]any{"core": []int{1}, "base": c.Text})
		serve(200, "application/json", string(doc))
		want := ""
		if c.Valid && c.Text != "/api/v1/federation/" {
			want = c.Text
		}
		if got := b.eng.FetchBaseForTest(ctx, fake.URL); got != want {
			t.Errorf("%q (%s): %q", c.Text, c.Reason, got)
		}
	}

	good := `{"core":[1],"base":"/x/api/v1/federation/"}`
	serve(200, "application/json; charset=utf-8", good)
	if got := b.eng.FetchBaseForTest(ctx, fake.URL); got != "/x/api/v1/federation/" {
		t.Errorf("good: %q", got)
	}
	for name, c := range map[string]struct {
		status   int
		ct, body string
	}{
		"404":             {404, "application/json", good},
		"redirect":        {302, "application/json", good},
		"not JSON":        {200, "application/json", "base: /x/api/v1/federation/"},
		"text":            {200, "text/plain", good},
		"an array":        {200, "application/json", `["/x/api/v1/federation/"]`},
		"core not array":  {200, "application/json", `{"core":1,"base":"/x/api/v1/federation/"}`},
		"base not string": {200, "application/json", `{"base":["/x/api/v1/federation/"]}`},
		"over 16 KiB":     {200, "application/json", `{"base":"/x/api/v1/federation/"` + strings.Repeat(" ", 16<<10) + `}`},
		"null base":       {200, "application/json", `{"base":null}`},
	} {
		serve(c.status, c.ct, c.body)
		if got := b.eng.FetchBaseForTest(ctx, fake.URL); got != "" {
			t.Errorf("%s: %q", name, got)
		}
		mu.Lock()
		if len(asked) != 1 || asked[0] != "GET /.well-known/msfp" {
			t.Errorf("%s: asked %v", name, asked)
		}
		mu.Unlock()
	}
	// Nothing is fetched of a server that can't be called.
	if got := b.eng.FetchBaseForTest(ctx, "https://msfp-4dq6fy7e4xtop2hj5lv6z3po54.invalid"); got != "" {
		t.Errorf(".invalid: %q", got)
	}
}

// A prefix not in MSFP's form is refused at Open.
func TestBadPrefix(t *testing.T) {
	for _, prefix := range []string{"srv", "/srv/", "/srv/../x", "/srv%2Fx", "/a b"} {
		_, err := msfp.Open(context.Background(), msfp.Config{Address: "https://a.example.org", Prefix: prefix, Store: memory.New()})
		if !errors.Is(err, msfp.ErrInvalidAddress) {
			t.Errorf("%q: %v", prefix, err)
		}
	}
}
