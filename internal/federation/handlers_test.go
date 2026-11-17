package federation

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	if !freed || rec.Code != 500 || rec.Header().Get(HeaderSignature) != "signed" {
		t.Fatalf("freed %v, %d, signature %q", freed, rec.Code, rec.Header().Get(HeaderSignature))
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

// A peer's name, cleaned in core section 9.1's order.
func TestCleanNameOrder(t *testing.T) {
	for in, want := range map[string]string{
		"a\tb\u0085c":                    "a b c",
		"  Films\u200b de B \n":               "Films de B",
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
