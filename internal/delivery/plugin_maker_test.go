package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/overfin/server/internal/jellyfin"
)

// pluginFake answers the companion plugin's delivery routes as the test sets.
type pluginFake struct {
	mu       sync.Mutex
	encoders string
	start    func(order map[string]any) (int, string)
	ping     int
	calls    []string
	orders   []map[string]any
}

func (f *pluginFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/Overfin/Encoders":
		io.WriteString(w, f.encoders)
	case r.Method == http.MethodPost && r.URL.Path == "/Overfin/Deliveries":
		var o map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &o)
		f.orders = append(f.orders, o)
		status, body := f.start(o)
		w.WriteHeader(status)
		io.WriteString(w, body)
	case strings.HasSuffix(r.URL.Path, "/Ping"):
		w.WriteHeader(f.ping)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (f *pluginFake) callsOf(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

const madeID = "0123456789abcdef0123456789abcdef"

func pluginMaker(t *testing.T, f *pluginFake) *PluginMaker {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	jf, _ := jellyfin.New(srv.URL, "test")
	return &PluginMaker{Jellyfin: jf, Admin: func() jellyfin.Auth { return jellyfin.Auth{Token: "k", DeviceID: "d"} }}
}

// The plugin's encoders: only codecs Overfin knows, each once; keeping
// HDR never offered; asked again only once kept long enough.
func TestPluginMakerEncoders(t *testing.T) {
	f := &pluginFake{encoders: `{"Video":["h264","hevc","h264","vp8","av1"],"Audio":["aac","truehd","opus","aac"],"Hardware":true,"ToneMapping":true}`}
	p := pluginMaker(t, f)
	e, err := p.Encoders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(e.Video, ",") != "h264,hevc,av1" || strings.Join(e.Audio, ",") != "aac,opus" || !e.Hardware || !e.ToneMapping || e.KeepsHDR {
		t.Errorf("encoders: %+v", e)
	}
	p.Encoders(context.Background())
	if n := f.callsOf("GET /Overfin/Encoders"); n != 1 {
		t.Errorf("asked %d times within the keep time", n)
	}
}

// An order as the plugin gets it, and what it made as Overfin reads it;
// each refusal sorted: busy, refused with its code, failing; a playlist
// too large stopped and refused; a copied stream carries no codec.
func TestPluginMakerStart(t *testing.T) {
	ctx := context.Background()
	f := &pluginFake{}
	p := pluginMaker(t, f)
	order := Order{ItemID: madeID, SourceID: madeID, Segments: "mp4", StartMs: 90_000,
		Video: &VideoOrder{Codec: "h264", MaxHeight: 720, MaxBitrate: 4_000_000, ToneMap: true, HeightAsked: true},
		Audio: &AudioOrder{Index: 2, Copy: true}}
	f.start = func(map[string]any) (int, string) {
		return 201, fmt.Sprintf(`{"Id":"%s","VideoCopied":false,"VideoCodec":"h264","Width":1280,"Height":720,"ToneMapped":true,
			"AudioCopied":true,"AudioCodec":"eac3","AudioIndex":2,"Segments":"mp4","Playlist":"#EXTM3U\n"}`, madeID)
	}
	made, err := p.Start(ctx, order)
	if err != nil {
		t.Fatal(err)
	}
	o := f.orders[0]
	v, _ := o["Video"].(map[string]any)
	a, _ := o["Audio"].(map[string]any)
	if o["StartTicks"] != float64(900_000_000) || o["Segments"] != "mp4" || v["MaxHeight"] != float64(720) || v["ToneMap"] != true ||
		v["HeightAsked"] != true || a["Index"] != float64(2) || a["Copy"] != true {
		t.Errorf("the order sent: %v", o)
	}
	if made.ID != madeID || made.Height != 720 || !made.ToneMapped || made.VideoCodec != "h264" || made.AudioCodec != "" ||
		string(made.Playlist) != "#EXTM3U\n" {
		t.Errorf("made: %+v", made)
	}

	for body, want := range map[string]string{
		`{"Refused":"busy"}`:                    "busy maker-busy",
		`{"Refused":"made-differs:height-cap"}`: "refused made-differs:height-cap",
	} {
		f.start = func(map[string]any) (int, string) { return 409, body }
		_, err := p.Start(ctx, order)
		var r *Refusal
		if !errors.As(err, &r) || (strings.HasPrefix(want, "busy") && r.Status != 503) || (strings.HasPrefix(want, "refused") && r.Status != 409) ||
			!strings.HasSuffix(want, r.Why) {
			t.Errorf("%s: %v", body, err)
		}
	}
	f.start = func(map[string]any) (int, string) { return 500, `{}` }
	if _, err := p.Start(ctx, order); err == nil || !strings.Contains(err.Error(), "maker-failed") {
		t.Errorf("the plugin failing: %v", err)
	}
	stops := f.callsOf("DELETE /Overfin/Deliveries/")
	f.start = func(map[string]any) (int, string) {
		huge, _ := json.Marshal(strings.Repeat("#EXTINF:6.0,\nhls1/main/0.ts\n", 100_000))
		return 201, fmt.Sprintf(`{"Id":"%s","Playlist":%s}`, madeID, huge)
	}
	if _, err := p.Start(ctx, order); err == nil || !strings.Contains(err.Error(), "playlist-too-large") {
		t.Errorf("a playlist too large: %v", err)
	}
	if f.callsOf("DELETE /Overfin/Deliveries/") != stops+1 {
		t.Error("a delivery refused for its playlist wasn't stopped")
	}
}

// A ping to a delivery the plugin no longer has: gone.
func TestPluginMakerPing(t *testing.T) {
	f := &pluginFake{ping: http.StatusNotFound}
	p := pluginMaker(t, f)
	if err := p.Ping(context.Background(), madeID); !errors.Is(err, ErrGone) {
		t.Errorf("gone: %v", err)
	}
	f.ping = http.StatusNoContent
	if err := p.Ping(context.Background(), madeID); err != nil {
		t.Errorf("alive: %v", err)
	}
}

// A media server that doesn't answer is unreachable (503 unreachable,
// MSFP Media base 4), not busy: the media can't be reached now.
func TestPluginMakerUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	jf, _ := jellyfin.New(url, "test")
	p := &PluginMaker{Jellyfin: jf, Admin: func() jellyfin.Auth { return jellyfin.Auth{Token: "k", DeviceID: "d"} }}
	_, err := p.Start(context.Background(), Order{ItemID: madeID, SourceID: madeID, Segments: "mp4"})
	var r *Refusal
	if !errors.As(err, &r) || r.Status != 503 || r.Problem != "unreachable" || r.Why != "maker-unreachable" {
		t.Errorf("a media server that doesn't answer: %v", err)
	}
}
