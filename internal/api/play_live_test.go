package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go/describe"
	"github.com/overfin/server/internal/copies"
	"github.com/overfin/server/internal/delivery"
	"github.com/overfin/server/internal/files"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/store"
)

// A live test of the players' API on MSFP's media model, end to end: a
// title's page gives its copy, plays of it are asked with MSFP's play
// request, made by Jellyfin's transcoder through the companion plugin
// (0.9.0), and fetched as a player does, under the ticket. A test
// Jellyfin only, never a real one, with the plugin installed and
// Overfin's key set in it. It runs only when told where (as the
// executor's live test, internal/delivery):
//
//	OVERFIN_LIVE_JELLYFIN  the test Jellyfin's address
//	OVERFIN_LIVE_KEY       the API key set in the plugin (Overfin's)
//	OVERFIN_LIVE_TOKEN     a user's token, and OVERFIN_LIVE_USER its ID: a user
//	                       of its own, made for the test (Overfin sets its
//	                       policy as its one account's)
//	OVERFIN_LIVE_MAP       "<Jellyfin's folder>=<this machine's>", its media's
//	OVERFIN_LIVE_FILE      the file name of the film to play (an H.264 one)
//
// It needs ffprobe on this machine.
func TestPlayLive(t *testing.T) {
	base, key := os.Getenv("OVERFIN_LIVE_JELLYFIN"), os.Getenv("OVERFIN_LIVE_KEY")
	if base == "" || key == "" {
		t.Skip("no test Jellyfin (OVERFIN_LIVE_JELLYFIN, OVERFIN_LIVE_KEY)")
	}
	ctx := context.Background()
	from, to, _ := strings.Cut(os.Getenv("OVERFIN_LIVE_MAP"), "=")
	host := func(p string) string {
		if strings.HasPrefix(p, from+"/") {
			return to + strings.TrimPrefix(p, from)
		}
		return p
	}
	jf, err := jellyfin.New(base, "live-test")
	if err != nil {
		t.Fatal(err)
	}
	admin := jellyfin.Auth{Token: key, DeviceID: "overfin-live-admin"}
	var film jellyfin.VideoFile
	jf.VideoFiles(ctx, admin, func(f jellyfin.VideoFile) bool {
		if filepath.Base(f.Path) == os.Getenv("OVERFIN_LIVE_FILE") {
			film = f
			return false
		}
		return true
	})
	if film.ItemID == "" {
		t.Fatalf("no %s in the test Jellyfin", os.Getenv("OVERFIN_LIVE_FILE"))
	}
	path := host(film.Path)
	pr, err := (&describe.Prober{Frames: true}).Probe(ctx, path)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	facts, streams, languages := copies.Shape(pr, film)
	libs := files.NewLibraries([]string{to})
	f, err := libs.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fp, _ := files.Fingerprint(f)
	size := f.Info.Size()
	f.Close()

	maker := &delivery.PluginMaker{Jellyfin: jf, Admin: func() jellyfin.Auth { return admin }}
	ex := &delivery.Executor{Maker: maker,
		Path:        func(context.Context, string, string) (string, error) { return path, nil },
		Libraries:   func(context.Context) (files.Libraries, error) { return libs, nil },
		Fingerprint: func(_ context.Context, _, _ string, f *files.File) (string, error) { return files.Fingerprint(f) }}
	if err := ex.Clear(ctx); err != nil {
		t.Fatalf("clear: %v", err)
	}
	var st *store.Store
	var tickets *playback.Tickets
	srv, _ := newServerWith(t, options{jellyfin: base, jellyfinUser: os.Getenv("OVERFIN_LIVE_USER"),
		jellyfinToken: os.Getenv("OVERFIN_LIVE_TOKEN"), adminKey: key, delivery: ex, store: &st, tickets: &tickets})
	st.PutCatalog(ctx, []store.CatalogItem{{ID: film.ItemID, Kind: "movie", Name: "Live", SortName: "live", TMDB: "949"}}, 1)
	st.RefreshTitleKeys(ctx)
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "949", Name: "Heat", Year: 1995}, []byte(`{}`), time.Now())
	copyID := "vlive" + strings.Repeat("a", 22)
	if err := st.PutCopy(ctx, store.IndexedCopy{CopyID: copyID, ItemID: film.ItemID, SourceID: film.SourceID, Fingerprint: fp,
		ETag: "liveetagliveetagliveet", Size: size, Facts: facts, Streams: streams, Parts: 1, Languages: languages}); err != nil {
		t.Fatal(err)
	}

	var page titlePage
	if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_949", &page); code != 200 || len(page.Copies) != 1 || page.Encoders == nil {
		t.Fatalf("the title's page: %d %+v", code, page)
	}
	c := page.Copies[0]
	t.Logf("copy: %s %s %dx%d, audio %d, encoders %+v", c.MediaType, c.Video.Codecs, c.Video.Width, c.Video.Height,
		len(c.AudioTracks), *page.Encoders)
	audio := c.AudioTracks[0].ID

	out := t.TempDir()
	// fetch gets a playback's master playlist, its media playlist and its
	// first segment (after the init segment, for fMP4) through Overfin,
	// and probes the segment.
	fetch := func(name, ticket string) map[string]any {
		t.Helper()
		root := "/api/v1/stream/" + ticket + "/"
		code, master := getAs(t, srv, subject, root+"master.m3u8")
		if code != 200 || !strings.Contains(master, delivery.MediaPlaylist) {
			t.Fatalf("%s: master %d %s", name, code, master)
		}
		t.Logf("%s: master\n%s", name, master)
		code, media := getAs(t, srv, subject, root+delivery.MediaPlaylist)
		if code != 200 || strings.Contains(media, "ApiKey") || strings.Contains(media, "api_key") || strings.Contains(media, "http") {
			t.Fatalf("%s: media playlist %d %s", name, code, media)
		}
		var initSeg, first string
		sc := bufio.NewScanner(strings.NewReader(media))
		for sc.Scan() {
			line := sc.Text()
			if u, ok := strings.CutPrefix(line, `#EXT-X-MAP:URI="`); ok {
				initSeg = strings.TrimSuffix(u, `"`)
			}
			if line != "" && !strings.HasPrefix(line, "#") && first == "" {
				first = line
			}
		}
		var body []byte
		for _, n := range []string{initSeg, first} {
			if n == "" {
				continue
			}
			req, _ := http.NewRequest("GET", srv.URL+root+n, nil)
			req.Header.Set("Remote-User", subject)
			res, err := client(srv, subject).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != 200 || len(b) == 0 {
				t.Fatalf("%s: %s: %d", name, n, res.StatusCode)
			}
			body = append(body, b...)
		}
		seg := filepath.Join(out, name)
		os.WriteFile(seg, body, 0o600)
		probe, err := execCommand("ffprobe", "-v", "error", "-show_entries",
			"stream=codec_type,codec_name,width,height,channels", "-of", "json", seg)
		if err != nil {
			t.Fatalf("%s: ffprobe: %v", name, err)
		}
		var p map[string]any
		json.Unmarshal(probe, &p)
		return p
	}
	streamOf := func(p map[string]any, kind string) map[string]any {
		list, _ := p["streams"].([]any)
		for _, e := range list {
			if s, ok := e.(map[string]any); ok && s["codec_type"] == kind {
				return s
			}
		}
		return nil
	}
	stop := func(name, id string, at int64) {
		t.Helper()
		if code, body := post(t, srv, "/api/v1/playback/"+id+"/stop", fmt.Sprintf(`{"positionMs":%d}`, at)); code != 204 {
			t.Errorf("%s: stop %d %s", name, code, body)
		}
	}

	// A remux, fMP4: both streams copied.
	code, a, raw := play(t, srv, `{"titleId":"title_movie_tmdb_949","versionId":"`+copyID+`","audioTrack":"`+audio+`",
		"delivery":"remux","mediaType":"video/mp4","reason":"container","device":"Live"}`)
	if code != 201 || a.Method != "remux" || a.Protocol != "hls" {
		t.Fatalf("remux: %d %s", code, raw)
	}
	p := fetch("remux", a.Ticket)
	if v := streamOf(p, "video"); v == nil || v["codec_name"] != c.Video.Codec || int(v["height"].(float64)) != c.Video.Height {
		t.Errorf("remux: %v", p)
	}
	if code, body := post(t, srv, "/api/v1/playback/"+a.ID+"/progress", `{"positionMs":5000,"paused":true}`); code != 204 {
		t.Errorf("progress: %d %s", code, body)
	}
	stop("remux", a.ID, 5000)

	// The picture converted: H.264 at 480 lines, AAC stereo.
	code, a, raw = play(t, srv, `{"titleId":"title_movie_tmdb_949","delivery":"transcode",
		"transcode":{"videoCodec":"h264","maxHeight":480,"audioCodec":"aac","audioChannels":2},"reason":"bitrate"}`)
	if code != 201 || a.Method != "transcode" {
		t.Fatalf("transcode: %d %s", code, raw)
	}
	p = fetch("transcode", a.Ticket)
	if v, au := streamOf(p, "video"), streamOf(p, "audio"); v == nil || v["codec_name"] != "h264" || int(v["height"].(float64)) > 480 ||
		au == nil || au["codec_name"] != "aac" {
		t.Errorf("transcode: %v", p)
	}
	stop("transcode", a.ID, 0)

	// The audio alone converted: the picture copied, a stream only.
	code, a, raw = play(t, srv, `{"titleId":"title_movie_tmdb_949","delivery":"transcode",
		"transcode":{"videoCodec":"copy","audioCodec":"aac"}}`)
	if code != 201 {
		t.Fatalf("audio only: %d %s", code, raw)
	}
	if n := tickets.Transcodes(); n != 0 {
		t.Errorf("an audio-only conversion took a transcode slot: %d", n)
	}
	p = fetch("audio-only", a.Ticket)
	if v, au := streamOf(p, "video"), streamOf(p, "audio"); v == nil || v["codec_name"] != c.Video.Codec || au == nil || au["codec_name"] != "aac" {
		t.Errorf("audio only: %v", p)
	}
	stop("audio-only", a.ID, 0)

	// The file itself, a range of it.
	code, a, raw = play(t, srv, `{"titleId":"title_movie_tmdb_949","delivery":"file","reason":"as-is"}`)
	if code != 201 || a.Protocol != "file" {
		t.Fatalf("file: %d %s", code, raw)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/stream/"+a.Ticket+"/file", nil)
	req.Header.Set("Remote-User", subject)
	req.Header.Set("Range", "bytes=0-1023")
	res, err := client(srv, subject).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	want, _ := os.ReadFile(path)
	if res.StatusCode != 206 || string(b) != string(want[:1024]) || res.Header.Get("ETag") == "" {
		t.Errorf("file: %d %d %v", res.StatusCode, len(b), res.Header)
	}
	stop("file", a.ID, 0)

	// What isn't made: HDR kept (no GPU), burned subtitles, a codec the
	// server doesn't encode.
	for _, body := range []string{
		`{"titleId":"title_movie_tmdb_949","delivery":"transcode","transcode":{"videoCodec":"hevc","range":"keep"}}`,
		`{"titleId":"title_movie_tmdb_949","delivery":"transcode","transcode":{"videoCodec":"vp9"}}`,
	} {
		if code, a, raw := play(t, srv, body); code != 409 || a.Type != "target-refused" {
			t.Errorf("%s: %d %s", body, code, raw)
		}
	}
	// Every delivery stopped at the plugin: none left.
	if !eventually(t, func() bool { return tickets.Transcodes() == 0 && len(tickets.Live(time.Now())) == 0 }) {
		t.Error("playbacks left")
	}
	if err := ex.Clear(ctx); err != nil {
		t.Errorf("clear at the end: %v", err)
	}
}

func execCommand(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}
