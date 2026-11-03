package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/overfin/server/internal/catalog"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// A series' episode orders in Identification: TMDB's own and the others on
// TMDB, each compared with the files. Overfin only says what fits; the admin
// chooses.

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
	// Fits: each season the files are in has that order's episode count,
	// and each file a place in it (specials aside).
	Fits bool `json:"fits"`
	// SeasonsFit counts the files' seasons the order has with that count.
	SeasonsFit int `json:"seasonsFit"`
	// Named counts the files whose names hold the name of the episode the
	// order has at their numbers.
	Named int `json:"named"`
}

// groupIDPattern is what an episode group's ID looks like.
var groupIDPattern = regexp.MustCompile(`^[0-9a-f]{1,64}$`)

// seriesOrders lists a series' orders, compared with its files: those that
// fit first, then those whose episode names the files hold most.
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
		writeJSON(w, http.StatusOK, map[string]any{"current": "", "orders": []seriesOrderJSON{}, "seasons": 0})
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
	slices.SortStableFunc(out, func(a, b seriesOrderJSON) int {
		if a.Fits != b.Fits {
			if a.Fits {
				return -1
			}
			return 1
		}
		return b.Named - a.Named
	})
	writeJSON(w, http.StatusOK, map[string]any{"current": current, "orders": out, "seasons": fileSeasons(files)})
}

// fileSeasons counts the seasons the files are in, specials aside.
func fileSeasons(files []store.SeriesFile) int {
	seen := map[int]bool{}
	for _, f := range files {
		if f.Season > 0 {
			seen[f.Season] = true
		}
	}
	return len(seen)
}

// compareOrder compares an order with a series' files.
func compareOrder(o catalog.SeriesOrder, files []store.SeriesFile) seriesOrderJSON {
	j := seriesOrderJSON{ID: o.ID, Name: o.Name, Type: o.Type, Seasons: []orderSeasonJSON{}}
	for _, se := range o.Seasons {
		j.Seasons = append(j.Seasons, orderSeasonJSON{Number: se.Number, Episodes: len(se.Episodes)})
	}
	slices.SortFunc(j.Seasons, func(a, b orderSeasonJSON) int { return a.Number - b.Number })
	have := map[int]map[int]bool{}
	for _, f := range files {
		if have[f.Season] == nil {
			have[f.Season] = map[int]bool{}
		}
		have[f.Season][f.Episode] = true
		if se := o.Seasons[f.Season]; se != nil {
			at := slices.IndexFunc(se.Episodes, func(e tmdb.Episode) bool { return e.Number == f.Episode })
			if at >= 0 && holdsName(f.Name, se.Episodes[at].Name) {
				j.Named++
			}
		}
	}
	j.Fits = len(files) > 0
	for season, episodes := range have {
		if season == 0 {
			continue
		}
		se := o.Seasons[season]
		fit := se != nil && len(se.Episodes) == len(episodes)
		if fit {
			for number := range episodes {
				fit = fit && slices.ContainsFunc(se.Episodes, func(e tmdb.Episode) bool { return e.Number == number })
			}
		}
		if fit {
			j.SeasonsFit++
		} else {
			j.Fits = false
		}
	}
	return j
}

// holdsName says whether a file's name holds an episode's, letters and digits
// compared without case. A name of a few letters says nothing.
func holdsName(file, episode string) bool {
	e := foldName(episode)
	return len(e) >= 4 && strings.Contains(foldName(file), e)
}

func foldName(s string) string {
	return strings.Map(func(c rune) rune {
		if unicode.IsLetter(c) || unicode.IsDigit(c) {
			return unicode.ToLower(c)
		}
		return -1
	}, s)
}

// chooseSeriesOrder numbers a series as an order does ("": TMDB's own).
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
