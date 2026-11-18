package msfp_test

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/overfin/server/msfp"
)

// fakeHost is a host's catalog, playback and media, in memory, for the
// tests: its libraries and titles, each title's copies, each copy's
// tracks; Start admits the method set (direct by default) and serves
// media from files set by name.
type fakeHost struct {
	mu     sync.Mutex
	libs   []msfp.Library
	titles map[string]msfp.Title
	copies map[string][]msfp.Copy
	info   map[string]msfp.CopyInfo
	// method and protocol of Start; startErr fails it; noAdmit starts
	// without Admit; started and stopped note what happened.
	method, protocol string
	startErr         error
	noAdmit          bool
	started          []msfp.StartRequest
	stopped          map[string]msfp.EndReason
	progress         []int64
	handles          int
	// files are what Serve answers, by name ("master.m3u8", "file",
	// "subtitles/s0/0"…); serve, when set, answers instead.
	files  map[string][]byte
	serve  func(ctx context.Context, path msfp.MediaPath, rng *msfp.Range, out msfp.ResponseSink) error
	served []msfp.MediaPath
	// hold makes Start wait until it is closed, so that plays cross.
	hold chan struct{}
}

func newFakeHost() *fakeHost {
	return &fakeHost{titles: map[string]msfp.Title{}, copies: map[string][]msfp.Copy{}, info: map[string]msfp.CopyInfo{},
		stopped: map[string]msfp.EndReason{}, files: map[string][]byte{}, method: "direct", protocol: "hls"}
}

// film adds a film to library lib, with one copy per audio language
// given (handle "<key>#<n>"), each with an audio track and a French
// subtitle track.
func (h *fakeHost) film(lib, key string, audio ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.titles[key] = &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: key, Kind: "movie", Libraries: []string{lib}},
		Audio: audio, Subtitles: []string{"fr"}}
	h.copies[key] = nil
	for i, a := range audio {
		handle := key + "#" + strconv.Itoa(i)
		h.copies[key] = append(h.copies[key], msfp.Copy{Handle: handle, Name: "Copy " + a, Audio: []string{a}, Library: lib})
		h.info[handle] = msfp.CopyInfo{DurationMs: 5_400_000,
			AudioTracks:    []msfp.Track{{ID: "a0", Label: "Audio " + a, Language: a, Channels: 2, Default: true}},
			SubtitleTracks: []msfp.Track{{ID: "s0", Label: "Français", Language: "fr"}, {ID: "s1", Label: "PGS", Language: "fr", Pictures: true}}}
	}
}

func (h *fakeHost) remove(key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.titles, key)
	delete(h.copies, key)
}

func (h *fakeHost) Libraries(context.Context) ([]msfp.Library, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.libs), nil
}

func (h *fakeHost) Titles(_ context.Context, libs []string, after string, limit int) (msfp.TitlePage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var page msfp.TitlePage
	for _, k := range slices.Sorted(maps.Keys(h.titles)) {
		t := h.titles[k]
		if k <= after || !slices.ContainsFunc(t.Common().Libraries, func(l string) bool { return slices.Contains(libs, l) }) {
			continue
		}
		if len(page.Titles) == limit {
			page.More = true
			break
		}
		page.Titles = append(page.Titles, copyTitle(t))
	}
	return page, nil
}

// copyTitle is a copy of a title, so that the engine's changes don't
// reach the host's.
func copyTitle(t msfp.Title) msfp.Title {
	b, _ := msfp.MarshalTitle(t)
	c, _ := msfp.UnmarshalTitle(b)
	return c
}

func (h *fakeHost) Copies(_ context.Context, key string, libs []string) ([]msfp.Copy, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []msfp.Copy
	for _, c := range h.copies[key] {
		if slices.Contains(libs, c.Library) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (h *fakeHost) Describe(_ context.Context, copy string) (msfp.CopyInfo, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.info[copy], nil
}

func (h *fakeHost) Start(_ context.Context, req msfp.StartRequest) (msfp.Started, error) {
	h.mu.Lock()
	method, protocol, err, noAdmit, hold := h.method, h.protocol, h.startErr, h.noAdmit, h.hold
	h.mu.Unlock()
	if hold != nil {
		<-hold
	}
	if err != nil {
		return msfp.Started{}, err
	}
	if !noAdmit {
		if _, err := req.Admit(method); err != nil {
			return msfp.Started{}, err
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handles++
	h.started = append(h.started, req)
	return msfp.Started{Handle: "h" + strconv.Itoa(h.handles), Method: method, Protocol: protocol, MimeType: "video/mp4",
		PositionMs: req.StartMs, Info: &msfp.PlaybackInfo{Container: "mp4"}}, nil
}

func (h *fakeHost) Progress(_ context.Context, _ string, positionMs int64, _ bool) error {
	h.mu.Lock()
	h.progress = append(h.progress, positionMs)
	h.mu.Unlock()
	return nil
}

func (h *fakeHost) Stop(_ context.Context, handle string, why msfp.EndReason) error {
	h.mu.Lock()
	h.stopped[handle] = why
	h.mu.Unlock()
	return nil
}

func (h *fakeHost) stops() map[string]msfp.EndReason {
	h.mu.Lock()
	defer h.mu.Unlock()
	return maps.Clone(h.stopped)
}

// mediaName is a MediaPath's name under the stream, as files keys it.
func mediaName(p msfp.MediaPath) string {
	switch p.Kind {
	case msfp.MediaMaster:
		return "master.m3u8"
	case msfp.MediaFile:
		return "file"
	case msfp.MediaSubtitles:
		return fmt.Sprintf("subtitles/%s/%d", p.Track, p.Part)
	case msfp.MediaPictures:
		return fmt.Sprintf("pictures/%s/%d", p.Track, p.Part)
	case msfp.MediaSheet:
		return fmt.Sprintf("trickplay/%d", p.Sheet)
	}
	return p.Name
}

func (h *fakeHost) Serve(ctx context.Context, _ string, path msfp.MediaPath, rng *msfp.Range, out msfp.ResponseSink) error {
	h.mu.Lock()
	h.served = append(h.served, path)
	serve := h.serve
	body, ok := h.files[mediaName(path)]
	h.mu.Unlock()
	if serve != nil {
		return serve(ctx, path, rng, out)
	}
	if !ok {
		return msfp.ErrNotFound
	}
	return serveBytes(body, rng, out, typeOf(path))
}

func typeOf(p msfp.MediaPath) string {
	switch p.Kind {
	case msfp.MediaMaster, msfp.MediaPlaylist:
		return "application/vnd.apple.mpegurl"
	case msfp.MediaSubtitles:
		return "text/vtt; charset=utf-8"
	case msfp.MediaSheet:
		return "image/jpeg"
	case msfp.MediaPictures:
		return "application/octet-stream"
	}
	return "video/mp4"
}

// serveBytes answers body, or the range of it asked.
func serveBytes(body []byte, rng *msfp.Range, out msfp.ResponseSink, contentType string) error {
	size := int64(len(body))
	if rng == nil {
		out.Start(http.StatusOK, contentType, size, "")
		_, err := out.Write(body)
		return err
	}
	first, last := rng.First, rng.Last
	if rng.Suffix > 0 {
		first, last = max(size-rng.Suffix, 0), size-1
	}
	if last < 0 || last >= size {
		last = size - 1
	}
	if first >= size {
		return msfp.ErrRangeNotSatisfiable
	}
	out.Start(http.StatusPartialContent, contentType, last-first+1, fmt.Sprintf("bytes %d-%d/%d", first, last, size))
	_, err := out.Write(body[first : last+1])
	return err
}

// sink is a ResponseSink in memory.
type sink struct {
	status       int
	contentType  string
	length       int64
	contentRange string
	body         []byte
}

func (s *sink) Start(status int, contentType string, length int64, contentRange string) {
	s.status, s.contentType, s.length, s.contentRange = status, contentType, length, contentRange
}

func (s *sink) Write(b []byte) (int, error) {
	s.body = append(s.body, b...)
	return len(b), nil
}

var _ io.Writer = (*sink)(nil)

// sharing is two engines paired, A sharing h's library "films" with B,
// B wanting it and having read it.
type sharing struct {
	a, b       *server
	h          *fakeHost
	onA, onB   msfp.PeerView // B as A knows it, A as B knows it
	libraryPub string        // the library's public ID
}

func newSharing(t *testing.T, h *fakeHost, cfgA, cfgB msfp.Config) *sharing {
	t.Helper()
	ctx := context.Background()
	if h.libs == nil {
		h.libs = []msfp.Library{{ID: "films", Name: "Films", Kind: "movies"}}
	}
	cfgA.Catalog, cfgA.Playback, cfgA.Media = h, h, h
	s := &sharing{h: h, a: newServer(t, cfgA), b: newServer(t, cfgB)}
	s.onA, s.onB = pair(t, s.a, s.b)
	// B's pins, so that A's calls to it may go, and A's.
	if _, err := s.a.eng.Check(ctx, s.onA.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.b.eng.Check(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, l := range h.libs {
		ids = append(ids, l.ID)
	}
	if err := s.a.eng.SetOffers(ctx, s.onA.ID, ids); err != nil {
		t.Fatal(err)
	}
	libs, err := s.b.eng.TheirLibraries(ctx, s.onB.ID)
	if err != nil || len(libs) != len(ids) {
		t.Fatalf("B's view of A's libraries: %+v, %v", libs, err)
	}
	var want []string
	for _, l := range libs {
		want = append(want, l.ID)
	}
	s.libraryPub = libs[0].ID
	if err := s.b.eng.SetWants(ctx, s.onB.ID, want); err != nil {
		t.Fatal(err)
	}
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	return s
}

// peerTitles is what B keeps of A's titles, by key.
func (s *sharing) peerTitles(t *testing.T) map[string]msfp.Title {
	t.Helper()
	out := map[string]msfp.Title{}
	s.b.store.Read(context.Background(), func(tx msfp.Tx) error {
		titles, err := tx.PeerTitles(s.onB.ID, "", 10_000_000)
		for _, ti := range titles {
			out[ti.Common().Key] = ti
		}
		return err
	})
	return out
}
