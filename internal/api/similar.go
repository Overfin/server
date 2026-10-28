package api

import (
	"encoding/json"
	"net/http"

	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// maxSimilar is how many similar titles a title page shows at most.
const maxSimilar = 20

// similar lists the titles TMDB recommends with a film or a series (a
// season's or an episode's, its series'): those a library has first, then,
// when the operator shows titles no library has on the home page, the
// others. Each as the profile may see it.
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
		out = out[:min(len(out), maxSimilar, limit)]
	}
	writeOK(w, map[string]any{"items": out})
}
