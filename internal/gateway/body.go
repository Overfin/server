package gateway

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"time"
)

// The API takes small bodies only (the backend caps them at 64 KiB); the
// gateway reads one whole, within bodyTime, before the request takes one
// of the link's streams. A visitor sending a byte now and then holds its
// own connection only, never a stream every visitor shares.
const maxBody = 64 << 10

var bodyTime = 20 * time.Second

func wholeBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
			next.ServeHTTP(w, r)
			return
		}
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(bodyTime))
		// One byte more than the backend takes: it refuses that as it
		// would any body too large.
		data, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		_ = rc.SetReadDeadline(time.Time{})
		if err != nil {
			// Out of time (net/http then cancels the request's context
			// too), or the visitor left: nobody to answer then.
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				return
			}
			w.Header().Set("Content-Type", "application/problem+json")
			if r.ProtoMajor == 1 {
				w.Header().Set("Connection", "close") // HTTP/2 would close the visitor's other streams too
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"invalid-request","title":"The request's body didn't arrive","status":400}` + "\n"))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
		r.ContentLength = int64(len(data))
		r.Header.Del("Transfer-Encoding")
		next.ServeHTTP(w, r)
	})
}
