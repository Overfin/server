package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// frameJPEG is a 64x36 frame: flat at grey level v, or a left-to-right
// ramp from black to white.
func frameJPEG(t *testing.T, v uint8, ramp bool) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 64, 36))
	for y := range 36 {
		for x := range 64 {
			g := v
			if ramp {
				g = uint8(x * 255 / 63)
			}
			img.SetGray(x, y, color.Gray{Y: g})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// framesJellyfin is a fake Jellyfin with Overfin's plugin for frames: a
// frame by the moment asked (seconds, rounded down), an item's intro, and
// the moments asked, in order.
type framesJellyfin struct {
	mu     sync.Mutex
	frames map[int][]byte // by second; missing: 404
	intro  *jellyfin.Segment
	asked  []int
}

func (j *framesJellyfin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/MediaSegments/"):
		var items []jellyfin.Segment
		if j.intro != nil {
			items = append(items, *j.intro)
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": items})
	case strings.HasPrefix(r.URL.Path, "/Overfin/Frame/"):
		at, _ := strconv.ParseFloat(r.URL.Query().Get("at"), 64)
		j.mu.Lock()
		j.asked = append(j.asked, int(at))
		f, ok := j.frames[int(at)]
		j.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(f)
	default:
		http.NotFound(w, r)
	}
}

// framesDB reads each test store's database, read only.
var framesDB = map[*store.Store]*sql.DB{}

func framesSetup(t *testing.T, j *framesJellyfin) (*EpisodeFrames, *store.Store) {
	t.Helper()
	srv := httptest.NewServer(j)
	t.Cleanup(srv.Close)
	jf, _ := jellyfin.New(srv.URL, "test")
	box, _ := seal.New(bytes.Repeat([]byte{3}, 32))
	path := filepath.Join(t.TempDir(), "t.db")
	st, err := store.Open(context.Background(), path, box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	framesDB[st] = db
	return &EpisodeFrames{
		Store: st, Jellyfin: jf, Images: images.New(jf, 1<<20),
		Admin:   func() jellyfin.Auth { return jellyfin.Auth{Token: "admin", DeviceID: "a"} },
		Service: func(context.Context) (jellyfin.Auth, error) { return jellyfin.Auth{Token: "svc", DeviceID: "s"}, nil },
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)), Every: time.Hour,
	}, st
}

// The moments: 30 s after a marked intro, else a fifth of the way; then
// a third and half. An intro ending too late, an unknown length and no
// service account each fall back.
func TestFrameMoments(t *testing.T) {
	ctx := context.Background()
	j := &framesJellyfin{}
	f, _ := framesSetup(t, j)
	c := store.FrameCandidate{TitleKey: "series/tmdb/1/e2", Item: id(7), Runtime: 30 * time.Minute}
	want := func(got []time.Duration, w ...time.Duration) {
		t.Helper()
		if len(got) != len(w) {
			t.Fatalf("moments %v, want %v", got, w)
		}
		for i := range w {
			if got[i] != w[i] {
				t.Fatalf("moments %v, want %v", got, w)
			}
		}
	}
	want(f.moments(ctx, c), 6*time.Minute, 10*time.Minute, 15*time.Minute)

	j.intro = &jellyfin.Segment{Type: "Intro", StartTicks: 0, EndTicks: int64(90 * time.Second / 100)}
	want(f.moments(ctx, c), 2*time.Minute, 10*time.Minute, 15*time.Minute)

	// An intro ending 30 s or less before the end: a fifth of the way.
	j.intro.EndTicks = int64((30*time.Minute - 30*time.Second) / 100)
	want(f.moments(ctx, c), 6*time.Minute, 10*time.Minute, 15*time.Minute)

	// Another kind of segment isn't an intro.
	j.intro = &jellyfin.Segment{Type: "Recap", EndTicks: int64(90 * time.Second / 100)}
	want(f.moments(ctx, c), 6*time.Minute, 10*time.Minute, 15*time.Minute)

	// Unknown length: a 20-minute episode's.
	j.intro = nil
	c.Runtime = 0
	want(f.moments(ctx, c), 4*time.Minute, 400*time.Second, 10*time.Minute)

	// No service account: no intro looked for.
	j.intro = &jellyfin.Segment{Type: "Intro", EndTicks: int64(90 * time.Second / 100)}
	f.Service = func(context.Context) (jellyfin.Auth, error) { return jellyfin.Auth{}, errors.New("no account") }
	c.Runtime = 30 * time.Minute
	want(f.moments(ctx, c), 6*time.Minute, 10*time.Minute, 15*time.Minute)
}

// frameKept reads the frame kept for an episode: its data, nil when none.
func frameKept(t *testing.T, st *store.Store, key string) (data []byte, tried bool) {
	t.Helper()
	var d []byte
	err := framesDB[st].QueryRow(`SELECT data FROM episode_frames WHERE title_key = ?`, key).Scan(&d)
	if err != nil {
		return nil, false
	}
	return d, true
}

// One episode's frame: the first good one is kept; a dark or flat one
// is passed over; with none good, the one of most contrast; with none
// at all, the try is noted with no frame.
func TestFrameChoice(t *testing.T) {
	ctx := context.Background()
	black, grey, ramp := frameJPEG(t, 2, false), frameJPEG(t, 128, false), frameJPEG(t, 0, true)
	c := store.FrameCandidate{TitleKey: "series/tmdb/1/e2", Item: id(7), Runtime: 30 * time.Minute}

	// Black at a fifth, the ramp at a third: the ramp, and half isn't asked.
	j := &framesJellyfin{frames: map[int][]byte{360: black, 600: ramp, 900: grey}}
	f, st := framesSetup(t, j)
	ok, err := f.one(ctx, c)
	if err != nil || !ok {
		t.Fatalf("one: %v %v", ok, err)
	}
	if got := j.asked; len(got) != 2 || got[0] != 360 || got[1] != 600 {
		t.Errorf("moments asked %v, want [360 600]", got)
	}
	if d, _ := frameKept(t, st, c.TitleKey); len(d) == 0 {
		t.Error("no frame kept")
	}

	// Only dark and flat frames: the one of most contrast is kept all the
	// same (flat grey and black are both flat; the brighter edge of
	// JPEG's blocks aside, either may win), and all three are asked.
	j2 := &framesJellyfin{frames: map[int][]byte{360: black, 600: grey, 900: black}}
	f2, st2 := framesSetup(t, j2)
	if ok, err := f2.one(ctx, c); err != nil || !ok {
		t.Fatalf("one, nothing good: %v %v", ok, err)
	}
	if len(j2.asked) != 3 {
		t.Errorf("moments asked %v, want all three", j2.asked)
	}
	if d, _ := frameKept(t, st2, c.TitleKey); len(d) == 0 {
		t.Error("the best of the bad ones wasn't kept")
	}

	// No frame at all (the plugin fails each time): noted with none.
	j3 := &framesJellyfin{frames: map[int][]byte{}}
	f3, st3 := framesSetup(t, j3)
	if ok, err := f3.one(ctx, c); err != nil || ok {
		t.Fatalf("one, no frame: %v %v", ok, err)
	}
	if d, tried := frameKept(t, st3, c.TitleKey); !tried || d != nil {
		t.Errorf("no frame: tried %v, data %d bytes", tried, len(d))
	}

	// Garbage from the plugin is passed over like a failure.
	j4 := &framesJellyfin{frames: map[int][]byte{360: []byte("not an image"), 600: ramp}}
	f4, _ := framesSetup(t, j4)
	if ok, err := f4.one(ctx, c); err != nil || !ok {
		t.Fatalf("one, garbage first: %v %v", ok, err)
	}
}
