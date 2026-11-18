package msfp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/overfin/server/msfp"
)

// Playback both ways: play, the choice of the copy and the tracks, the
// answer as the viewer's side keeps it, the media relayed, progress and
// stop, the limits counted in one step, and every way a playback ends.

const master = "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-STREAM-INF:BANDWIDTH=5000000\nv0.m3u8\n"
const media = "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:6.0,\nseg0.m4s\n#EXT-X-ENDLIST\n"

func filmHost() *fakeHost {
	h := newFakeHost()
	h.film("films", "movie/tmdb/949", "en", "fr")
	h.files = map[string][]byte{"master.m3u8": []byte(master), "v0.m3u8": []byte(media), "init.mp4": []byte("init"),
		"seg0.m4s": []byte("0123456789abcdefghij"), "subtitles/s0/0": []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<i>Bonjour</i>\n")}
	return h
}

func TestPlayAndRelay(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949", AudioLanguage: "fr",
		Capabilities: msfp.Capabilities{Containers: []string{"MP4"}, MaxHeight: 1000, MaxBitrateKbps: 12345}, SubtitleLanguage: "fr"})
	if err != nil {
		t.Fatal(err)
	}
	if pb.Method != "direct" || pb.Protocol != "hls" || pb.AudioTrack != "a0" || pb.SubtitleTrack != "s0" ||
		len(pb.Versions) != 2 || pb.VersionID != pb.Versions[1].ID || len(pb.SubtitleTracks) != 2 || pb.SubtitleStretchSeconds != 120 ||
		strings.HasPrefix(pb.ID, "play_") {
		t.Errorf("the playback as B keeps it: %+v", pb)
	}
	// A heard the French copy, the coarsened capabilities, the cap.
	if len(h.started) != 1 || h.started[0].Copy != "movie/tmdb/949#1" || h.started[0].MaxBitrateKbps != 12000 ||
		len(h.started[0].Capabilities.Containers) != 1 || h.started[0].Capabilities.MaxHeight != 720 {
		t.Errorf("A started %+v", h.started)
	}
	plays := s.a.exchanges("play")
	if body := plays[0].reqBody; !strings.Contains(body, `"containers":["mp4"]`) || !strings.Contains(body, `"maxBitrateKbps":12000`) {
		t.Errorf("the play request: %s", body)
	}
	if strings.Contains(plays[0].resBody, "movie/tmdb/949#") {
		t.Errorf("a handle left in the answer: %s", plays[0].resBody)
	}
	// The media, through B.
	var out sink
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaMaster}, "", &out); err != nil || string(out.body) != master ||
		out.contentType != "application/vnd.apple.mpegurl" {
		t.Errorf("master: %v %q %q", err, out.body, out.contentType)
	}
	out = sink{}
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaSegment, Name: "v0.m3u8"}, "", &out); err != nil ||
		string(out.body) != media {
		t.Errorf("the media playlist, by a name the master named: %v %q", err, out.body)
	}
	out = sink{}
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaSegment, Name: "seg0.m4s"}, "bytes=2-5", &out); err != nil ||
		out.status != 206 || string(out.body) != "2345" || out.contentRange != "bytes 2-5/20" || out.contentType != "video/mp4" {
		t.Errorf("a range: %v %+v", err, out)
	}
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaSegment, Name: "seg0.m4s"}, "bytes=0-1,4-5", &sink{}); !errors.Is(err, msfp.ErrRangeNotSatisfiable) {
		t.Errorf("several ranges: %v", err)
	}
	out = sink{}
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaSubtitles, Track: "s0", Part: 0}, "", &out); err != nil ||
		string(out.body) != "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nBonjour\n" || out.contentType != "text/vtt; charset=utf-8" {
		t.Errorf("subtitles: %v %q", err, out.body)
	}
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaSubtitles, Track: "s1", Part: 0}, "", &sink{}); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("a PGS track asked as text: %v", err)
	}
	// Progress and stop.
	s.b.eng.Progress(pb.ID, 60_000, true)
	waitAll(s.b)
	if p := s.a.exchanges("progress"); len(p) != 1 || p[0].status != 200 || !strings.Contains(p[0].reqBody, `"paused":true`) {
		t.Errorf("progress: %+v", p)
	}
	if v := s.a.eng.Playbacks(); len(v) != 1 || v[0].State != "active" || v[0].Title != "movie/tmdb/949" || v[0].PeerName == "" {
		t.Errorf("A's live view: %+v", v)
	}
	if err := s.b.eng.Stop(ctx, pb.ID, 61_000); err != nil {
		t.Fatal(err)
	}
	waitAll(s.a)
	if st := h.stops(); st["h1"] != msfp.EndStopped {
		t.Errorf("A's host was told %v", st)
	}
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaMaster}, "", &sink{}); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("a fetch after the stop: %v", err)
	}
	if _, ok := has[msfp.PlaybackEnded](s.a.events); !ok {
		t.Error("no PlaybackEnded")
	}
}

// play's malformed requests are refused with 400, a key not offered with
// 404, nothing at all for a pairing with no streams, and a ticket works
// only as MSFP says.
func TestPlayRefusalsAndTickets(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	call := func(body string) int {
		var raw json.RawMessage
		code, _ := s.b.eng.CallForTest(ctx, s.onB.ID, "play", json.RawMessage(body), &raw)
		return code
	}
	for _, body := range []string{`{}`, `{"title":7}`, `{"title":"series/tmdb/1399"}`, `{"title":"movie/tmdb/0949"}`,
		`{"title":"movie/tmdb/949","startPositionMs":-1}`, `{"title":"movie/tmdb/949","audioTrack":"a01"}`,
		`{"title":"movie/tmdb/949","subtitleTrack":"s007"}`, `{"title":"movie/tmdb/949","capabilities":{"containers":["mp4",1]}}`,
		`{"title":"movie/tmdb/949","capabilities":{"containers":"mp4"}}`, `{"title":"movie/tmdb/949","capabilities":"mp4"}`,
		`{"title":"movie/tmdb/949","audioLanguage":"fr_CA"}`, `{"title":"movie/tmdb/949","maxBitrateKbps":10000001}`} {
		if code := call(body); code != 400 {
			t.Errorf("%s: %d", body, code)
		}
	}
	if code := call(`{"title":"movie/tmdb/9999999999"}`); code != 404 {
		t.Errorf("a title not offered: %d", code)
	}
	// Unknown values and members ignored; a track the copy hasn't, as absent.
	if code := call(`{"title":"movie/tmdb/949","capabilities":{"containers":["MP4","x"]},"audioTrack":"a7","other":1}`); code != 200 {
		t.Errorf("unknown values: %d", code)
	}
	if got := h.started[0]; len(got.Capabilities.Containers) != 0 || got.AudioTrack != "a0" {
		t.Errorf("A started %+v", got)
	}
	var ans struct {
		Stream struct{ URL string }
	}
	json.Unmarshal([]byte(s.a.exchanges("play")[len(s.a.exchanges("play"))-1].resBody), &ans)
	if !strings.HasPrefix(ans.Stream.URL, s.a.srv.URL+"/api/v1/stream/") {
		t.Fatalf("stream URL %q", ans.Stream.URL)
	}
	get := func(url string, hdr ...string) int {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		if len(hdr) == 1 {
			req.Method = hdr[0]
		}
		res, err := s.a.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	base := strings.TrimSuffix(ans.Stream.URL, "master.m3u8")
	if code := get(ans.Stream.URL); code != 200 {
		t.Errorf("master: %d", code)
	}
	cases := []struct {
		url  string
		hdr  []string
		want int
	}{
		{ans.Stream.URL + "?x=1", nil, 404}, {ans.Stream.URL, []string{"Origin", "https://example.org"}, 404},
		{ans.Stream.URL, []string{"Sec-Fetch-Mode", "cors"}, 404}, {ans.Stream.URL, []string{"Sec-Fetch-Site", "same-origin"}, 404},
		{ans.Stream.URL, []string{"POST"}, 404}, {ans.Stream.URL, []string{"HEAD"}, 404}, {base + "file", nil, 404},
		{base + "%2e%2e/x", nil, 404}, {base + "seg0.m4s", []string{"Range", "bytes=-"}, 416},
		{base + "seg0.m4s", []string{"Range", "bytes=0-1,3-4"}, 416}, {base + "seg0.m4s", []string{"Range", "bytes=0-3"}, 206},
		{base + "seg0.m4s", []string{"Range", "bytes=40-"}, 416}, {base + "v0.m3u8", []string{"Range", "bytes=0-3"}, 200},
		{base + "trickplay/0", nil, 404}, {base + "subtitles/s0/01", nil, 404}, {base + "subtitles/s0/0", nil, 200},
		{base + "pictures/s0/0", nil, 404},
		{s.a.srv.URL + "/api/v1/stream/" + strings.Repeat("A", 43) + "/master.m3u8", nil, 404},
	}
	for _, c := range cases {
		if got := get(c.url, c.hdr...); got != c.want {
			t.Errorf("%s %v: %d, want %d", strings.TrimPrefix(c.url, base), c.hdr, got, c.want)
		}
	}
	// No stream at all for this pairing: refused before anything else.
	if err := s.a.eng.SetLimits(ctx, s.onA.ID, msfp.Limits{}); err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	if code, _ := s.b.eng.CallForTest(ctx, s.onB.ID, "play", json.RawMessage(`{"title":"movie/tmdb/949"}`), &raw); code != 409 {
		t.Errorf("no streams: %d", code)
	}
}

// Two plays that arrive together never both take the last stream; a
// transcode past the pairing's transcodes is 503.
func TestLimitsAtOnce(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	s := newSharing(t, h, msfp.Config{NewPairing: &msfp.Limits{Streams: 1, Transcodes: 0}}, msfp.Config{})
	h.hold = make(chan struct{})
	var wg sync.WaitGroup
	var ok, limited atomic.Int32
	for range 2 {
		wg.Go(func() {
			_, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, msfp.ErrStreamLimitReached):
				limited.Add(1)
			default:
				t.Error(err)
			}
		})
	}
	time.Sleep(200 * time.Millisecond)
	close(h.hold)
	wg.Wait()
	if ok.Load() != 1 || limited.Load() != 1 {
		t.Errorf("%d played, %d refused", ok.Load(), limited.Load())
	}
	for _, v := range s.a.eng.Playbacks() {
		s.a.eng.StopPlayback(v.ID)
	}
	h.mu.Lock()
	h.hold, h.method = nil, "transcode"
	h.mu.Unlock()
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); !errors.Is(err, msfp.ErrServerBusy) {
		t.Errorf("a transcode past the pairing's: %v", err)
	}
	// A host that doesn't call Admit: counted after, refused the same.
	h.mu.Lock()
	h.method, h.noAdmit = "direct", true
	h.mu.Unlock()
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); err != nil {
		t.Errorf("without Admit: %v", err)
	}
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); !errors.Is(err, msfp.ErrStreamLimitReached) {
		t.Errorf("without Admit, over the limit: %v", err)
	}
	waitAll(s.a)
	if len(h.stops()) == 0 {
		t.Error("the playback refused after it started wasn't stopped")
	}
}

// Every ending of MSFP playback section 6.3, each telling both sides.
func TestPlaybackEndings(t *testing.T) {
	ctx := context.Background()
	clockA := newClock(time.Now().Unix())
	h := filmHost()
	s := newSharing(t, h, msfp.Config{Clock: clockA}, msfp.Config{Clock: clockA})
	play := func() msfp.RemotePlayback {
		t.Helper()
		pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
		if err != nil {
			t.Fatal(err)
		}
		return pb
	}
	reason := func() msfp.EndReason {
		t.Helper()
		waitAll(s.a)
		ev, ok := has[msfp.PlaybackEnded](s.a.events)
		if !ok {
			t.Fatal("no PlaybackEnded")
		}
		return ev.Reason
	}
	// Expiry: two minutes without use.
	pb := play()
	clockA.Add(119 * time.Second)
	s.a.eng.RunOnce(ctx)
	if len(s.a.eng.Playbacks()) != 1 {
		t.Fatal("expired too soon")
	}
	clockA.Add(2 * time.Second)
	s.a.eng.RunOnce(ctx)
	if r := reason(); r != msfp.EndExpired || len(s.a.eng.Playbacks()) != 0 {
		t.Errorf("after 2 minutes: %v", r)
	}
	// The viewer's side learns at its next progress: 410.
	s.b.eng.Progress(pb.ID, 1000, true)
	waitAll(s.b)
	if ev, ok := has[msfp.RemotePlaybackEnded](s.b.events); !ok || ev.Playback != pb.ID {
		t.Errorf("B's playback after the 410: %+v %v", ev, ok)
	}
	// Too long.
	play()
	for range 25 {
		clockA.Add(time.Hour)
		for _, v := range s.a.eng.Playbacks() {
			s.a.eng.TouchForTest(v.ID)
		}
		s.a.eng.RunOnce(ctx)
	}
	if r := reason(); r != msfp.EndTooLong {
		t.Errorf("after 24 hours: %v", r)
	}
	// The library no longer offered.
	pb = play()
	if err := s.a.eng.SetOffers(ctx, s.onA.ID, nil); err != nil {
		t.Fatal(err)
	}
	if r := reason(); r != msfp.EndNotOffered {
		t.Errorf("offers withdrawn: %v", r)
	}
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaMaster}, "", &sink{}); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("a fetch after the offers ended: %v", err)
	}
	// The operator.
	s.a.eng.SetOffers(ctx, s.onA.ID, []string{"films"})
	play()
	s.a.eng.StopPlayback(s.a.eng.Playbacks()[0].ID)
	if r := reason(); r != msfp.EndOperator {
		t.Errorf("the operator: %v", r)
	}
	// The pairing ended, by the viewer's side: both end.
	pb = play()
	if err := s.b.eng.End(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	if r := reason(); r != msfp.EndPairingEnded {
		t.Errorf("the pairing ended: %v", r)
	}
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaMaster}, "", &sink{}); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("a fetch after the pairing ended: %v", err)
	}
	if st := h.stops(); len(st) != 5 {
		t.Errorf("A's host told of %d endings: %v", len(st), st)
	}
}

// After a restart, the playbacks left on peers are stopped.
func TestLeftoversStopped(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); err != nil {
		t.Fatal(err)
	}
	s.b.reopen(t, msfp.Config{Store: s.b.store})
	if err := s.b.eng.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	waitAll(s.a)
	if len(s.a.eng.Playbacks()) != 0 || len(s.a.exchanges("stop")) != 1 {
		t.Errorf("after B's restart: %+v, %d stops", s.a.eng.Playbacks(), len(s.a.exchanges("stop")))
	}
	var left []msfp.Leftover
	s.b.store.Read(ctx, func(tx msfp.Tx) error { left, _ = tx.Leftovers(); return nil })
	if len(left) != 0 {
		t.Errorf("leftovers kept: %v", left)
	}
}

// A peer that says it can't be called is never fetched from: its
// playbacks end, its titles go, and nothing connects to it.
func TestNoMediaFromUncallable(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	var conns atomic.Int32
	cfgA := msfp.Config{Catalog: h, Playback: h, Media: h}
	h.libs = []msfp.Library{{ID: "films", Name: "Films", Kind: "movies"}}
	a := newServerWith(t, cfgA, serverOpts{conn: func() { conns.Add(1) }})
	b := newServer(t, msfp.Config{})
	onA, onB := pair(t, a, b)
	a.eng.Check(ctx, onA.ID)
	b.eng.Check(ctx, onB.ID)
	a.eng.SetOffers(ctx, onA.ID, []string{"films"})
	libs, _ := b.eng.TheirLibraries(ctx, onB.ID)
	b.eng.SetWants(ctx, onB.ID, []string{libs[0].ID})
	b.eng.ReadNow(ctx, onB.ID)
	pb, err := b.eng.Play(ctx, onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
	if err != nil {
		t.Fatal(err)
	}
	// A can't be called now; its scheduled hello tells B.
	if err := a.eng.SetUncallable(ctx, true); err != nil {
		t.Fatal(err)
	}
	a.eng.RunOnce(ctx)
	waitAll(a, b)
	if v, _ := b.eng.Peer(ctx, onB.ID); v.Inbound != msfp.InboundNo {
		t.Fatalf("B sees A as %v", v.Inbound)
	}
	waitAll(b)
	before := conns.Load()
	if err := b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaMaster}, "", &sink{}); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("a fetch from a peer that can't be called: %v", err)
	}
	if _, err := b.eng.Play(ctx, onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); !errors.Is(err, msfp.ErrUncallable) {
		t.Errorf("a play on a peer that can't be called: %v", err)
	}
	if _, err := b.eng.ReadNow(ctx, onB.ID); !errors.Is(err, msfp.ErrUncallable) {
		t.Errorf("a reading of a peer that can't be called: %v", err)
	}
	if b.eng.Playable(onB.ID, "movie") {
		t.Error("its titles playable")
	}
	if n := conns.Load() - before; n != 0 {
		t.Errorf("%d connections to a peer that can't be called", n)
	}
	var titles []msfp.Title
	b.store.Read(ctx, func(tx msfp.Tx) error { titles, _ = tx.PeerTitles(onB.ID, "", 10); return nil })
	if len(titles) != 0 {
		t.Errorf("its titles kept: %d", len(titles))
	}
}

// A music track plays audio only, counted apart when the limits say so.
func TestMusicPlay(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.libs = []msfp.Library{{ID: "music", Name: "Music", Kind: "music"}}
	track := "track/mb/0c000000-0000-4000-8000-000000000003"
	h.titles[track] = &msfp.MusicTitle{TitleCommon: msfp.TitleCommon{Key: track, Kind: "track", Libraries: []string{"music"}}, Name: "x"}
	h.copies[track] = []msfp.Copy{{Handle: "t", Name: "FLAC", Library: "music"}}
	h.info["t"] = msfp.CopyInfo{AudioTracks: []msfp.Track{{ID: "a0", Label: "Audio"}},
		SubtitleTracks: []msfp.Track{{ID: "s0", Label: "x"}}, Trickplay: &msfp.Trickplay{TileWidth: 10, TileHeight: 10, Columns: 1, Rows: 1, IntervalMs: 1000, TileCount: 1}}
	h.protocol = "file"
	s := newSharing(t, h, msfp.Config{NewPairing: &msfp.Limits{Streams: 0, Audio: true, AudioStreams: 1}}, msfp.Config{})
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: track, SubtitleTrack: "s0"})
	if err != nil {
		t.Fatal(err)
	}
	if !h.started[0].AudioOnly || len(pb.SubtitleTracks) != 0 || pb.Trickplay != nil || pb.Protocol != "file" {
		t.Errorf("a track's playback: %+v / %+v", h.started[0], pb)
	}
	if strings.Contains(s.a.exchanges("play")[0].resBody, "subtitle") || strings.Contains(s.a.exchanges("play")[0].reqBody, "subtitle") {
		t.Errorf("subtitles with a track: %s", s.a.exchanges("play")[0].resBody)
	}
	// One audio stream: a second track waits for the first's stop.
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: track}); !errors.Is(err, msfp.ErrStreamLimitReached) {
		t.Errorf("a second track at once: %v", err)
	}
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: track, Replaces: pb.ID}); err != nil {
		t.Errorf("the next track, replacing the first: %v", err)
	}
	// A film counts in streams: 0 here.
	h.film("music", "movie/tmdb/1", "en")
	var raw json.RawMessage
	if code, _ := s.b.eng.CallForTest(ctx, s.onB.ID, "play", json.RawMessage(`{"title":"movie/tmdb/1"}`), &raw); code != 409 {
		t.Errorf("a film with no streams: %d", code)
	}
	if !strings.Contains(s.a.exchanges("libraries")[0].resBody, `"audioStreams":1`) {
		t.Errorf("the limits sent: %s", s.a.exchanges("libraries")[0].resBody)
	}
}

// The host's playlists are checked before they leave: one outside the
// subset is refused, with a warning, not sent.
func TestOwnPlaylistChecked(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	h.files["master.m3u8"] = []byte("#EXTM3U\n#EXT-X-KEY:METHOD=NONE\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv0.m3u8\n")
	h.files["v0.m3u8"] = []byte("#EXTM3U\n#EXT-X-PROGRAM-DATE-TIME:2020\n#EXTINF:6,\nseg0.m4s\n")
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []msfp.MediaPath{{Kind: msfp.MediaMaster}, {Kind: msfp.MediaPlaylist, Name: "v0.m3u8"}} {
		if err := s.b.eng.Fetch(ctx, pb.ID, p, "", &sink{}); !errors.Is(err, msfp.ErrNotFound) {
			t.Errorf("%v: %v", p, err)
		}
	}
}

// progress and stop of a playback not live, or another's; and an open
// response cut when its playback ends, on both sides.
func TestStatesAndCuts(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	call := func(name, body string) int {
		var raw json.RawMessage
		code, _ := s.b.eng.CallForTest(ctx, s.onB.ID, name, json.RawMessage(body), &raw)
		return code
	}
	unknown := `{"id":"play_aaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	for _, c := range []struct {
		call, body string
		want       int
	}{
		{"progress", unknown, 410}, {"stop", unknown, 200}, {"progress", `{"id":"play_x"}`, 400},
		{"progress", `{}`, 400}, {"stop", `{"id":"play_aaaaaaaaaaaaaaaaaaaaaaaaaa","positionMs":-1}`, 400},
		{"progress", `{"id":"play_aaaaaaaaaaaaaaaaaaaaaaaaaa","positionMs":8640000001}`, 400},
	} {
		if got := call(c.call, c.body); got != c.want {
			t.Errorf("%s %s: %d, want %d", c.call, c.body, got, c.want)
		}
	}
	// A stopped playback: progress 410, stop 200 again.
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
	if err != nil {
		t.Fatal(err)
	}
	var theirs struct{ ID string }
	json.Unmarshal([]byte(s.a.exchanges("play")[0].resBody), &theirs)
	if !strings.HasPrefix(theirs.ID, "play_") || len(theirs.ID) != 31 {
		t.Errorf("a playback ID: %q", theirs.ID)
	}
	// A second stop of the same playback is answered 200 too.
	stopped := call("stop", `{"id":"`+theirs.ID+`"}`)
	again := call("stop", `{"id":"`+theirs.ID+`"}`)
	if stopped != 200 || again != 200 || call("progress", `{"id":"`+theirs.ID+`"}`) != 410 {
		t.Error("a stopped playback's calls")
	}
	s.b.eng.Stop(ctx, pb.ID, -1)
	// A response held open: cut when the playback ends.
	h.protocol = "file"
	h.mu.Lock()
	cut := make(chan struct{})
	h.serve = func(ctx context.Context, _ msfp.MediaPath, _ *msfp.Range, out msfp.ResponseSink) error {
		out.Start(200, "video/mp4", -1, "")
		for {
			if _, err := out.Write([]byte("x")); err != nil || ctx.Err() != nil {
				close(cut)
				return nil
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	h.mu.Unlock()
	pb, err = s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949", Capabilities: msfp.Capabilities{Containers: []string{"mp4"}}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaFile}, "", &sink{}) }()
	time.Sleep(100 * time.Millisecond)
	if v := s.a.eng.Playbacks(); len(v) == 1 {
		s.a.eng.StopPlayback(v[0].ID)
	}
	select {
	case <-cut:
	case <-time.After(5 * time.Second):
		t.Fatal("the owning side's response wasn't cut")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay wasn't cut")
	}
}
