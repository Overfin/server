package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/overfin/server/internal/store"
)

// A stream reserved for each profile, with a limit of 3 and two profiles:
// one of them watches in 2 places while the other keeps its own, and
// nobody's playback stops for another profile's.
func TestReservedStreams(t *testing.T) {
	var tmpl string
	var st *store.Store
	srv, _ := newServerWith(t, options{maxStreams: 3, template: &tmpl, store: &st})
	ctx := context.Background()

	limits := func() (out struct {
		MaxProfiles, MaxStreams int
		ReservedStreams         bool
		SharedStreams           int
	}) {
		t.Helper()
		var me struct{ Limits json.RawMessage }
		if code := get(t, srv, "/api/v1/me", &me); code != 200 {
			t.Fatalf("me: %d", code)
		}
		json.Unmarshal(me.Limits, &out)
		return out
	}
	// Off by default: every stream is shared.
	if l := limits(); l.ReservedStreams || l.SharedStreams != 3 {
		t.Errorf("off: %+v", l)
	}
	if err := st.UpdateTemplate(ctx, tmpl, "Test", 3, 3, true); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, srv, "/api/v1/profiles", `{"name":"Partner"}`)
	if code != 201 {
		t.Fatalf("partner: %d %s", code, body)
	}
	var partner struct{ ID string }
	json.Unmarshal([]byte(body), &partner)
	if l := limits(); !l.ReservedStreams || l.SharedStreams != 1 || l.MaxStreams != 3 {
		t.Errorf("on, two profiles: %+v", l)
	}

	// The owner: its own stream, then the shared one; a third place
	// replaces its own oldest, the partner's stream staying free.
	first := startPlayback(t, srv, "Laptop")
	second := startPlayback(t, srv, "Phone")
	third := startPlayback(t, srv, "TV")
	if code, _ := getAs(t, srv, subject, "/api/v1/playback/"+first); code != http.StatusGone {
		t.Errorf("the owner's oldest, replaced: %d", code)
	}

	// The partner gets its own, though the account has 2 playing.
	if code, body := post(t, srv, "/api/v1/profiles/"+partner.ID+"/activate", `{}`); code != 200 {
		t.Fatalf("activate: %d %s", code, body)
	}
	mine := startPlayback(t, srv, "Tablet")
	// A second place for the partner: the shared one is the owner's, so
	// it replaces its own, and the owner's keep playing.
	startPlayback(t, srv, "Bedroom")
	if code, _ := getAs(t, srv, subject, "/api/v1/playback/"+mine); code != http.StatusGone {
		t.Errorf("the partner's own, replaced: %d", code)
	}
	post(t, srv, "/api/v1/profiles/"+ownerID(t, srv)+"/activate", `{}`)
	for _, id := range []string{second, third} {
		if code, _ := getAs(t, srv, subject, "/api/v1/playback/"+id); code != 200 {
			t.Errorf("the owner's %s after the partner's: %d", id, code)
		}
	}
}

// While each profile has a stream reserved, an account gets no more
// profiles than streams, whatever its profile limit says.
func TestReservedStreamsLimitProfiles(t *testing.T) {
	var tmpl string
	var st *store.Store
	srv, _ := newServerWith(t, options{maxStreams: 2, template: &tmpl, store: &st})
	// The control panel refuses such a template; here it's made in the
	// database, as an older one could be.
	if err := st.UpdateTemplate(context.Background(), tmpl, "Test", 2, 3, true); err != nil {
		t.Fatal(err)
	}
	if code, body := post(t, srv, "/api/v1/profiles", `{"name":"Partner"}`); code != 201 {
		t.Fatalf("a second: %d %s", code, body)
	}
	code, body := post(t, srv, "/api/v1/profiles", `{"name":"Kid","kind":"kids","maxAge":0}`)
	if code != http.StatusConflict || !strings.Contains(body, "profile-limit-reached") {
		t.Errorf("a third, over 2 streams: %d %s", code, body)
	}
	var me struct {
		Limits struct{ MaxProfiles, SharedStreams int }
	}
	get(t, srv, "/api/v1/me", &me)
	if me.Limits.MaxProfiles != 2 || me.Limits.SharedStreams != 0 {
		t.Errorf("limits: %+v", me.Limits)
	}
}
