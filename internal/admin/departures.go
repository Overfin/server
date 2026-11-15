package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/overfin/server/internal/store"
)

// maxDepartures caps the titles one ask may hold.
const maxDepartures = 500

// departureJSON is a title an app plans to remove: a film, a season (with
// its number) or a whole series, by TMDB ID; in the answer, with the date
// it's held until when someone wants it.
type departureJSON struct {
	Kind      string `json:"kind"`
	TMDB      string `json:"tmdbId"`
	Season    *int   `json:"season,omitempty"`
	HoldUntil string `json:"holdUntil,omitempty"`
}

// key is the title's key; false when it isn't one.
func (d departureJSON) key() (string, bool) {
	n, err := strconv.Atoi(d.TMDB)
	if err != nil || n < 1 || strconv.Itoa(n) != d.TMDB {
		return "", false
	}
	switch {
	case d.Kind == "movie" && d.Season == nil:
		return "movie/tmdb/" + d.TMDB, true
	case d.Kind == "series" && d.Season == nil:
		return "series/tmdb/" + d.TMDB, true
	case d.Kind == "season" && d.Season != nil && *d.Season >= 0 && *d.Season <= 9999:
		return "series/tmdb/" + d.TMDB + "/s" + strconv.Itoa(*d.Season), true
	}
	return "", false
}

// appDepartures (POST /apps/v1/departures, history scope): an app that
// removes titles asks, before it does, which of them should wait. A title
// someone keeps for later or is partway through is held for a last watch
// (store.Depart), and those people see when it leaves; the app hears only
// until when, never who nor how many (SECURITY.md rule 85). It asks again
// at least hourly about what it still plans to remove, or the hold goes.
func (p *Panel) appDepartures(w http.ResponseWriter, r *http.Request) {
	if p.appKey(w, r, "history") == nil {
		return
	}
	var req struct {
		Titles []departureJSON `json:"titles"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || len(req.Titles) > maxDepartures {
		invalid(w, "titles: at most 500 films, seasons or series")
		return
	}
	keys := make([]string, 0, len(req.Titles))
	for _, t := range req.Titles {
		key, ok := t.key()
		if !ok || t.HoldUntil != "" {
			invalid(w, "kind: movie or series by tmdbId, or season with its number")
			return
		}
		keys = append(keys, key)
	}
	var interests []store.ProfileTitle
	if p.Interests != nil {
		var err error
		if interests, err = p.Interests(r.Context()); err != nil {
			p.fail(w, "reading the watchlists", err)
			return
		}
	}
	wanted, err := p.Store.WantedTitles(r.Context(), interests)
	if err != nil {
		p.fail(w, "reading who wants what", err)
		return
	}
	held, err := p.Store.Depart(r.Context(), keys, wanted, time.Now())
	if err != nil {
		p.fail(w, "holding the titles", err)
		return
	}
	out := struct {
		Titles []departureJSON `json:"titles"`
	}{Titles: make([]departureJSON, 0, len(req.Titles))}
	for i, t := range req.Titles {
		if at, ok := held[keys[i]]; ok {
			t.HoldUntil = at.UTC().Format(time.RFC3339)
		}
		out.Titles = append(out.Titles, t)
	}
	writeJSON(w, http.StatusOK, out)
}
