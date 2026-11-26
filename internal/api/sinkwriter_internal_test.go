package api

import (
	"bytes"
	"errors"
	"net/http"
	"testing"

	"github.com/msfp-io/msfp-go"
)

// sink is the engine's side of an answer: how it was started, and its
// bytes.
type sink struct {
	starts       int
	status       int
	contentType  string
	length       int64
	contentRange string
	body         bytes.Buffer
}

func (s *sink) Start(status int, contentType string, length int64, contentRange string) {
	s.starts++
	s.status, s.contentType, s.length, s.contentRange = status, contentType, length, contentRange
}

func (s *sink) Write(b []byte) (int, error) { return s.body.Write(b) }

// What this server's own serving writes, as the engine gets it: a
// success starts the answer once, with its type, length and range; a
// failure starts nothing, its body never sent, and is the engine's error
// for MSFP's statuses (a range that can't be served, busy, rate limited),
// else not found.
func TestSinkWriter(t *testing.T) {
	s := &sink{}
	w := &sinkWriter{sink: s, header: http.Header{}}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Content-Length", "5")
	w.Header().Set("Content-Range", "bytes 0-4/10")
	w.WriteHeader(http.StatusPartialContent)
	w.WriteHeader(http.StatusOK) // a second one changes nothing
	w.Write([]byte("hello"))
	if s.starts != 1 || s.status != 206 || s.contentType != "video/mp2t" || s.length != 5 || s.contentRange != "bytes 0-4/10" ||
		s.body.String() != "hello" || w.result() != nil {
		t.Errorf("a success: %+v %q %v", s, s.body.String(), w.result())
	}
	// Written without a status: 200, length unknown.
	s = &sink{}
	w = &sinkWriter{sink: s, header: http.Header{}}
	w.Write([]byte("x"))
	if s.status != 200 || s.length != -1 || w.result() != nil {
		t.Errorf("no status: %+v", s)
	}
	for status, want := range map[int]error{
		http.StatusRequestedRangeNotSatisfiable: msfp.ErrRangeNotSatisfiable,
		http.StatusServiceUnavailable:           msfp.ErrServerBusy,
		http.StatusTooManyRequests:              msfp.ErrServerBusy,
		http.StatusNotFound:                     msfp.ErrNotFound,
		http.StatusForbidden:                    msfp.ErrNotFound,
		http.StatusInternalServerError:          msfp.ErrNotFound,
	} {
		s := &sink{}
		w := &sinkWriter{sink: s, header: http.Header{}}
		w.WriteHeader(status)
		w.Write([]byte(`{"detail":"/media/films/x.mkv"}`))
		if s.starts != 0 || s.body.Len() != 0 || !errors.Is(w.result(), want) {
			t.Errorf("%d: started %d, sent %q, %v", status, s.starts, s.body.String(), w.result())
		}
	}
	// Nothing written at all: not found.
	if err := (&sinkWriter{sink: &sink{}, header: http.Header{}}).result(); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("nothing written: %v", err)
	}
}
