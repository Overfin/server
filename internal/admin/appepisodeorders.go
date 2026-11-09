package admin

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/overfin/server/internal/tmdb"
)

// appEpisodeOrders (GET /apps/v1/episode-orders?tmdbId=N, catalog scope) says what
// TMDB says of a series' episodes: an app that imports them (Overtide) has
// no TMDB token of its own.
func (p *Panel) appEpisodeOrders(w http.ResponseWriter, r *http.Request) {
	if p.appKey(w, r, "catalog") == nil {
		return
	}
	id, err := strconv.Atoi(r.URL.Query().Get("tmdbId"))
	if err != nil || !tmdb.ValidID(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "tmdbId must be a TMDB ID"})
		return
	}
	if p.EpisodeOrders == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"type": "no-tmdb"})
		return
	}
	orders, err := p.EpisodeOrders(r.Context(), id)
	switch {
	case errors.Is(err, ErrNoTMDB):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"type": "no-tmdb"})
	case errors.Is(err, tmdb.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"type": "not-found"})
	case err != nil:
		p.fail(w, "reading a series' episodes from TMDB", err)
	default:
		writeJSON(w, http.StatusOK, orders)
	}
}
