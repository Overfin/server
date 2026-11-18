package federation

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/msfp/wire"
)

// Names match exactly: "State" is an unknown member, ignored; null is
// refused in a request, and at the top of an answer; each check reaches
// into lists and nested objects.
func TestDecodeJSONExact(t *testing.T) {
	var msg helloMessage
	if err := wire.DecodeJSON([]byte(`{"State":"ended"}`), &msg); err != nil || msg.State != "" {
		t.Errorf("another case: %+v %v", msg, err)
	}
	if err := wire.DecodeJSON([]byte(`{"state":"paired","certificates":null}`), &msg); err == nil {
		t.Error("null accepted in a request")
	}
	var a titlesAnswer
	if err := wire.DecodeAnswer([]byte(`{"titles":[],"next":null}`), &a); err == nil {
		t.Error("null accepted at the top of an answer")
	}
	err := wire.DecodeAnswer([]byte(`{"titles":[{"key":"movie/tmdb/1","KIND":"movie","kind":"movie","Library":"x","library":"l","audio":null}],"Seq":3}`), &a)
	if err != nil || len(a.Titles) != 1 || a.Titles[0].Kind != "movie" || a.Titles[0].Library != "l" || a.Seq != 0 {
		t.Errorf("nested: %+v %v", a, err)
	}
	for _, bad := range []string{
		`{"titles":[{"key":"a","key":"b"}]}`, // twice, inside a list
		`{"seq":1.5}`, `{"seq":1e2}`, `{"seq":-0}`, `{"seq":9007199254740992}`,
		`{"titles":[]} {}`, `{"state":"\udfff"}`, `{"state":"a","state":"b"}`,
		"{\"state\":\"\xed\xa0\x80\"}", // a surrogate written in UTF-8
		``, `[`, `{"a":` + strings.Repeat("[", 100) + strings.Repeat("]", 100) + `}`,
	} {
		var x titlesAnswer
		if err := wire.DecodeJSON([]byte(bad), &x); err == nil {
			t.Errorf("accepted: %s", bad)
		}
	}
	// Escapes that are fine: a pair, a backslash before a u.
	var m map[string]string
	if err := wire.DecodeJSON([]byte(`{"a":"🎬","b":"\\ud800"}`), &m); err != nil || m["a"] != "🎬" || m["b"] != `\ud800` {
		t.Errorf("good escapes: %v %v", m, err)
	}
}

// hasNull says whether a JSON body holds null anywhere.
func hasNull(t *testing.T, b []byte) bool {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch v := v.(type) {
		case nil:
			return true
		case map[string]any:
			for _, x := range v {
				if walk(x) {
					return true
				}
			}
		case []any:
			for _, x := range v {
				if walk(x) {
					return true
				}
			}
		}
		return false
	}
	return walk(v)
}

// What Overfin sends peers holds no null, even empty (MSFP section 2).
func TestNoNullSent(t *testing.T) {
	for _, v := range []any{helloMessage{}, pairRequest{}, pairAnswer{}, librariesAnswer{Libraries: []store.PeerLibrary{}},
		titlesRequest{Libraries: []string{}}, store.PeerTitle{}, struct{}{}} {
		b, _ := json.Marshal(v)
		if hasNull(t, b) {
			t.Errorf("%T sends null: %s", v, b)
		}
	}
}

// A hello whose state is in another case has no state: refused, signed,
// and the pairing stays (encoding/json alone read it as "ended").
func TestHelloStateExactCase(t *testing.T) {
	ctx := context.Background()
	a, b := newServer(t, "A"), newServer(t, "B")
	code, _, _ := a.s.NewCode(ctx, "")
	aOnB, err := b.s.UseCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.s.Accept(ctx, onlyPeer(t, a).ID); err != nil {
		t.Fatal(err)
	}
	var out helloMessage
	status, err := b.s.Call(ctx, aOnB, helloPath, map[string]string{"State": "ended"}, &out)
	if err != nil || status != http.StatusNotFound {
		t.Fatalf("hello with State: %d, %v", status, err)
	}
	if got := onlyPeer(t, a); got.State != store.PeerPaired {
		t.Fatalf("A after it: %+v", got)
	}
	// Two members of the same name: refused too (sent as they are: this
	// server never writes them).
	if status := rawCall(t, b, aOnB, helloPath, `{"state":"paired","state":"ended"}`); status != http.StatusNotFound {
		t.Fatalf("hello with state twice: %d", status)
	}
	onlyPeer(t, a)
}

// rawCall sends p, from s, a signed request of exactly body, and gives the
// status of its answer.
func rawCall(t *testing.T, s *server, p *store.Peer, path, body string) int {
	t.Helper()
	req, err := http.NewRequest("POST", p.Address+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	wire.SignRequest(req, []byte(body), s.s.Keys.Current(), s.s.now())
	res, err := s.s.conns(p, true).client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}
