package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/store"
)

type fakePlaying struct {
	live    []playback.Live
	stopped []string
}

func (f *fakePlaying) NowPlaying() ([]playback.Live, int) { return f.live, 2 }

func (f *fakePlaying) EndPlayback(id string) bool {
	for i, l := range f.live {
		if l.Stream.ID == id {
			f.live = append(f.live[:i], f.live[i+1:]...)
			f.stopped = append(f.stopped, id)
			return true
		}
	}
	return false
}

// The live view: who's watching what and how, and stopping it.
func TestNowPlaying(t *testing.T) {
	ctx := context.Background()
	p, b, db, _ := signedInPanel(t)
	acct, owner, _ := db.CreateAccount(ctx, "person-1", store.NewOwner{Name: "Main", JellyfinUserID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", JellyfinUsername: "of-1", JellyfinToken: "t"})
	src := jellyfin.MediaSource{Bitrate: 8_000_000, RunTimeTicks: 60 * 60 * 10_000_000,
		MediaStreams: []jellyfin.MediaStream{{Type: "Video", Height: 1080}}}
	f := &fakePlaying{live: []playback.Live{
		{Stream: &playback.Stream{ID: "play_one", AccountID: acct.ID, ProfileID: owner.ID, Device: "Firefox on Linux",
			Decision: playback.Decision{Method: "transcode", Source: src}},
			Title: "Free Guy (2021)", Method: "transcode", Started: time.Now(), PositionMs: 61_000},
		// An account deleted meanwhile still shows.
		{Stream: &playback.Stream{ID: "play_two", AccountID: "acct_gone"}, Method: "direct", Started: time.Now()},
	}}
	p.Playing = f
	b.h = p.Handler()

	rec := b.do("GET", "/admin/api/playing", "", nil)
	body := rec.Body.String()
	for _, want := range []string{`"name":"Main"`, `"profile":"Main"`, `"title":"Free Guy (2021)"`, `"method":"transcode"`,
		`"height":1080`, `"bitrateKbps":8000`, `"positionMs":61000`, `"durationMs":3600000`, `"transcodes":1`,
		`"maxTranscodes":2`, `"name":"Deleted account"`} {
		if !strings.Contains(body, want) {
			t.Errorf("lacks %s: %s", want, body)
		}
	}
	if strings.Contains(body, "jellyfinToken") || strings.Contains(body, `"t"`) {
		t.Errorf("a token: %s", body)
	}

	if rec := b.do("POST", "/admin/api/playing/play_one/stop", `{}`, nil); rec.Code != 204 || len(f.stopped) != 1 {
		t.Errorf("stop: %d %v", rec.Code, f.stopped)
	}
	if rec := b.do("POST", "/admin/api/playing/play_one/stop", `{}`, nil); rec.Code != 404 {
		t.Errorf("stopped again: %d", rec.Code)
	}
	// Signed out, nothing.
	anon := &browser{h: b.h, cookies: map[string]string{}}
	if rec := anon.do("GET", "/admin/api/playing", "", nil); rec.Code == 200 {
		t.Errorf("signed out: %d", rec.Code)
	}
}
