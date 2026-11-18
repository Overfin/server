package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/overfin/server/internal/federation"
	"github.com/overfin/server/internal/store"
)

// peerIDsSession is what these tests read of a play answer.
type peerIDsSession struct {
	ID        string `json:"id"`
	VersionID string `json:"versionId"`
	Versions  []struct {
		ID string `json:"id"`
	} `json:"versions"`
	AudioTrack    string `json:"audioTrack"`
	SubtitleTrack string `json:"subtitleTrack"`
}

// peerPlayer is the test server, B, holding Heat in its films, offered to
// a paired server, A; play is A asking B to play Heat, with extra members
// in its request, then stopping it.
type peerPlayer struct {
	srv  *httptest.Server
	fake *fakeJellyfin
	play func(extra map[string]any) (peerIDsSession, int)
}

func newPeerPlayer(t *testing.T) peerPlayer {
	t.Helper()
	ctx := context.Background()
	var st *store.Store
	var b *federation.Service
	srv, fake := newServerWith(t, options{store: &st, federation: &b})
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: "films", AudioTags: []string{"fr"}},
	}, 1)
	st.RefreshTitleKeys(ctx)
	st.SetLibraries(ctx, []store.Library{{ID: "films", Item: []byte(`{"Name":"Films"}`)}})
	a, bOnA, _ := viewer(t, b, srv.Client())
	play := func(extra map[string]any) (peerIDsSession, int) {
		t.Helper()
		req := map[string]any{"title": "movie/tmdb/949", "capabilities": map[string]any{}}
		for k, v := range extra {
			req[k] = v
		}
		var s peerIDsSession
		status, err := a.Call(ctx, bOnA, "/api/v1/federation/play", req, &s)
		if err != nil {
			t.Fatal(err)
		}
		if status == 200 {
			a.Call(ctx, bOnA, "/api/v1/federation/stop", map[string]any{"id": s.ID}, &struct{}{})
		}
		return s, status
	}
	return peerPlayer{srv: srv, fake: fake, play: play}
}

// A play answer to a paired server names versions by IDs of its own,
// never Jellyfin's media source IDs (MSFP section 9.5), the same from one
// playback to the next; this server's own site keeps Jellyfin's.
func TestPeerVersionIDsOpaque(t *testing.T) {
	x := newPeerPlayer(t)
	first, status := x.play(nil)
	jellyfins := fmt.Sprintf("%032x", 1)
	opaque := regexp.MustCompile(`^ver_[A-Za-z0-9_-]{22}$`)
	if status != 200 || !opaque.MatchString(first.VersionID) || strings.Contains(first.VersionID, jellyfins) ||
		len(first.Versions) != 1 || first.Versions[0].ID != first.VersionID {
		t.Fatalf("versions to a peer: %d %+v", status, first)
	}
	if again, _ := x.play(nil); again.VersionID != first.VersionID {
		t.Fatalf("not stable: %s, then %s", first.VersionID, again.VersionID)
	}
	code, body := post(t, x.srv, "/api/v1/playback", `{"itemId":"item_`+jellyfins+`","capabilities":{}}`)
	var own peerIDsSession
	json.Unmarshal([]byte(body), &own)
	if code != 201 || own.VersionID != "ver_"+jellyfins {
		t.Fatalf("the site's own: %d %s", code, body)
	}
}

// A paired server's audioTrack or subtitleTrack that isn't one of the
// copy's counts as none asked for (MSFP playback, section 2.6): Jellyfin
// never hears of it, and the answer says what plays. One of the copy's
// plays; a malformed one is refused.
func TestPeerTracksOfTheCopy(t *testing.T) {
	x := newPeerPlayer(t)
	s, status := x.play(map[string]any{"audioTrack": "a9", "subtitleTrack": "s9"})
	if status != 200 || s.AudioTrack != "a1" || s.SubtitleTrack != "" {
		t.Fatalf("tracks the copy doesn't have: %d %+v", status, s)
	}
	if x.fake.called("PlaybackInfo audio=9") != 0 {
		t.Fatal("Jellyfin heard of a9")
	}
	s, status = x.play(map[string]any{"audioTrack": "a2", "subtitleTrack": "s3"})
	if status != 200 || s.AudioTrack != "a2" || s.SubtitleTrack != "s3" || x.fake.called("PlaybackInfo audio=2") == 0 {
		t.Fatalf("the copy's tracks: %d %+v", status, s)
	}
	// The forced track, as subtitles; off.
	if s, _ = x.play(map[string]any{"subtitleTrack": "s5"}); s.SubtitleTrack != "s5" {
		t.Fatalf("forced: %+v", s)
	}
	if s, _ = x.play(map[string]any{"subtitleTrack": "off", "subtitleLanguage": "en"}); s.SubtitleTrack != "" {
		t.Fatalf("off: %+v", s)
	}
	if _, status = x.play(map[string]any{"audioTrack": "s1"}); status != 400 {
		t.Fatalf("malformed: %d", status)
	}
}

// A paired server's capabilities in another case are unknown values
// (MSFP playback, section 2.1): "H264" lets nothing play as it is. This
// server's own site is read leniently.
func TestPeerCapabilitiesExactCase(t *testing.T) {
	x := newPeerPlayer(t)
	direct := func() string {
		t.Helper()
		x.fake.mu.Lock()
		defer x.fake.mu.Unlock()
		b, _ := json.Marshal(x.fake.lastAsked["DeviceProfile"].(map[string]any)["DirectPlayProfiles"])
		return string(b)
	}
	caps := func(container, codec string) map[string]any {
		return map[string]any{"capabilities": map[string]any{"containers": []string{container}, "videoCodecs": []string{codec}}}
	}
	if _, status := x.play(caps("MKV", "H264")); status != 200 || strings.Contains(strings.ToLower(direct()), "h264") {
		t.Fatalf("another case: %d %s", status, direct())
	}
	if _, status := x.play(caps("mkv", "h264")); status != 200 || !strings.Contains(direct(), `"VideoCodec":"h264"`) {
		t.Fatalf("exact: %d %s", status, direct())
	}
	code, _ := post(t, x.srv, "/api/v1/playback", `{"itemId":"item_`+fmt.Sprintf("%032x", 1)+`","capabilities":{"containers":["MKV"],"videoCodecs":["H264"]}}`)
	if code != 201 || !strings.Contains(direct(), `"VideoCodec":"h264"`) {
		t.Fatalf("the site's own: %d %s", code, direct())
	}
}

// play takes a film's or an episode's key in MSFP's one form only (video
// section 4): another form of the same film, a key of another source, a
// season, is refused (playback section 2.2, step 1). A versionId from a
// former answer is taken; one that is none of the copies' is ignored.
func TestPeerPlayKeysAndVersions(t *testing.T) {
	x := newPeerPlayer(t)
	for _, key := range []string{"movie/tmdb/0949", "movie/imdb/tt0113277", "series/tmdb/1399/s1", "series/tmdb/1399", "movie/tmdb/949/"} {
		if _, status := x.play(map[string]any{"title": key}); status != 400 {
			t.Errorf("%s: %d", key, status)
		}
	}
	first, status := x.play(nil)
	if status != 200 {
		t.Fatal(status)
	}
	if s, status := x.play(map[string]any{"versionId": first.VersionID}); status != 200 || s.VersionID != first.VersionID {
		t.Errorf("its version: %d %+v", status, s)
	}
	if s, status := x.play(map[string]any{"versionId": "ver_other"}); status != 200 || s.VersionID != first.VersionID {
		t.Errorf("another version: %d %+v", status, s)
	}
}
