package overfin

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/store"
)

// The backend's events to its gateway, over the link: one JSON object a
// line, numbered where the gateway must not miss one; a heartbeat line keeps
// the stream alive:
//   - the logins ended before their time (the gateway refuses them at its
//     first check, package edge);
//   - new titles' artwork, for the gateway to fetch ahead into its cache
//     (best effort: from when the stream starts);
//   - a change to what a profile may see (migration 107): the gateway forgets
//     every access answer its image cache keeps. It forgets them too each
//     time this stream starts, so a change during a break isn't missed.
//
//	{"type":"revoked","seq":12,"hash":"<SHA-256 of the login cookie, hex>","expires":1794000000}
//	{"type":"new","images":["/api/v1/images/tmdbart_poster_…_jpg", …]}
//	{"type":"access"}
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
		_, titlesAt, err := db.NewTitles(r.Context(), -1, 0)
		if err != nil {
			return
		}
		access, err := db.AccessChange(r.Context())
		if err != nil {
			return
		}
		for n := 0; ; n++ {
			if n%10 == 0 {
				var paths []string
				if paths, titlesAt, err = newArtwork(r.Context(), db, titlesAt); err != nil {
					return
				}
				if len(paths) > 0 {
					if enc.Encode(map[string]any{"type": "new", "images": paths}) != nil {
						return
					}
					if flusher != nil {
						flusher.Flush()
					}
				}
			}
			changed, err := db.AccessChange(r.Context())
			if err != nil {
				return
			}
			if changed != access {
				access = changed
				if enc.Encode(map[string]string{"type": "access"}) != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
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

// newArtwork is the artwork of the titles the catalog changed after since
// (those still there): their image routes, without a size (the gateway
// fetches the widths it has seen asked for).
func newArtwork(ctx context.Context, db *store.Store, since int64) ([]string, int64, error) {
	keys, last, err := db.NewTitles(ctx, since, 200)
	if err != nil || len(keys) == 0 {
		return nil, last, err
	}
	infos, err := db.TitleInfos(ctx, keys, time.Now().UTC().Format("2006-01-02"), "")
	if err != nil {
		return nil, since, err
	}
	var paths []string
	for _, t := range infos {
		for typ, p := range map[string]string{"poster": t.Poster, "backdrop": t.Backdrop, "logo": t.Logo} {
			if id := images.ArtworkID(typ, p); id != "" {
				paths = append(paths, "/api/v1/images/"+id)
			}
		}
	}
	return paths, last, nil
}

// prefetchArtwork is the backend's answer to its gateway's prefetch
// (api.Library.PrefetchArtwork), once the library is built.
var prefetchArtwork atomic.Pointer[http.HandlerFunc]

func prefetchHandler(w http.ResponseWriter, r *http.Request) {
	if h := prefetchArtwork.Load(); h != nil {
		(*h)(w, r)
		return
	}
	http.NotFound(w, r)
}
