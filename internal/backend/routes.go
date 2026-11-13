package backend

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// maxSpeedBytes bounds one speed measurement.
const maxSpeedBytes = 10 << 30

// Routes are what the backend answers its gateway over the link, for now:
// its state, and a stream of bytes to measure the link with (the gateway
// alone can ask: the link is the backend's only door from outside).
func Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /link/v1/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"version": Version})
	})
	mux.HandleFunc("GET /link/v1/speed", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
		if err != nil || n <= 0 || n > maxSpeedBytes {
			http.Error(w, "bytes: 1 to 10 GiB", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
		buf := make([]byte, 64<<10)
		for n > 0 {
			k := min(n, int64(len(buf)))
			if _, err := w.Write(buf[:k]); err != nil {
				return
			}
			n -= k
		}
	})
	return mux
}
