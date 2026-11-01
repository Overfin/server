package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/wishes"
)

// The wishes, for the app that fetches them (Overswell): the titles people
// wished for, in the order Overfin's turns give (internal/wishes), never
// who wished them or how many did (SECURITY.md rule 40). The app says back
// what became of each title; a search spends the turn of the account whose
// wish it was.

// appWishesLimit is how many titles one read gives at most.
const appWishesLimit = 200

type appWishJSON struct {
	// Kind is movie, series (every season out now) or season.
	Kind   string `json:"kind"`
	Name   string `json:"name,omitempty"`
	TMDB   string `json:"tmdbId"`
	TVDB   string `json:"tvdbId,omitempty"`
	Season *int   `json:"season,omitempty"`
}

// toOverswell reports whether wishes go to an app (Overswell): with Seerr,
// Overfin hands them over itself, and with none, there are none.
func (p *Panel) toOverswell() bool {
	return p.Features.Features().Wishes.To == config.WishesToOverswell
}

// order reads the turns: the titles to fetch, next first.
func (p *Panel) wishOrder(r *http.Request, limit int) ([]wishes.Next, error) {
	ctx := r.Context()
	all, err := p.Store.AllWishes(ctx)
	if err != nil {
		return nil, err
	}
	states, err := p.Store.WishStates(ctx)
	if err != nil {
		return nil, err
	}
	turns, err := p.Store.WishTurns(ctx)
	if err != nil {
		return nil, err
	}
	return wishes.Order(all, states, turns, p.clock(), limit), nil
}

// appWishes (GET /apps/v1/wishes?limit=N) lists the titles to fetch, next
// first: what can be tried now, waiting for nothing else.
func (p *Panel) appWishes(w http.ResponseWriter, r *http.Request) {
	if p.appKey(w, r, "wishes") == nil {
		return
	}
	limit := appWishesLimit
	for name := range r.URL.Query() {
		if name != "limit" {
			invalid(w, "unknown parameter: "+name)
			return
		}
	}
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > appWishesLimit {
			invalid(w, "limit: 1 to "+strconv.Itoa(appWishesLimit))
			return
		}
		limit = n
	}
	out := []appWishJSON{}
	if p.toOverswell() {
		order, err := p.wishOrder(r, limit)
		if err != nil {
			p.fail(w, "reading the wishes", err)
			return
		}
		keys := make([]string, len(order))
		read := []string{}
		for i, n := range order {
			keys[i] = n.Title
			read = append(read, n.Title)
			// A season's TheTVDB ID is its series'.
			if ref, ok := store.ParseTitleKey(n.Title); ok && ref.Kind == "season" {
				read = append(read, ref.Series().Key())
			}
		}
		infos, err := p.Store.TitleInfos(r.Context(), read, p.clock().UTC().Format(time.DateOnly), "")
		if err != nil {
			p.fail(w, "reading the wished titles", err)
			return
		}
		for _, key := range keys {
			ref, ok := store.ParseTitleKey(key)
			if !ok {
				continue
			}
			j := appWishJSON{Kind: ref.Kind, TMDB: ref.TMDB()}
			if ref.Kind == "season" {
				j.Season = &ref.Season
			}
			if t := infos[key]; t != nil {
				j.Name = t.Name
			}
			switch ref.Kind {
			case "series":
				if t := infos[key]; t != nil {
					j.TVDB = t.Details.TVDB
				}
			case "season":
				if t := infos[ref.Series().Key()]; t != nil {
					j.TVDB = t.Details.TVDB
				}
			}
			out = append(out, j)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"titles": out, "generatedAt": p.clock().UTC().Format(time.RFC3339)})
}

// appWishStatus (POST /apps/v1/wishes/status) is the app saying what became
// of a wished title: downloading, notFound (with nextTry, when it tries
// again), unreleased (with releaseDate, if known) or waiting (it gave up on
// it for now). A search, found or not, spends the turn of the account whose
// wish it was; waiting for a release spends nothing.
func (p *Panel) appWishStatus(w http.ResponseWriter, r *http.Request) {
	if p.appKey(w, r, "wishes") == nil {
		return
	}
	var req struct {
		Kind        string `json:"kind"`
		TMDB        string `json:"tmdbId"`
		Season      *int   `json:"season"`
		State       string `json:"state"`
		NextTry     string `json:"nextTry"`
		ReleaseDate string `json:"releaseDate"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		invalid(w, "not a wish's status")
		return
	}
	n, err := strconv.Atoi(req.TMDB)
	if err != nil || n < 1 || strconv.Itoa(n) != req.TMDB {
		invalid(w, "tmdbId: a TMDB ID")
		return
	}
	var key string
	switch {
	case req.Kind == "movie" && req.Season == nil:
		key = "movie/tmdb/" + req.TMDB
	case req.Kind == "series" && req.Season == nil:
		key = "series/tmdb/" + req.TMDB
	case req.Kind == "season" && req.Season != nil && *req.Season >= 0 && *req.Season <= 9999:
		key = "series/tmdb/" + req.TMDB + "/s" + strconv.Itoa(*req.Season)
	default:
		invalid(w, "kind: movie, series, or season with its number")
		return
	}
	st := store.WishState{Title: key}
	spends := false
	switch req.State {
	case "downloading":
		st.State, spends = store.WishDownloading, true
	case "notFound":
		st.State, spends = store.WishNotFound, true
		if req.NextTry != "" {
			at, err := time.Parse(time.RFC3339, req.NextTry)
			if err != nil {
				invalid(w, "nextTry: an RFC 3339 time")
				return
			}
			st.NextTry = at
		}
	case "unreleased":
		st.State = store.WishUnreleased
		if req.ReleaseDate != "" {
			if _, err := time.Parse(time.DateOnly, req.ReleaseDate); err != nil {
				invalid(w, "releaseDate: YYYY-MM-DD")
				return
			}
			st.ReleaseOn = req.ReleaseDate
		}
	case "waiting":
	default:
		invalid(w, "state: downloading, notFound, unreleased or waiting")
		return
	}
	ctx := r.Context()
	wished, err := p.Store.WishedTitles(ctx)
	if err != nil {
		p.fail(w, "reading the wishes", err)
		return
	}
	found := false
	for _, t := range wished {
		if t == key {
			found = true
			break
		}
	}
	if !found {
		notFound(w, "No one wishes for this title")
		return
	}
	// Whose turn went: the first in line for this title, before its state
	// takes it out of the line.
	if spends {
		order, err := p.wishOrder(r, store.MaxWishes)
		if err != nil {
			p.fail(w, "reading the wishes", err)
			return
		}
		for _, next := range order {
			if next.Title == key {
				if err := p.Store.SpendWishTurn(ctx, next.AccountID, next.ProfileID); err != nil {
					p.fail(w, "spending a turn", err)
					return
				}
				break
			}
		}
	}
	if err := p.Store.SetWishState(ctx, st); err != nil {
		p.fail(w, "keeping a wish's state", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
