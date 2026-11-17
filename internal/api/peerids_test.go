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
