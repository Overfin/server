package federation

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/overfin/server/internal/store"
)

// The json and integers cases of MSFP's strict vectors
// (testdata/v0-strict.json), through DecodeJSON: valid ones decode,
// invalid ones are refused.
func TestStrictJSONVectors(t *testing.T) {
	b, err := os.ReadFile("testdata/v0-strict.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		JSON []struct {
			Name, Text, Hex string
			Valid           bool
		}
		Integers []struct {
			Text  string
			Valid bool
		}
	}
	if err := json.Unmarshal(b, &v); err != nil || len(v.JSON) == 0 || len(v.Integers) == 0 {
		t.Fatal("no cases", err)
	}
	for _, c := range v.JSON {
		body := []byte(c.Text)
		if c.Hex != "" {
			body, _ = hex.DecodeString(c.Hex)
		}
		var msg helloMessage
		if err := DecodeJSON(body, &msg); (err == nil) != c.Valid {
			t.Errorf("json %s: %v", c.Name, err)
		}
		var any map[string]any
		if err := DecodeJSON(body, &any); (err == nil) != c.Valid {
			t.Errorf("json %s, into a map: %v", c.Name, err)
		}
	}
	for _, c := range v.Integers {
		var n struct {
			N int64 `json:"n"`
		}
		if err := DecodeJSON([]byte(`{"n":`+c.Text+`}`), &n); (err == nil) != c.Valid {
			t.Errorf("integer %s: %v", c.Text, err)
		}
	}
}

// Names match exactly: "State" is an unknown member, ignored; null is a
// member left out; each check reaches into lists and nested objects.
func TestDecodeJSONExact(t *testing.T) {
	var msg helloMessage
	if err := DecodeJSON([]byte(`{"State":"ended","certificates":null}`), &msg); err != nil || msg.State != "" || msg.Certificates != nil {
		t.Errorf("another case: %+v %v", msg, err)
	}
	var a titlesAnswer
	err := DecodeJSON([]byte(`{"titles":[{"key":"movie/tmdb/1","KIND":"movie","kind":"movie","Library":"x","library":"l"}],"next":null,"Seq":3}`), &a)
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
		if err := DecodeJSON([]byte(bad), &x); err == nil {
			t.Errorf("accepted: %s", bad)
		}
	}
	// Escapes that are fine: a pair, a backslash before a u.
	var m map[string]string
	if err := DecodeJSON([]byte(`{"a":"🎬","b":"\\ud800"}`), &m); err != nil || m["a"] != "🎬" || m["b"] != `\ud800` {
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
	// Two members of the same name: refused too.
	status, err = b.s.Call(ctx, aOnB, helloPath, json.RawMessage(`{"state":"paired","state":"ended"}`), &out)
	if err != nil || status != http.StatusNotFound {
		t.Fatalf("hello with state twice: %d, %v", status, err)
	}
	onlyPeer(t, a)
}
