package overfin

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/overfin/server/internal/store"
)

// The backend's events to its gateway, over the link: one JSON object a
// line, numbered where the gateway must not miss one; a heartbeat line keeps
// the stream alive. For now: the logins ended before their time (the
// gateway refuses them at its first check, package edge).
//
//	{"type":"revoked","seq":12,"hash":"<SHA-256 of the login cookie, hex>","expires":1794000000}
//	{"type":"ping"}

// eventsPath is where the gateway follows them, from a revocation number
// (since): those after it, then the new ones as they come.
const eventsPath = "/link/v1/events"

func eventsHandler(db *store.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		lastWrite := time.Time{}
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			revoked, err := db.RevokedLogins(r.Context(), since, 500)
			if err != nil {
				return
			}
			for _, rv := range revoked {
				if enc.Encode(map[string]any{"type": "revoked", "seq": rv.Seq, "hash": hex.EncodeToString(rv.Hash), "expires": rv.Expires}) != nil {
					return
				}
				since = rv.Seq
			}
			if len(revoked) > 0 || time.Since(lastWrite) > 15*time.Second {
				if len(revoked) == 0 && enc.Encode(map[string]string{"type": "ping"}) != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
				lastWrite = time.Now()
			}
			if len(revoked) == 500 {
				continue // more waiting
			}
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
			}
		}
	})
}
