package msfp_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/overfin/server/msfp"
)

// The relay against a hostile peer: everything a peer sends is checked
// before it reaches the viewer, its URLs, its playlists, subtitles,
// pictures and sheets, its ranges and headers; and the media vectors.

func jpegOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// hostile is a sharing whose owning side, A, rewrites its answers: the
// media of path name (after the ticket), or of a call.
type hostile struct {
	*sharing
	rewrites map[string]func(h http.Header, body []byte) []byte
}

func newHostile(t *testing.T, h *fakeHost) *hostile {
	s := &hostile{sharing: newSharing(t, h, msfp.Config{}, msfp.Config{}), rewrites: map[string]func(http.Header, []byte) []byte{}}
	s.a.setRewrite(func(path string, hdr http.Header, body []byte) ([]byte, bool) {
		for name, f := range s.rewrites {
			if strings.HasSuffix(path, "/"+name) {
				return f(hdr, body), true
			}
		}
		return nil, false
	})
	return s
}

func (s *hostile) set(name string, f func(h http.Header, body []byte) []byte) { s.rewrites[name] = f }

func (s *hostile) body(name, body string) {
	s.set(name, func(http.Header, []byte) []byte { return []byte(body) })
}

func (s *hostile) play(t *testing.T) msfp.RemotePlayback {
	t.Helper()
	pb, err := s.b.eng.Play(context.Background(), s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
	if err != nil {
		t.Fatal(err)
	}
	return pb
}

func TestRelayHostilePeer(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	sheet := jpegOf(t, 20, 10)
	h.info["movie/tmdb/949#0"] = msfp.CopyInfo{AudioTracks: []msfp.Track{{ID: "a0", Label: "English", Language: "en"}},
		SubtitleTracks: []msfp.Track{{ID: "s0", Label: "Français", Language: "fr"}, {ID: "s1", Label: "PGS", Pictures: true}},
		Trickplay:      &msfp.Trickplay{TileWidth: 10, TileHeight: 10, Columns: 2, Rows: 1, IntervalMs: 10_000, TileCount: 3}}
	h.files["pictures/s1/0"] = nil
	h.files["trickplay/0"], h.files["trickplay/1"] = sheet, sheet
	s := newHostile(t, h)
	pb := s.play(t)
	if pb.Trickplay == nil || pb.Trickplay.Sheets != 2 {
		t.Fatalf("previews: %+v", pb.Trickplay)
	}
	fetch := func(p msfp.MediaPath, rng string) (sink, error) {
		var out sink
		err := s.b.eng.Fetch(ctx, pb.ID, p, rng, &out)
		return out, err
	}
	master := msfp.MediaPath{Kind: msfp.MediaMaster}
	seg := msfp.MediaPath{Kind: msfp.MediaSegment, Name: "seg0.m4s"}
	for _, c := range []struct {
		name, body string
		path       msfp.MediaPath
	}{
		{"master.m3u8", "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nhttps://evil.example/v.m3u8\n", master},
		{"master.m3u8", "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n../v0.m3u8\n", master},
		{"master.m3u8", "#EXTM3U\n#EXT-X-SESSION-KEY:METHOD=AES-128,URI=\"k\"\n", master},
		{"master.m3u8", "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,URI=\"https://evil.example/a\"\n", master},
		{"master.m3u8", "#EXTM3U\n" + strings.Repeat("#x\n", 1<<20), master},
		{"master.m3u8", "not a playlist", master},
		{"seg0.m4s", "#EXTM3U\n#EXTINF:1,\nhttps://evil.example/x\n", seg},
		{"subtitles/s0/0", "<script>WEBVTT", msfp.MediaPath{Kind: msfp.MediaSubtitles, Track: "s0"}},
		{"subtitles/s0/0", "WEBVTT\n" + strings.Repeat("x", 5<<20), msfp.MediaPath{Kind: msfp.MediaSubtitles, Track: "s0"}},
		{"pictures/s1/0", "PG\x00", msfp.MediaPath{Kind: msfp.MediaPictures, Track: "s1"}},
		{"trickplay/0", "not a picture", msfp.MediaPath{Kind: msfp.MediaSheet}},
	} {
		s.body(c.name, c.body)
		if _, err := fetch(c.path, ""); !errors.Is(err, msfp.ErrNotFound) {
			t.Errorf("%s %.40q: %v", c.name, c.body, err)
		}
		delete(s.rewrites, c.name)
	}
	// Tags outside the subset dropped, URIs kept as they are.
	s.body("master.m3u8", "#EXTM3U\n# a comment\n#EXT-X-SESSION-DATA:DATA-ID=\"x\",VALUE=\"y\"\n#EXT-X-STREAM-INF:BANDWIDTH=1\r\nv0.m3u8\n")
	if out, err := fetch(master, ""); err != nil || string(out.body) != "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv0.m3u8\n" {
		t.Errorf("a master with other tags: %v %q", err, out.body)
	}
	delete(s.rewrites, "master.m3u8")
	// WebVTT rebuilt (a tag made of references never comes back); an
	// empty PGS part relayed empty.
	s.body("subtitles/s0/0", "WEBVTT\nSTYLE\n::cue{}\n\n1\n00:00:01.000 --> 00:00:02.000 align:start\n<c.x>&lt;b&gt;hi</c>\n")
	if out, err := fetch(msfp.MediaPath{Kind: msfp.MediaSubtitles, Track: "s0"}, ""); err != nil ||
		string(out.body) != "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nhi\n" {
		t.Errorf("WebVTT: %v %q", err, out.body)
	}
	if out, err := fetch(msfp.MediaPath{Kind: msfp.MediaPictures, Track: "s1"}, ""); err != nil || out.status != 200 || len(out.body) != 0 ||
		out.contentType != "application/octet-stream" {
		t.Errorf("an empty PGS part: %v %+v", err, out)
	}
	// Sheets: re-encoded; of another format, too large, or past the
	// count: refused.
	s.set("trickplay/0", func(_ http.Header, b []byte) []byte { return append(b, "trailing bytes"...) })
	out, err := fetch(msfp.MediaPath{Kind: msfp.MediaSheet}, "")
	if err != nil || out.contentType != "image/jpeg" || bytes.Contains(out.body, []byte("trailing")) {
		t.Errorf("a sheet: %v %q", err, out.contentType)
	} else if _, err := jpeg.Decode(bytes.NewReader(out.body)); err != nil {
		t.Errorf("a sheet re-encoded: %v", err)
	}
	var pngSheet bytes.Buffer
	png.Encode(&pngSheet, image.NewRGBA(image.Rect(0, 0, 20, 10)))
	for _, bad := range [][]byte{pngSheet.Bytes(), jpegOf(t, 21, 10), jpegOf(t, 20, 11)} {
		s.body("trickplay/0", string(bad))
		if _, err := fetch(msfp.MediaPath{Kind: msfp.MediaSheet}, ""); !errors.Is(err, msfp.ErrNotFound) {
			t.Errorf("a hostile sheet: %v", err)
		}
	}
	if _, err := fetch(msfp.MediaPath{Kind: msfp.MediaSheet, Sheet: 2}, ""); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("a sheet past the count: %v", err)
	}
	// Ranges and headers.
	s.set("seg0.m4s", func(h http.Header, b []byte) []byte { h.Set("Content-Range", "bytes 0-3/20"); return b })
	if _, err := fetch(seg, "bytes=2-5"); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("a 206 of another range: %v", err)
	}
	s.set("seg0.m4s", func(h http.Header, b []byte) []byte { h.Set("Content-Length", "99"); return b })
	if _, err := fetch(seg, "bytes=2-5"); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("a 206 whose length isn't its range's: %v", err)
	}
	s.set("seg0.m4s", func(h http.Header, b []byte) []byte { h.Set("Content-Encoding", "gzip"); return b })
	if _, err := fetch(seg, ""); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("a coded answer: %v", err)
	}
	s.set("seg0.m4s", func(h http.Header, b []byte) []byte { h.Set("Content-Type", "text/html"); return b })
	if out, err := fetch(seg, ""); err != nil || out.contentType != "application/octet-stream" || string(out.body) != "0123456789abcdefghij" {
		t.Errorf("a segment of another type: %v %+v", err, out)
	}
	delete(s.rewrites, "seg0.m4s")
	// The peer's failures pass nothing on.
	h.mu.Lock()
	h.serve = func(context.Context, msfp.MediaPath, *msfp.Range, msfp.ResponseSink) error { return msfp.ErrServerBusy }
	h.mu.Unlock()
	if _, err := fetch(seg, ""); !errors.Is(err, msfp.ErrServerBusy) {
		t.Errorf("the peer's 503: %v", err)
	}
	h.mu.Lock()
	h.serve = func(context.Context, msfp.MediaPath, *msfp.Range, msfp.ResponseSink) error {
		return errors.New("broken")
	}
	h.mu.Unlock()
	if _, err := fetch(seg, ""); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("the peer's 500: %v", err)
	}
}

// A play answer that breaks a "must" is stopped and given up; what may be
// cleaned is.
func TestHostilePlayAnswers(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	s := newHostile(t, h)
	edit := func(f func(m map[string]any)) {
		s.set("play", func(_ http.Header, b []byte) []byte {
			var m map[string]any
			json.Unmarshal(b, &m)
			f(m)
			out, _ := json.Marshal(m)
			return out
		})
	}
	for name, f := range map[string]func(m map[string]any){
		"a stream elsewhere": func(m map[string]any) {
			m["stream"].(map[string]any)["url"] = "https://evil.example/api/v1/stream/x/master.m3u8"
		},
		"a stream of another ticket's form": func(m map[string]any) {
			st := m["stream"].(map[string]any)
			st["url"] = strings.Replace(st["url"].(string), "/api/v1/stream/", "/api/v1/stream/%41", 1)
		},
		"a file stream named otherwise": func(m map[string]any) { m["stream"].(map[string]any)["protocol"] = "file" },
		"a subtitle URL elsewhere": func(m map[string]any) {
			m["subtitleTracks"].([]any)[0].(map[string]any)["url"] = "https://evil.example/s"
		},
		"a method MSFP lacks":         func(m map[string]any) { m["method"] = "stream" },
		"a member of the wrong type":  func(m map[string]any) { m["durationMs"] = "5400000" },
		"trickplay of the wrong type": func(m map[string]any) { m["trickplay"] = []any{} },
	} {
		before := len(s.a.exchanges("stop"))
		edit(f)
		if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); !errors.Is(err, msfp.ErrNotPlayable) {
			t.Errorf("%s: %v", name, err)
		}
		waitAll(s.b, s.a)
		if len(s.a.exchanges("stop")) != before+1 {
			t.Errorf("%s: no stop sent", name)
		}
	}
	edit(func(m map[string]any) { m["id"] = "play_UPPER" })
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); !errors.Is(err, msfp.ErrNotPlayable) {
		t.Errorf("an id of another form: %v", err)
	}
	// Cleaned, not refused.
	for _, v := range s.a.eng.Playbacks() {
		s.a.eng.StopPlayback(v.ID)
	}
	edit(func(m map[string]any) {
		m["audioTracks"] = []any{map[string]any{"id": "a0", "label": "Eng\u202elish\u0007"}, map[string]any{"id": "a01", "label": "x"},
			map[string]any{"id": "a2", "label": ""}, map[string]any{"id": "a3", "label": "y", "role": "evil"}}
		m["versions"] = []any{map[string]any{"id": "../x", "name": "x"}, map[string]any{"id": "v1", "name": "<b>HD</b>"}}
		m["segments"] = []any{map[string]any{"kind": "intro", "startMs": 0, "endMs": 1000}, map[string]any{"kind": "ad", "startMs": 0, "endMs": 1}}
		m["durationMs"] = 9_000_000_000
		m["title"] = map[string]any{"name": "whatever"}
		m["trickplay"] = map[string]any{"tileWidth": 5000, "tileHeight": 10, "columns": 1, "rows": 1, "intervalMs": 1000,
			"tileCount": 1, "sheets": []any{strings.TrimSuffix(m["stream"].(map[string]any)["url"].(string), "master.m3u8") + "trickplay/0"}}
	})
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pb.AudioTracks) != 2 || pb.AudioTracks[0].Label != "English" || pb.AudioTracks[1].Role != "" ||
		len(pb.Versions) != 1 || pb.Versions[0].Name != "<b>HD</b>" || len(pb.Segments) != 1 || pb.DurationMs != 0 || pb.Trickplay != nil {
		t.Errorf("cleaned: %+v", pb)
	}
}

// The cases of vectors/v1/media.json, through a relay: what a peer's
// playlists, WebVTT and PGS parts become on the way to the viewer.
func TestMediaVectorsThroughRelay(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile("wire/testdata/v1/media.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		WebVTT []struct {
			Name, Text, Hex, Output string
			Valid                   bool
		}
		HLS []struct {
			Name, Text string
			Valid      bool
		}
		PGS []struct {
			Name, Hex string
			Valid     bool
		}
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	h := filmHost()
	h.files["pictures/s1/0"] = nil
	s := newHostile(t, h)
	pb := s.play(t)
	fetch := func(name string, body []byte, p msfp.MediaPath) (sink, error) {
		s.set(name, func(http.Header, []byte) []byte { return body })
		var out sink
		return out, s.b.eng.Fetch(ctx, pb.ID, p, "", &out)
	}
	for _, c := range v.HLS {
		// Each as a media playlist (a master's tags are a subset of a
		// media playlist's checks).
		if _, err := fetch("v0.m3u8", []byte(c.Text), msfp.MediaPath{Kind: msfp.MediaPlaylist, Name: "v0.m3u8"}); (err == nil) != c.Valid {
			t.Errorf("hls %s: %v, want valid %v", c.Name, err, c.Valid)
		}
	}
	for _, c := range v.WebVTT {
		in := []byte(c.Text)
		if c.Hex != "" {
			in, _ = hex.DecodeString(c.Hex)
		}
		out, err := fetch("subtitles/s0/0", in, msfp.MediaPath{Kind: msfp.MediaSubtitles, Track: "s0"})
		if (err == nil) != c.Valid || c.Valid && string(out.body) != c.Output {
			t.Errorf("webvtt %s: %v %q", c.Name, err, out.body)
		}
	}
	for _, c := range v.PGS {
		in, _ := hex.DecodeString(c.Hex)
		out, err := fetch("pictures/s1/0", in, msfp.MediaPath{Kind: msfp.MediaPictures, Track: "s1"})
		if (err == nil) != c.Valid || c.Valid && !bytes.Equal(out.body, in) {
			t.Errorf("pgs %s: %v", c.Name, err)
		}
	}
}
