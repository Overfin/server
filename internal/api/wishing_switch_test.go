package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/overfin/server/internal/store"
)

// The owner's switch on a profile's wishing: on by default, turned off and
// on again; only the owner reads or sets it; an unknown profile, or a
// body without "on", refused.
func TestWishingSwitch(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st, wishes: true})
	code, body := post(t, srv, "/api/v1/profiles", `{"name":"Partner"}`)
	var partner struct{ ID string }
	json.Unmarshal([]byte(body), &partner)
	if code != http.StatusCreated {
		t.Fatalf("partner: %d %s", code, body)
	}
	path := "/api/v1/profiles/" + partner.ID + "/wishing"
	on := func() bool {
		t.Helper()
		code, body := getAs(t, srv, subject, path)
		var out struct{ On bool }
		json.Unmarshal([]byte(body), &out)
		if code != 200 {
			t.Fatalf("wishing: %d %s", code, body)
		}
		return out.On
	}
	if !on() {
		t.Error("off by default")
	}
	if code, body := put(t, srv, "PUT", path, `{"on":false}`); code != 200 {
		t.Fatalf("off: %d %s", code, body)
	}
	if on() {
		t.Error("still on")
	}
	put(t, srv, "PUT", path, `{"on":true}`)
	if !on() {
		t.Error("not on again")
	}
	for _, body := range []string{`{}`, `{"on":"yes"}`, `not json`} {
		if code, _ := put(t, srv, "PUT", path, body); code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, code)
		}
	}
	if code, _ := getAs(t, srv, subject, "/api/v1/profiles/prof_nothing/wishing"); code != http.StatusNotFound {
		t.Errorf("an unknown profile: %d", code)
	}
	// As the partner: not the owner's switch to read or turn.
	if code, body := post(t, srv, "/api/v1/profiles/"+partner.ID+"/activate", `{}`); code != 200 {
		t.Fatalf("activate: %d %s", code, body)
	}
	if code, _ := getAs(t, srv, subject, path); code != http.StatusForbidden {
		t.Errorf("the partner reading it: %d", code)
	}
	if code, _ := put(t, srv, "PUT", path, `{"on":false}`); code != http.StatusForbidden {
		t.Errorf("the partner turning it: %d", code)
	}
}
