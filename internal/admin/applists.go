package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/overfin/server/internal/store"
)

const (
	// appListsMax is how many lists an app may send, appListsMaxTitles how
	// many titles in all, appListsMaxBytes how big the request may be.
	appListsMax       = 200
	appListsMaxTitles = 100_000
	appListsMaxBytes  = 8 << 20
)

// appLists (PUT /apps/v1/lists, lists scope) is the app that reads the
// operator's import lists (Overtide, from Radarr and Sonarr) sending them
// all: each replaces what was sent before. Their titles the libraries lack
// are picks, valued like every other, and the lists' share of the
// suggestions comes from them in their order (suggestionOrder).
func (p *Panel) appLists(w http.ResponseWriter, r *http.Request) {
	if p.appKey(w, r, "lists") == nil {
		return
	}
	var req struct {
		Lists []struct {
			Name   string `json:"name"`
			Titles []struct {
				Kind string `json:"kind"`
				TMDB string `json:"tmdbId"`
			} `json:"titles"`
		} `json:"lists"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, appListsMaxBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Lists == nil {
		invalid(w, "not lists")
		return
	}
	if len(req.Lists) > appListsMax {
		invalid(w, "at most "+strconv.Itoa(appListsMax)+" lists")
		return
	}
	lists := make([]store.OperatorList, 0, len(req.Lists))
	names := map[string]bool{}
	titles := 0
	for _, l := range req.Lists {
		name := strings.TrimSpace(l.Name)
		if name == "" || utf8.RuneCountInString(name) > 100 || names[name] {
			invalid(w, "name: each list's own, 100 characters at most")
			return
		}
		names[name] = true
		titles += len(l.Titles)
		if titles > appListsMaxTitles {
			invalid(w, "at most "+strconv.Itoa(appListsMaxTitles)+" titles in all")
			return
		}
		list := store.OperatorList{Name: name, Titles: make([]store.ListTitle, 0, len(l.Titles))}
		seen := map[string]bool{}
		for _, t := range l.Titles {
			n, err := strconv.Atoi(t.TMDB)
			if err != nil || n < 1 || strconv.Itoa(n) != t.TMDB || (t.Kind != "movie" && t.Kind != "series") {
				invalid(w, "titles: kind movie or series, and a TMDB ID")
				return
			}
			if !seen[t.Kind+"/"+t.TMDB] {
				seen[t.Kind+"/"+t.TMDB] = true
				list.Titles = append(list.Titles, store.ListTitle{Kind: t.Kind, TMDB: t.TMDB})
			}
		}
		lists = append(lists, list)
	}
	if err := p.Store.SetOperatorLists(r.Context(), lists, p.clock()); err != nil {
		p.fail(w, "keeping the operator's lists", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
