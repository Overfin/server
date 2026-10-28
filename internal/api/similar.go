package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"

	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// maxSimilar is how many similar titles a title page shows at most.
const maxSimilar = 20

// similar lists the titles TMDB recommends with a film or a series (a
// season's or an episode's, its series'): those a library has first, then,
// when the operator shows titles no library has on the home page, the
// others; then, up to maxSimilar, the library's titles that have the most
// in common with it (store.RelatedTitles), its own collection's aside (the
// page links to it). Each as the profile may see it.
func (l Library) similar(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r, "limit") {
		return
	}
	limit, ok := parseLimit(r.URL.Query().Get("limit"))
	if !ok {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	ref, ok := titleRef(r.PathValue("id"))
	if !ok || ref.TMDB() == "" {
		problem.Send(w, problem.NotFound, "")
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	// Only for a title the profile may see.
	if own, err := l.titleSummaries(ctx, p, []string{ref.Key()}); err != nil {
		l.storeError(w, err)
		return
	} else if len(own) == 0 {
		problem.Send(w, problem.NotFound, "")
		return
	}
	kind := "series"
	if ref.Kind == "movie" {
		kind = "movie"
	}
	st := l.Provisioner.Store
	out := []library.TitleSummary{}
	t, err := st.TitleByTMDB(ctx, kind, ref.TMDB())
	if err != nil {
		l.storeError(w, err)
		return
	}
	var det store.TitleDetails
	if t != nil && len(t.Details) > 0 {
		_ = json.Unmarshal(t.Details, &det) // kept by Overfin: a bad page is none
	}
	keys := make([]string, 0, len(det.Similar))
	for _, id := range det.Similar {
		keys = append(keys, kind+"/tmdb/"+id)
	}
	if len(keys) > 0 {
		all, err := l.titleSummaries(ctx, p, keys)
		if err != nil {
			l.storeError(w, err)
			return
		}
		missing, err := st.HomeMissing(ctx)
		if err != nil {
			l.storeError(w, err)
			return
		}
		var others []library.TitleSummary
		for _, s := range all {
			if s.Availability == "missing" {
				if missing {
					others = append(others, s)
				}
				continue
			}
			out = append(out, s)
		}
		out = append(out, others...)
	}
	want := min(maxSimilar, limit)
	if len(out) < want {
		more, err := l.related(ctx, p, kind, ref.TMDB(), out, want-len(out))
		if err != nil {
			l.storeError(w, err)
			return
		}
		out = append(out, more...)
	}
	out = out[:min(len(out), want)]
	writeOK(w, map[string]any{"items": out})
}

// related returns up to n of the library's titles most related to a film or
// a series, as the profile may see them, leaving out those already listed
// and the film's own collection.
func (l Library) related(ctx context.Context, p *store.Profile, kind, tmdb string, listed []library.TitleSummary, n int) ([]library.TitleSummary, error) {
	st := l.Provisioner.Store
	// A few more than needed: some may be listed already, or hidden.
	keys, err := st.RelatedTitles(ctx, kind+"/tmdb/"+tmdb, n+len(listed)+20)
	if err != nil {
		return nil, err
	}
	skip := map[string]bool{}
	for _, s := range listed {
		skip[s.ID] = true
	}
	if kind == "movie" {
		c, err := st.FilmCollection(ctx, tmdb)
		if err != nil {
			return nil, err
		}
		if c != nil {
			for _, film := range c.Parts {
				skip[titleID("movie/tmdb/"+film)] = true
			}
		}
	}
	keys = slices.DeleteFunc(keys, func(k string) bool { return skip[titleID(k)] })
	all, err := l.titleSummaries(ctx, p, keys)
	if err != nil {
		return nil, err
	}
	return all[:min(len(all), n)], nil
}
