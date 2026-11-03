package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"

	"github.com/overfin/server/internal/catalog"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// A series' episode orders in Identification: TMDB's own and the others on
// TMDB. An order only arranges the episodes the files already are: Overfin
// says how many of them each one lists, and the admin chooses.

// orderSeasonJSON is one of an order's seasons, with its episode count.
type orderSeasonJSON struct {
	Number   int `json:"number"`
	Episodes int `json:"episodes"`
}

type seriesOrderJSON struct {
	ID      string            `json:"id"` // "" for TMDB's own
	Name    string            `json:"name"`
	Type    int               `json:"type,omitempty"`
	Seasons []orderSeasonJSON `json:"seasons"`
	// Has counts the episodes here that the order lists: those it lacks
	// wouldn't be shown in its seasons.
	Has int `json:"has"`
}

// groupIDPattern is what an episode group's ID looks like.
var groupIDPattern = regexp.MustCompile(`^[0-9a-f]{1,64}$`)

// seriesOrders lists a series' orders, TMDB's own first, each with how many
// of the episodes here it lists.
func (p *Panel) seriesOrders(w http.ResponseWriter, r *http.Request, _ *session) {
	id := r.PathValue("id")
	if !itemIDPattern.MatchString(id) {
		notFound(w, "No such series in the libraries")
		return
	}
	series, files, err := p.Store.SeriesFiles(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		notFound(w, "No such series in the libraries")
		return
	case err != nil:
		p.fail(w, "listing a series' files", err)
		return
	}
	n, err := strconv.Atoi(series)
	if err != nil || !tmdb.ValidID(n) || p.ReadSeriesOrders == nil {
		writeJSON(w, http.StatusOK, map[string]any{"current": "", "orders": []seriesOrderJSON{}, "episodes": 0, "unplaced": 0})
		return
	}
	current, _, err := p.Store.SeriesOrder(r.Context(), series)
	if err != nil {
		p.fail(w, "reading a series' order", err)
		return
	}
	orders, err := p.ReadSeriesOrders(r.Context(), n)
	switch {
	case errors.Is(err, catalog.ErrNoSource):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"type": "no-tmdb"})
		return
	case errors.Is(err, tmdb.ErrNotFound):
		notFound(w, "TMDB doesn't know this series")
		return
	case err != nil:
		p.fail(w, "reading a series' orders from TMDB", err)
		return
	}
	out := make([]seriesOrderJSON, 0, len(orders))
	for _, o := range orders {
		out = append(out, compareOrder(o, files))
	}
	writeJSON(w, http.StatusOK, map[string]any{"current": current, "orders": out, "episodes": knownEpisodes(files), "unplaced": len(files) - knownFiles(files)})
}

// knownEpisodes counts the different episodes the files are.
func knownEpisodes(files []store.SeriesFile) int {
	seen := map[int]bool{}
	for _, f := range files {
		if f.EpisodeID > 0 {
			seen[f.EpisodeID] = true
		}
	}
	return len(seen)
}

// knownFiles counts the files TMDB's own order has an episode for.
func knownFiles(files []store.SeriesFile) int {
	n := 0
	for _, f := range files {
		if f.EpisodeID > 0 {
			n++
		}
	}
	return n
}

// compareOrder says how many of a series' episodes here an order lists.
func compareOrder(o catalog.SeriesOrder, files []store.SeriesFile) seriesOrderJSON {
	j := seriesOrderJSON{ID: o.ID, Name: o.Name, Type: o.Type, Seasons: []orderSeasonJSON{}}
	listed := map[int]bool{}
	for _, se := range o.Seasons {
		j.Seasons = append(j.Seasons, orderSeasonJSON{Number: se.Number, Episodes: len(se.Episodes)})
		for _, e := range se.Episodes {
			listed[e.ID] = true
		}
	}
	slices.SortFunc(j.Seasons, func(a, b orderSeasonJSON) int { return a.Number - b.Number })
	seen := map[int]bool{}
	for _, f := range files {
		if f.EpisodeID > 0 && listed[f.EpisodeID] && !seen[f.EpisodeID] {
			seen[f.EpisodeID] = true
			j.Has++
		}
	}
	return j
}

// chooseSeriesOrder shows a series as an order has it ("": TMDB's own).
func (p *Panel) chooseSeriesOrder(w http.ResponseWriter, r *http.Request, s *session) {
	id := r.PathValue("id")
	var req struct {
		Group string `json:"group"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || !itemIDPattern.MatchString(id) || (req.Group != "" && !groupIDPattern.MatchString(req.Group)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "not an episode order"})
		return
	}
	series, _, err := p.Store.SeriesFiles(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "No such series in the libraries")
		return
	}
	if err != nil {
		p.fail(w, "reading a series", err)
		return
	}
	n, convErr := strconv.Atoi(series)
	if convErr != nil || !tmdb.ValidID(n) {
		notFound(w, "This series has no TMDB title")
		return
	}
	if p.ChooseSeriesOrder == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"type": "no-tmdb"})
		return
	}
	switch err := p.ChooseSeriesOrder(r.Context(), n, req.Group); {
	case errors.Is(err, catalog.ErrNoSource):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"type": "no-tmdb"})
		return
	case errors.Is(err, tmdb.ErrNotFound):
		notFound(w, "TMDB doesn't have this order")
		return
	case err != nil:
		p.fail(w, "choosing a series' episode order", err)
		return
	}
	p.Logger.Info("a series' episode order was chosen in the control panel", "item", id, "group", req.Group, "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}
