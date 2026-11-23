package delivery

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/overfin/server/internal/files"
)

// A file delivery: the copy's file served by Overfin from disk (D3), its
// bytes as they are, by ranges; Jellyfin isn't asked anything. Each
// request opens the file again through the path checks (rule 78), and
// serves it only while it is the very file the playback started with
// (its real path, size and time).

// OpenFile opens a file delivery's file for a request; ErrGone when it
// changed, moved or went since the playback started.
func (e *Executor) OpenFile(ctx context.Context, s *Started) (*files.File, error) {
	if s == nil || s.file == nil || e.Libraries == nil {
		return nil, ErrGone
	}
	libs, err := e.Libraries(ctx)
	if err != nil {
		return nil, err
	}
	f, err := libs.Open(s.file.real)
	if err != nil {
		return nil, ErrGone
	}
	if f.Real != s.file.real || f.Info.Size() != s.file.size || f.Info.ModTime().UnixNano() != s.file.mod {
		f.Close()
		return nil, ErrGone
	}
	return f, nil
}

// oneRange is a Range header of one range of bytes: from a byte, to
// one, both, or the last bytes ("bytes=-500").
var oneRange = regexp.MustCompile(`^bytes=(\d{1,15})?-(\d{1,15})?$`)

// ValidRange says whether a Range header asks one range of bytes, in
// order: never several, nor "bytes=-", nor "bytes=5-3", nor "bytes=-0".
func ValidRange(h string) bool {
	m := oneRange.FindStringSubmatch(h)
	switch {
	case m == nil, m[1] == "" && m[2] == "":
		return false
	case m[1] == "":
		n, _ := strconv.ParseInt(m[2], 10, 64)
		return n > 0
	case m[2] == "":
		return true
	}
	a, _ := strconv.ParseInt(m[1], 10, 64)
	b, _ := strconv.ParseInt(m[2], 10, 64)
	return a <= b
}

// ServeFile answers a request for a file delivery's bytes from f: the
// whole file or the one range asked (416 for any other Range), with the
// copy's media type and etag, until done is closed (the playback over).
// Nothing else of the file goes out: no name, no time.
func ServeFile(w http.ResponseWriter, r *http.Request, s *Started, f *files.File, done <-chan struct{}) {
	if rh := r.Header.Get("Range"); rh != "" && !ValidRange(rh) {
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(f.Info.Size(), 10))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	h := w.Header()
	mediaType := s.Plan.Copy.Facts.MediaType
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	h.Set("Content-Type", mediaType)
	if etag := s.Plan.Copy.ETag; etag != "" {
		h.Set("ETag", strconv.Quote(etag))
	}
	h.Set("Cache-Control", "private, no-store")
	// ServeContent answers ranges, HEAD and If-Range by the etag; no time
	// is given, so none is sent.
	http.ServeContent(w, r, "", time.Time{}, stoppable{f: f, done: done})
}

// stoppable is a file read until done closes.
type stoppable struct {
	f    io.ReadSeeker
	done <-chan struct{}
}

func (s stoppable) Read(p []byte) (int, error) {
	select {
	case <-s.done:
		return 0, errOver
	default:
	}
	return s.f.Read(p)
}

func (s stoppable) Seek(offset int64, whence int) (int64, error) { return s.f.Seek(offset, whence) }
