package admin

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/delivery"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/store"
)

type fakePlaying struct {
	live    []playback.Live
	stopped []string
	ended   []string // accountID/sessionID
}

func (f *fakePlaying) EndPlaybacksOf(accountID, sessionID string) {
	f.ended = append(f.ended, accountID+"/"+sessionID)
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
	peer := msfp.PeerRecord{ID: "pb", Key: bytes.Repeat([]byte{7}, 32), Address: "https://b.example.org", Name: "Films de B", State: msfp.Paired}
	putPeer(t, db, peer)
	src := jellyfin.MediaSource{Bitrate: 8_000_000, RunTimeTicks: 60 * 60 * 10_000_000,
		MediaStreams: []jellyfin.MediaStream{{Type: "Video", Height: 1080}}}
	f := &fakePlaying{live: []playback.Live{
		{Stream: &playback.Stream{ID: "play_one", AccountID: acct.ID, ProfileID: owner.ID, Device: "Firefox on Linux",
			Decision: playback.Decision{Method: "transcode", Source: src}},
			Title: "Free Guy (2021)", Method: "transcode", Transcodes: true, Started: time.Now(), PositionMs: 61_000},
		// The executor's, its audio alone converted: a stream, no transcode slot.
		{Stream: &playback.Stream{ID: "play_four", AccountID: acct.ID, ProfileID: owner.ID, Reason: "audio-codec",
			Delivery: &delivery.Started{Plan: &delivery.Plan{Kind: delivery.KindTranscode, AudioConverted: true,
				Copy: store.IndexedCopy{Facts: wire.Copy{DurationMs: 1000, Size: 1000, Video: &wire.VideoStream{Height: 2160}}}}}},
			Method: "transcode", Started: time.Now()},
		// An account deleted meanwhile still shows.
		{Stream: &playback.Stream{ID: "play_two", AccountID: "acct_gone"}, Method: "direct", Started: time.Now()},
		// A paired server's viewer: the server, not a deleted account.
		{Stream: &playback.Stream{ID: "play_three", AccountID: playback.PeerPrefix + peer.ID, ProfileID: playback.PeerPrefix + peer.ID},
			Title: "Heat (1995)", Method: "direct", Started: time.Now()},
	}}
	p.Playing = f
	b.h = p.Handler()

	rec := b.do("GET", "/admin/api/playing", "", nil)
	body := rec.Body.String()
	for _, want := range []string{`"name":"Main"`, `"profile":"Main"`, `"title":"Free Guy (2021)"`, `"method":"transcode"`,
		`"height":1080`, `"bitrateKbps":8000`, `"positionMs":61000`, `"durationMs":3600000`, `"transcodes":1`,
		`"maxTranscodes":2`, `"delivery":"transcode","converts":["audio"],"reason":"audio-codec"`, `"height":2160`, `"name":"Deleted account"`, `"name":"Films de B (paired server)"`, `"title":"Heat (1995)"`} {
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
	// Already ended: said in JSON, which the panel reads to tell it from
	// a failure.
	if rec := b.do("POST", "/admin/api/playing/play_one/stop", `{}`, nil); rec.Code != 404 ||
		!strings.Contains(rec.Body.String(), `"detail":"That playback has already ended"`) {
		t.Errorf("stopped again: %d %s", rec.Code, rec.Body)
	}
	// Signed out, nothing.
	anon := &browser{h: b.h, cookies: map[string]string{}}
	if rec := anon.do("GET", "/admin/api/playing", "", nil); rec.Code == 200 {
		t.Errorf("signed out: %d", rec.Code)
	}
}
