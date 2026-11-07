package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// genreJSON is one of TMDB's genres, and whether the server leaves its
// titles out.
type genreJSON struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Left bool   `json:"left"`
}

// genresJSON are TMDB's genres of films and of series, for the panel to
// choose those left out (store.GenresLeftOut).
type genresJSON struct {
	Movie  []genreJSON `json:"movie"`
	Series []genreJSON `json:"series"`
}

// genresState lists TMDB's genres of each kind with those left out; a
// genre left out that TMDB no longer lists still shows, by number, so it
// can be taken back.
func (p *Panel) genresState(ctx context.Context) (genresJSON, error) {
	if p.TMDBGenres == nil {
		return genresJSON{}, ErrNoTMDB
	}
	left, err := p.Store.GenresLeftOut(ctx)
	if err != nil {
		return genresJSON{}, err
	}
	var out genresJSON
	for _, kind := range []string{"movie", "series"} {
		all, err := p.TMDBGenres(ctx, kind)
		if err != nil {
			return genresJSON{}, fmt.Errorf("%w: %w", errGenres, err)
		}
		list := []genreJSON{}
		for _, g := range all {
			list = append(list, genreJSON{ID: g.ID, Name: g.Name, Left: slices.Contains(left.Of(kind), g.ID)})
		}
		for _, id := range left.Of(kind) {
			if !slices.ContainsFunc(all, func(g tmdb.Genre) bool { return g.ID == id }) {
				list = append(list, genreJSON{ID: id, Name: "Genre " + strconv.Itoa(id), Left: true})
			}
		}
		if kind == "movie" {
			out.Movie = list
		} else {
			out.Series = list
		}
	}
	return out, nil
}

// errGenres: TMDB didn't list its genres.
var errGenres = errors.New("admin: TMDB's genres")

func (p *Panel) genres(w http.ResponseWriter, r *http.Request, s *session) {
	out, err := p.genresState(r.Context())
	p.writeGenres(w, out, err)
}

// setGenres changes the genres left out: TMDB's numbers, for each kind.
func (p *Panel) setGenres(w http.ResponseWriter, r *http.Request, s *session) {
	var req store.GenresLeftOut
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || len(req.Movie) > 100 || len(req.Series) > 100 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "not a valid request"})
		return
	}
	for _, id := range slices.Concat(req.Movie, req.Series) {
		if id < 1 || id > 1_000_000 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "not a TMDB genre"})
			return
		}
	}
	ctx := r.Context()
	err := p.Store.SetGenresLeftOut(ctx, req)
	if err == nil {
		p.Logger.Info("the genres left out changed in the control panel", "films", req.Movie, "series", req.Series, "by", s.logID)
	}
	var out genresJSON
	if err == nil {
		out, err = p.genresState(ctx)
	}
	p.writeGenres(w, out, err)
}

func (p *Panel) writeGenres(w http.ResponseWriter, out genresJSON, err error) {
	switch {
	case errors.Is(err, ErrNoTMDB):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"type": "unavailable", "detail": "TMDB itself is needed to list its genres"})
	case errors.Is(err, errGenres):
		p.Logger.Warn("TMDB's genres", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"type": "upstream", "detail": "TMDB didn't list its genres; try again in a moment"})
	case err != nil:
		p.Logger.Error("reading the genres left out failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"type": "internal", "detail": "Overfin's log says why"})
	default:
		writeJSON(w, http.StatusOK, out)
	}
}
