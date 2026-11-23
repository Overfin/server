package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/delivery"
	"github.com/overfin/server/internal/files"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/previews"
	"github.com/overfin/server/internal/store"
)

// testMaker makes exactly what it's ordered.
type testMaker struct {
	mu      sync.Mutex
	enc     delivery.Encoders
	orders  []delivery.Order
	stopped []string
}

func (m *testMaker) Encoders(context.Context) (delivery.Encoders, error) { return m.enc, nil }

func (m *testMaker) Start(_ context.Context, o delivery.Order) (*delivery.Made, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orders = append(m.orders, o)
	made := &delivery.Made{ID: fmt.Sprintf("%032x", len(m.orders)), Segments: o.Segments, AudioIndex: -1,
		Playlist: []byte("#EXTM3U\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXTINF:6.0,\nhls1/main/0.ts\n#EXT-X-ENDLIST\n")}
	if v := o.Video; v != nil {
		made.VideoCopied = v.Copy
		if !v.Copy {
			made.VideoCodec, made.Height, made.Width, made.VideoBitrate, made.ToneMapped = v.Codec, v.MaxHeight, v.MaxHeight*16/9, v.MaxBitrate, v.ToneMap
		}
	}
	if a := o.Audio; a != nil {
		made.AudioIndex, made.AudioCopied = a.Index, a.Copy
		if !a.Copy {
			made.AudioCodec, made.AudioChannels = a.Codec, a.Channels
		}
	}
	return made, nil
}

func (m *testMaker) Ping(context.Context, string) error { return nil }

func (m *testMaker) Stop(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = append(m.stopped, id)
	return nil
}

func (m *testMaker) Clear(context.Context) error { return nil }

// stops is how many deliveries were stopped.
func (m *testMaker) stops() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.stopped)
}

func (m *testMaker) last() delivery.Order {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.orders[len(m.orders)-1]
}

// played is a test server with two copies of Heat (TMDB 949): item 1 in
// the films library, an H.264 1080p copy Overfin read, with E-AC-3 5.1
// and AAC stereo, French subtitles in SubRip and PGS, its final credits
// from 80 %, seek-bar previews, its file on disk; item 2 in the music
// library, a minimal copy. And Up (TMDB 14160), item 3, read, without
// skip markers.
type played struct {
	srv      *httptest.Server
	fake     *fakeJellyfin
	st       *store.Store
	tickets  *playback.Tickets
	maker    *testMaker
	original *bool
	file     []byte
}

const (
	heatCopy    = "vheatheatheatheatheatheathe"
	heatMinimal = "vheatminimalminimalminimalm"
	upCopy      = "vupupupupupupupupupupupupup"
	previewsFP  = "x1-0123456789abcdef0123456789abcdef"
)

func playedServer(t *testing.T, o options) *played {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	libDir := filepath.Join(dir, "films")
	os.MkdirAll(libDir, 0o700)
	path := filepath.Join(libDir, "heat.mkv")
	content := bytes.Repeat([]byte("0123456789"), max(o.fileSize, 10_000)/10)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	p := &played{maker: &testMaker{enc: delivery.Encoders{Video: []string{"h264", "hevc"}, Audio: []string{"aac", "ac3", "eac3"}, ToneMapping: true}},
		original: new(bool), file: content}
	*p.original = true
	o.delivery = &delivery.Executor{Maker: p.maker,
		Path:         func(context.Context, string, string) (string, error) { return path, nil },
		Libraries:    func(context.Context) (files.Libraries, error) { return files.NewLibraries([]string{libDir}), nil },
		Fingerprint:  func(context.Context, string, string, *files.File) (string, error) { return previewsFP, nil },
		OriginalFile: func() bool { return *p.original }}
	o.store, o.tickets = &p.st, &p.tickets
	// Overfin's own previews of Heat's file: one sheet.
	pvDir := filepath.Join(dir, "previews")
	pv, err := previews.NewStore(pvDir)
	if err != nil {
		t.Fatal(err)
	}
	sets := filepath.Join(pvDir, previewsFP[len(previewsFP)-32:len(previewsFP)-30], previewsFP)
	os.MkdirAll(sets, 0o700)
	os.WriteFile(filepath.Join(sets, "set.json"), []byte(`{"width":160,"height":90,"columns":2,"rows":2,"count":3,"intervalMs":10000}`), 0o600)
	var sheet bytes.Buffer
	jpeg.Encode(&sheet, image.NewRGBA(image.Rect(0, 0, 320, 180)), nil)
	os.WriteFile(filepath.Join(sets, "0.jpg"), sheet.Bytes(), 0o600)
	o.previews = func(st *store.Store) *previews.Keeper { return &previews.Keeper{Store: pv, DB: st} }

	p.srv, p.fake = newServerWith(t, o)
	st := p.st
	id := func(n int) string { return fmt.Sprintf("%032x", n) }
	films, music := id(0xf1), id(0xf2)
	if err := st.PutCatalog(ctx, []store.CatalogItem{
		{ID: id(1), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: films, SegmentsRead: true,
			Segments: map[string][]wire.CopySegment{id(1): {{Kind: "intro", StartMs: 0, EndMs: 60_000}, {Kind: "outro", StartMs: 4_800_000, EndMs: 6_000_000}}}},
		{ID: id(2), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: music},
		{ID: id(3), Kind: "movie", Name: "Up", SortName: "up", TMDB: "14160", LibraryID: films},
	}, 1); err != nil {
		t.Fatal(err)
	}
	st.RefreshTitleKeys(ctx)
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	now := time.Now()
	st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "949", Name: "Heat", Year: 1995}, []byte(`{}`), now)
	st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "14160", Name: "Up", Year: 2009}, []byte(`{}`), now)
	one := func() *int { v := 1; return &v }
	read := wire.Copy{DurationMs: 6_000_000, MediaType: "video/matroska", Size: int64(len(content)),
		Video: &wire.VideoStream{Codec: "h264", Codecs: "avc1.640028", Width: 1920, Height: 1080, BitDepth: 8,
			Colour: &wire.VideoColour{Primaries: one(), Transfer: one(), Matrix: one()}},
		AudioTracks: []wire.CopyAudio{
			{ID: "a1", Codec: "eac3", Codecs: "ec-3", Channels: 6, Default: true, Language: "en", SampleRate: 48000},
			{ID: "a2", Codec: "aac", Codecs: "mp4a.40.2", Channels: 2, Language: "fr", SampleRate: 48000}},
		SubtitleTracks: []wire.CopySubtitle{{ID: "s3", Format: "subrip", Language: "fr"},
			{ID: "s4", Format: "hdmv_pgs_subtitle", Language: "fr"}, {ID: "s5", Format: "dvd_subtitle", Language: "en"}},
		Chapters: []wire.CopyChapter{{StartMs: 0, Name: "Opening"}}}
	layout := &wire.PreviewLayout{TileWidth: 160, TileHeight: 90, Columns: 2, Rows: 2, IntervalMs: 10000, TileCount: 3}
	for _, c := range []store.IndexedCopy{
		{CopyID: heatCopy, ItemID: id(1), SourceID: id(1), Fingerprint: previewsFP, ETag: "heatetagheatetagheatet",
			Size: int64(len(content)), Facts: read, Streams: map[string]int{"v": 0, "a1": 1, "a2": 2, "s3": 3, "s4": 4, "s5": 5},
			Parts: 1, PreviewLayout: layout},
		{CopyID: heatMinimal, ItemID: id(2), SourceID: id(2), Fingerprint: "x1-" + strings.Repeat("b", 32), ETag: "minimaletagminimaletag",
			Size: 4_000_000_000, Minimal: true, Parts: 1, Streams: map[string]int{"a1": 1},
			Facts: wire.Copy{DurationMs: 6_000_000, MediaType: "video/mp4", Size: 4_000_000_000,
				AudioTracks: []wire.CopyAudio{{ID: "a1", Codec: "aac", Channels: 2}}}},
		{CopyID: upCopy, ItemID: id(3), SourceID: id(3), Fingerprint: "x1-" + strings.Repeat("c", 32), ETag: "upetagupetagupetagupet",
			Size: int64(len(content)), Facts: read, Streams: map[string]int{"v": 0, "a1": 1, "a2": 2, "s3": 3, "s4": 4, "s5": 5}, Parts: 1},
	} {
		if err := st.PutCopy(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	st.SetPreviewFile(ctx, store.PreviewFile{ItemID: id(1), SourceID: id(1), Size: int64(len(content)), Fingerprint: previewsFP}, now)
	return p
}

// titlePage is what the tests read of a title's page.
type titlePage struct {
	Copies []wire.Copy
	Limits *struct {
		MaxBitrateKbps int
		OriginalFile   bool
	}
	Wishes   *struct{ Audio, Subtitles string }
	Encoders *struct {
		Video, Audio, Ranges []string
		ToneMapping          bool
	}
	UserState *struct {
		Played     bool
		PositionMs int64
	}
}

// A film's page gives its copies as MSFP's copy objects, those the
// profile may see, in Overfin's order, with Overfin's wrapper: the limits
// a play goes by, the tracks wished for, what the server converts to.
func TestTitlePageCopies(t *testing.T) {
	p := playedServer(t, options{maxBitrateKbps: 40_000})
	*p.original = false
	var heat titlePage
	if code := get(t, p.srv, "/api/v1/titles/title_movie_tmdb_949", &heat); code != 200 {
		t.Fatalf("Heat: %d", code)
	}
	if len(heat.Copies) != 2 || heat.Copies[0].ID != heatCopy || heat.Copies[1].ID != heatMinimal {
		t.Fatalf("copies: %+v", heat.Copies)
	}
	c := heat.Copies[0]
	if c.ETag != "heatetagheatetagheatet" || c.Video == nil || c.Video.Codecs != "avc1.640028" || len(c.AudioTracks) != 2 ||
		len(c.SubtitleTracks) != 3 || c.PreviewLayout == nil || c.PreviewLayout.TileCount != 3 || len(c.Segments) != 2 ||
		c.Segments[1].Kind != "outro" || len(c.Chapters) != 1 {
		t.Errorf("Heat's copy: %+v", c)
	}
	if m := heat.Copies[1]; m.Video != nil || m.MediaType != "video/mp4" || len(m.AudioTracks) != 1 {
		t.Errorf("the minimal copy: %+v", m)
	}
	if heat.Limits == nil || heat.Limits.MaxBitrateKbps != 40_000 || heat.Limits.OriginalFile || heat.Wishes == nil ||
		heat.Encoders == nil || strings.Join(heat.Encoders.Video, ",") != "h264,hevc" || strings.Join(heat.Encoders.Ranges, ",") != "sdr" ||
		!heat.Encoders.ToneMapping {
		t.Errorf("wrapper: %+v %+v %+v", heat.Limits, heat.Wishes, heat.Encoders)
	}
	// Each copy exactly as MSFP's (no member of Overfin's own in it).
	_, body := getAs(t, p.srv, subject, "/api/v1/titles/title_movie_tmdb_949")
	var raw struct{ Copies []json.RawMessage }
	json.Unmarshal([]byte(body), &raw)
	for _, r := range raw.Copies {
		if _, err := wire.ReadCopy(r); err != nil {
			t.Errorf("not an MSFP copy: %v %s", err, r)
		}
		var m map[string]any
		json.Unmarshal(r, &m)
		for k := range m {
			if !strings.Contains(" id edition durationMs startMs endMs mediaType size etag parts video audioTracks subtitleTracks chapters segments previewLayout threeD ", " "+k+" ") {
				t.Errorf("a member of a copy MSFP doesn't have: %s", k)
			}
		}
	}

	// A profile limited to the films library: the copy in music isn't
	// its, to see or to play.
	limitedProfile(t, p.srv, fmt.Sprintf("%032x", 0xf1))
	var limited titlePage
	get(t, p.srv, "/api/v1/titles/title_movie_tmdb_949", &limited)
	if len(limited.Copies) != 1 || limited.Copies[0].ID != heatCopy {
		t.Errorf("limited: %+v", limited.Copies)
	}
	if code, body := post(t, p.srv, "/api/v1/playback", `{"titleId":"title_movie_tmdb_949","versionId":"`+heatMinimal+`"}`); code != 404 {
		t.Errorf("playing a copy the profile may not see: %d %s", code, body)
	}
}

// limitedProfile makes a profile that sees one library only, and makes it
// the active one.
func limitedProfile(t *testing.T, srv *httptest.Server, library string) {
	t.Helper()
	code, body := post(t, srv, "/api/v1/profiles", `{"name":"Partner"}`)
	var partner struct{ ID string }
	json.Unmarshal([]byte(body), &partner)
	if code != 201 {
		t.Fatalf("create: %d %s", code, body)
	}
	if code, body := put(t, srv, "PATCH", "/api/v1/profiles/"+partner.ID, `{"libraries":["lib_`+library+`"]}`); code != 200 {
		t.Fatalf("limit: %d %s", code, body)
	}
	if code, body := post(t, srv, "/api/v1/profiles/"+partner.ID+"/activate", `{}`); code != 200 {
		t.Fatalf("activate: %d %s", code, body)
	}
}

// playAnswer is what the tests read of a play's answer.
type playAnswer struct {
	ID, Ticket, Protocol, Method, Etag, VersionID, AudioTrack string
	PositionMs                                                int64
	MaxBitrateKbps                                            *int
	Subtitles                                                 []wire.ServedSubtitle
	Title                                                     *struct{ ID, Name string }
	Type, Detail                                              string // a problem's
}

func play(t *testing.T, srv *httptest.Server, body string) (int, playAnswer, string) {
	t.Helper()
	code, raw := post(t, srv, "/api/v1/playback", body)
	var a playAnswer
	json.Unmarshal([]byte(raw), &a)
	return code, a, raw
}

// A play from MSFP's play request: the answer is MSFP's playback object
// (no URL), what the player asked made exactly, the stream served from
// the ticket; restored as it is after a reload; the older body makes
// the page load again.
func TestPlayRequest(t *testing.T) {
	p := playedServer(t, options{maxBitrateKbps: 40_000})
	code, a, raw := play(t, p.srv, `{"titleId":"title_movie_tmdb_949","versionId":"`+heatCopy+`","audioTrack":"a2",
		"delivery":"remux","mediaType":"video/mp4","startPositionMs":60000,"device":"Laptop","reason":"container"}`)
	if code != 201 {
		t.Fatalf("remux: %d %s", code, raw)
	}
	if a.Protocol != "hls" || a.Method != "remux" || a.Etag != "heatetagheatetagheatet" || a.VersionID != heatCopy ||
		a.AudioTrack != "a2" || a.PositionMs != 60_000 || a.MaxBitrateKbps == nil || *a.MaxBitrateKbps != 40_000 || a.Title == nil ||
		a.Title.Name != "Heat" || len(a.Ticket) < 32 {
		t.Errorf("answer: %s", raw)
	}
	// Text and PGS apart; DVD pictures not.
	if len(a.Subtitles) != 2 || a.Subtitles[0] != (wire.ServedSubtitle{ID: "s3", Form: "text"}) ||
		a.Subtitles[1] != (wire.ServedSubtitle{ID: "s4", Form: "picture"}) {
		t.Errorf("subtitles: %+v", a.Subtitles)
	}
	for _, leak := range []string{"url", "http", "/api/", "stream", "Videos", strings.Repeat("0", 31) + "1"} {
		if strings.Contains(raw, leak) {
			t.Errorf("the answer holds %q: %s", leak, raw)
		}
	}
	if _, err := wire.ReadPlayAnswer([]byte(raw), wire.Asked{Purpose: "play", Delivery: "remux", Part: wire.PartVideo,
		CatalogEtag: "heatetagheatetagheatet", Subtitles: []wire.CopySubtitle{{ID: "s3", Format: "subrip"}, {ID: "s4", Format: "hdmv_pgs_subtitle"}}}); err != nil {
		t.Errorf("not an MSFP answer: %v", err)
	}
	o := p.maker.last()
	if o.Video == nil || !o.Video.Copy || o.Audio == nil || !o.Audio.Copy || o.Audio.Index != 2 || o.Segments != delivery.SegmentsMP4 || o.StartMs != 60_000 {
		t.Errorf("ordered: %+v %+v %+v", o, o.Video, o.Audio)
	}
	// The stream, under the ticket: Overfin's master playlist.
	if code, body := getAs(t, p.srv, subject, "/api/v1/stream/"+a.Ticket+"/master.m3u8"); code != 200 || !strings.Contains(body, "#EXT-X-STREAM-INF") {
		t.Errorf("master: %d %s", code, body)
	}
	// Now playing's reason.
	s, ok := p.tickets.Find(a.ID, time.Now())
	if !ok || s.Reason != "container" || s.Device != "Laptop" {
		t.Errorf("the playback: %+v", s)
	}
	// After a reload: the same answer.
	var back playAnswer
	if code := get(t, p.srv, "/api/v1/playback/current", &back); code != 200 || back.ID != a.ID || back.Ticket != a.Ticket || back.VersionID != heatCopy {
		t.Errorf("current: %d %+v", code, back)
	}
	// Without a copy or a delivery: the first copy, its default audio, the
	// baseline (the H.264 picture within it copied, E-AC-3 converted).
	code, a, raw = play(t, p.srv, `{"titleId":"title_movie_tmdb_949"}`)
	if o := p.maker.last(); code != 201 || a.VersionID != heatCopy || a.AudioTrack != "a1" || a.Method != "transcode" ||
		!o.Video.Copy || o.Audio.Copy || o.Audio.Codec != "aac" {
		t.Errorf("baseline: %d %s %+v", code, raw, o.Audio)
	}
	// The file itself, by ranges, while the operator lets players have it.
	code, a, raw = play(t, p.srv, `{"titleId":"title_movie_tmdb_949","delivery":"file"}`)
	if code != 201 || a.Protocol != "file" || a.Method != "direct" {
		t.Fatalf("file: %d %s", code, raw)
	}
	req, _ := http.NewRequest("GET", p.srv.URL+"/api/v1/stream/"+a.Ticket+"/file", nil)
	req.Header.Set("Remote-User", subject)
	req.Header.Set("Range", "bytes=10-19")
	res, err := client(p.srv, subject).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 206 || string(got) != string(p.file[10:20]) {
		t.Errorf("the file's range: %d %q", res.StatusCode, got)
	}
	// The older body, from a page still on an older player: "not signed
	// in", which sends it through sign-in and so loads the site again;
	// nothing starts.
	n := len(p.tickets.Live(time.Now()))
	for _, older := range []string{`{"itemId":"item_` + fmt.Sprintf("%032x", 1) + `","capabilities":{},"device":"Laptop"}`,
		`{"titleId":"title_movie_tmdb_949","capabilities":{}}`, `{"titleId":"title_movie_tmdb_949","audioLanguage":"fr"}`} {
		code, raw = post(t, p.srv, "/api/v1/playback", older)
		if code != http.StatusUnauthorized || !strings.Contains(raw, `"type":"not-authenticated"`) {
			t.Errorf("the older body %s: %d %s", older, code, raw)
		}
	}
	if len(p.tickets.Live(time.Now())) != n {
		t.Error("the older body started a playback")
	}
}

// What's refused, and how: 400 only for what's malformed; 409
// delivery-refused, with a closed code, for what isn't made; 503 when
// every transcode is taken; 404 for a copy the title hasn't.
func TestPlayRefusals(t *testing.T) {
	p := playedServer(t, options{maxTranscodes: 1, maxBitrateKbps: 40_000})
	heat := `"titleId":"title_movie_tmdb_949"`
	for _, body := range []string{
		`{` + heat + `,"purpose":"download"}`,
		`{` + heat + `,"delivery":"no such"}`,
		`{` + heat + `,"delivery":"file","mediaType":"video/mp4"}`,
		`{` + heat + `,"delivery":"remux"}`,
		`{` + heat + `,"transcode":{"videoCodec":"h264"}}`,
		`{` + heat + `,"delivery":"transcode","transcode":{"maxHeight":721}}`,
		`{` + heat + `,"audioTrack":"a01"}`,
		`{` + heat + `,"audioTrack":null}`,
		`{` + heat + `,"reason":"because"}`,
		`{` + heat + `,"device":"é"}`,
		`{` + heat + `,"startPositionMs":-1}`,
		`{"titleId":"title_movie_tmdb_0949"}`,
		`{"versionId":"` + heatCopy + `"}`,
		`{` + heat + `,"title":"movie/tmdb/949"}`,
	} {
		if code, _, raw := play(t, p.srv, body); code != 400 {
			t.Errorf("%s: %d %s", body, code, raw)
		}
	}
	for _, tc := range []struct{ body, why string }{
		{`{` + heat + `,"delivery":"transcode","transcode":{"burnSubtitle":"s3"}}`, "burned-subtitles"},
		{`{` + heat + `,"delivery":"transcode","transcode":{"videoCodec":"vp9"}}`, ""},
		{`{` + heat + `,"versionId":"` + heatMinimal + `","delivery":"file"}`, "not-read"},
		{`{` + heat + `,"delivery":"remux","mediaType":"video/webm"}`, ""},
	} {
		code, a, raw := play(t, p.srv, tc.body)
		if code != 409 || a.Type != "delivery-refused" || !strings.HasPrefix(a.Detail, tc.why) {
			t.Errorf("%s: %d %s", tc.body, code, raw)
		}
	}
	*p.original = false
	if code, a, raw := play(t, p.srv, `{`+heat+`,"delivery":"file"}`); code != 409 || a.Detail != "original-file-off" {
		t.Errorf("the file, the switch off: %d %s", code, raw)
	}
	if code, _, raw := play(t, p.srv, `{`+heat+`,"versionId":"vnosuchcopy"}`); code != 404 {
		t.Errorf("a copy the title hasn't: %d %s", code, raw)
	}
	if code, _, raw := play(t, p.srv, `{"titleId":"title_movie_tmdb_550"}`); code != 404 {
		t.Errorf("a title the libraries haven't: %d %s", code, raw)
	}
	// One transcode in all: the picture converted takes it; a second is
	// refused before anything starts, a remux still plays.
	if code, _, raw := play(t, p.srv, `{`+heat+`,"delivery":"transcode","transcode":{"videoCodec":"h264","maxHeight":720},"device":"TV"}`); code != 201 {
		t.Fatalf("transcode: %d %s", code, raw)
	}
	n := len(p.maker.orders)
	if code, a, raw := play(t, p.srv, `{"titleId":"title_movie_tmdb_14160","delivery":"transcode","transcode":{"videoCodec":"hevc"}}`); code != 503 || a.Type != "server-busy" {
		t.Errorf("a second transcode: %d %s", code, raw)
	}
	if len(p.maker.orders) != n {
		t.Error("a refused transcode was ordered")
	}
	if code, _, raw := play(t, p.srv, `{"titleId":"title_movie_tmdb_14160","delivery":"remux","mediaType":"video/mp4"}`); code != 201 {
		t.Errorf("a remux beside: %d %s", code, raw)
	}
}

// Progress keeps where it is and whether it's paused; a stop past the
// copy's final credits is watched, without credits past 90 % only; no
// "ended".
func TestPlayProgressAndPlayed(t *testing.T) {
	p := playedServer(t, options{})
	code, a, raw := play(t, p.srv, `{"titleId":"title_movie_tmdb_949","delivery":"remux","mediaType":"video/mp4"}`)
	if code != 201 {
		t.Fatalf("Heat: %d %s", code, raw)
	}
	if code, body := post(t, p.srv, "/api/v1/playback/"+a.ID+"/progress", `{"positionMs":1200000,"paused":true}`); code != 204 {
		t.Fatalf("progress: %d %s", code, body)
	}
	s, _ := p.tickets.Find(a.ID, time.Now())
	if snap := p.tickets.Snapshot(s); !snap.Paused || snap.PositionTicks != 1_200_000*10_000 {
		t.Errorf("progress kept: %+v", snap)
	}
	// Past the credits (80 %), short of 90 %: watched.
	if code, body := post(t, p.srv, "/api/v1/playback/"+a.ID+"/stop", `{"positionMs":4900000}`); code != 204 {
		t.Fatalf("stop: %d %s", code, body)
	}
	var heat titlePage
	if !eventually(t, func() bool {
		get(t, p.srv, "/api/v1/titles/title_movie_tmdb_949", &heat)
		return heat.UserState != nil && heat.UserState.Played
	}) {
		t.Errorf("Heat, stopped in its credits: %+v", heat.UserState)
	}
	// Up has no skip markers: at 85 % it's where it stopped.
	_, a, _ = play(t, p.srv, `{"titleId":"title_movie_tmdb_14160","delivery":"remux","mediaType":"video/mp4"}`)
	if code, body := post(t, p.srv, "/api/v1/playback/"+a.ID+"/stop", `{"positionMs":5100000}`); code != 204 {
		t.Fatalf("stop: %d %s", code, body)
	}
	var up titlePage
	if !eventually(t, func() bool {
		get(t, p.srv, "/api/v1/titles/title_movie_tmdb_14160", &up)
		return up.UserState != nil && up.UserState.PositionMs == 5_100_000
	}) || up.UserState.Played {
		t.Errorf("Up, stopped at 85 %%: %+v", up.UserState)
	}
	if len(p.maker.stopped) == 0 {
		t.Error("the deliveries weren't stopped")
	}
}

// The previews call: a ticket for a copy's sheets before it plays, in
// this session only, for a copy the profile may see with previews
// Overfin made; the playback's own sheets under its ticket too.
func TestPreviewsCall(t *testing.T) {
	p := playedServer(t, options{})
	code, raw := post(t, p.srv, "/api/v1/previews", `{"titleId":"title_movie_tmdb_949","versionId":"`+heatCopy+`"}`)
	var pa wire.PreviewsAnswer
	json.Unmarshal([]byte(raw), &pa)
	if code != 200 || pa.ETag != "heatetagheatetagheatet" || len(pa.Ticket) < 32 {
		t.Fatalf("previews: %d %s", code, raw)
	}
	if _, err := wire.ReadPreviewsAnswer([]byte(raw)); err != nil {
		t.Errorf("not MSFP's answer: %v", err)
	}
	if code, body := getAs(t, p.srv, subject, "/api/v1/stream/"+pa.Ticket+"/trickplay/0"); code != 200 || !strings.HasPrefix(body, "\xff\xd8") {
		t.Errorf("sheet 0: %d", code)
	}
	if code, _ := getAs(t, p.srv, subject, "/api/v1/stream/"+pa.Ticket+"/trickplay/1"); code != 404 {
		t.Errorf("a sheet the layout hasn't: %d", code)
	}
	// Another account's session, or another site asking: nothing.
	if code, _ := getAs(t, p.srv, "other-subject", "/api/v1/stream/"+pa.Ticket+"/trickplay/0"); code == 200 {
		t.Errorf("another account: %d", code)
	}
	req, _ := http.NewRequest("GET", p.srv.URL+"/api/v1/stream/"+pa.Ticket+"/trickplay/0", nil)
	req.Header.Set("Remote-User", subject)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if res, err := client(p.srv, subject).Do(req); err != nil || res.StatusCode != 404 {
		t.Errorf("asked from another site: %v %v", err, res)
	} else {
		res.Body.Close()
	}
	for body, want := range map[string]int{
		`{"titleId":"title_movie_tmdb_949","versionId":"` + heatMinimal + `"}`: 404, // no previews
		`{"titleId":"title_movie_tmdb_949","versionId":"vnosuchcopy"}`:         404,
		`{"titleId":"title_movie_tmdb_949"}`:                                   400,
		`{"titleId":"title_movie_tmdb_949","versionId":"a b"}`:                 400,
	} {
		if code, raw := post(t, p.srv, "/api/v1/previews", body); code != want {
			t.Errorf("%s: %d %s", body, code, raw)
		}
	}
	// A playback's sheets, under its own ticket.
	_, a, _ := play(t, p.srv, `{"titleId":"title_movie_tmdb_949","delivery":"remux","mediaType":"video/mp4"}`)
	if code, _ := getAs(t, p.srv, subject, "/api/v1/stream/"+a.Ticket+"/trickplay/0"); code != 200 {
		t.Errorf("a playback's sheet: %d", code)
	}
}

// startPlayback starts a transcode of Heat's copy (its picture converted:
// a transcode slot) on a played server, from device, at 1 s.
func startPlayback(t *testing.T, srv *httptest.Server, device string) string {
	t.Helper()
	return startDelivery(t, srv, device, `"delivery":"transcode","transcode":{"videoCodec":"h264"}`)
}

// startDelivery starts a play of Heat's copy on a played server, with
// the play request's members delivery gives.
func startDelivery(t *testing.T, srv *httptest.Server, device, delivery string) string {
	t.Helper()
	code, body := post(t, srv, "/api/v1/playback", `{"titleId":"title_movie_tmdb_949","versionId":"`+heatCopy+`",`+delivery+
		`,"device":"`+device+`","startPositionMs":1000}`)
	if code != http.StatusCreated {
		t.Fatalf("start: %d %s", code, body)
	}
	var s struct{ ID string }
	json.Unmarshal([]byte(body), &s)
	return s.ID
}

// A delivery's subtitles: a text track in stretches from Overfin's plugin
// (asked with Overfin's own key, each 10 s early) or whole, rebuilt as
// WebVTT (MSFP video section 6.3); a track of pictures (PGS) as the
// stretch itself, checked whole; DVD subtitles not served.
func TestDeliverySubtitles(t *testing.T) {
	p := playedServer(t, options{})
	code, a, raw := play(t, p.srv, `{"titleId":"title_movie_tmdb_949","versionId":"`+heatCopy+`","delivery":"remux","mediaType":"video/mp4"}`)
	if code != 201 {
		t.Fatalf("play: %d %s", code, raw)
	}
	base := "/api/v1/stream/" + a.Ticket + "/"
	fake := p.fake
	const cue = "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<b>Hi</b>\n"
	if code, vtt := getAs(t, p.srv, subject, base+"subtitles/s3/0"); code != 200 || vtt != cue {
		t.Errorf("subtitles: %d %q", code, vtt)
	}
	if code, _ := getAs(t, p.srv, subject, base+"subtitles/s3/1"); code != 200 {
		t.Errorf("subtitles, part 1: %d", code)
	}
	fake.mu.Lock()
	if got := strings.Join(fake.stretches, ", "); got != "3 0 120, 3 110 240" {
		t.Errorf("stretches asked: %s", got)
	}
	fake.stretches = nil
	fake.mu.Unlock()
	// Whole: the plugin's stretches of 10 minutes over the copy's length
	// (100 minutes), a cue shown in two of them kept once.
	req, _ := http.NewRequest("GET", p.srv.URL+base+"subtitles/s3/whole", nil)
	req.Header.Set("Remote-User", subject)
	res, err := client(p.srv, subject).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	whole, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(whole) != cue || res.Header.Get("Content-Type") != "text/vtt; charset=utf-8" {
		t.Errorf("whole: %d %q %s", res.StatusCode, whole, res.Header.Get("Content-Type"))
	}
	fake.mu.Lock()
	if got := strings.Join(fake.stretches, ", "); !strings.HasPrefix(got, "3 0 600, 3 600 1200, ") || !strings.HasSuffix(got, ", 3 5400 6000") ||
		len(fake.stretches) != 10 {
		t.Errorf("whole, stretches asked: %s", got)
	}
	fake.mu.Unlock()
	for _, part := range []string{"", "/-1", "/x", "/01", "/10000", "/1/2", "/Whole", "/whole/0"} {
		if code, _ := getAs(t, p.srv, subject, base+"subtitles/s3"+part); code != http.StatusNotFound {
			t.Errorf("subtitles part %q: status %d, want 404", part, code)
		}
	}
	// Track 4 is pictures (PGS): no WebVTT, whole or not, but the stretch
	// itself; one that isn't well-formed is refused; text track 3 has no
	// pictures, DVD subtitles (5) aren't served.
	for _, u := range []string{base + "subtitles/s4/0", base + "subtitles/s4/whole", base + "subtitles/s5/0", base + "pictures/s4/whole"} {
		if code, _ := getAs(t, p.srv, subject, u); code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", u, code)
		}
	}
	fake.mu.Lock()
	fake.pgs = pgsTrack()
	fake.mu.Unlock()
	if code, body := getAs(t, p.srv, subject, base+"pictures/s4/3"); code != 200 || body != string(fake.pgs) {
		t.Errorf("picture subtitles: %d, %d bytes", code, len(body))
	}
	fake.mu.Lock()
	fake.pgs = []byte{}
	fake.mu.Unlock()
	if code, body := getAs(t, p.srv, subject, base+"pictures/s4/3"); code != 200 || body != "" {
		t.Errorf("picture subtitles, a stretch without any: %d, %d bytes", code, len(body))
	}
	fake.mu.Lock()
	fake.pgs = append(pgsTrack(), "PX"...)
	fake.mu.Unlock()
	for _, u := range []string{base + "pictures/s4/0", base + "pictures/s3/0", base + "pictures/s04/3", base + "pictures/s5/0",
		base + "pictures/s4/0?x=1", base + "pictures/s4"} {
		if code, _ := getAs(t, p.srv, subject, u); code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", u, code)
		}
	}
	// Another account gets nothing.
	if code, _ := getAs(t, p.srv, "other-subject", base+"subtitles/s3/whole"); code != http.StatusNotFound {
		t.Errorf("another account: %d", code)
	}
}
