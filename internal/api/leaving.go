package api

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/store"
)

// leaving is what leaves the server soon for the profile: the titles held
// for a last watch (store.Departures) it keeps for later or is partway
// through, by title key, with when they leave. None held: nothing read.
func (l Library) leaving(ctx context.Context, p *store.Profile) (map[string]time.Time, error) {
	st := l.Provisioner.Store
	held, err := st.Departures(ctx, time.Now())
	if err != nil || len(held) == 0 {
		return nil, err
	}
	var interests []store.ProfileTitle
	if l.Interests != nil {
		if interests, err = l.Interests(ctx); err != nil {
			return nil, err
		}
	}
	wanted, err := st.WantedBy(ctx, interests, p.ID)
	if err != nil {
		return nil, err
	}
	out := map[string]time.Time{}
	for key, at := range held {
		if wanted.Wants(key, p.ID) {
			out[key] = at
		}
	}
	return out, nil
}

// leavesAt is when a title shown leaves, from leaving: a film or a season
// its own date, a series its first season's (or its own).
func leavesAt(key string, leaving map[string]time.Time) (time.Time, bool) {
	if at, ok := leaving[key]; ok {
		return at, true
	}
	r, ok := store.ParseTitleKey(key)
	if !ok || r.Kind != "series" {
		return time.Time{}, false
	}
	var first time.Time
	for k, at := range leaving {
		if strings.HasPrefix(k, key+"/s") && (first.IsZero() || at.Before(first)) {
			first = at
		}
	}
	return first, !first.IsZero()
}

// leavingTitles (GET /api/v1/titles/leaving) lists what leaves the server
// soon for the profile, the first to leave first: Home's "Last chance".
func (l Library) leavingTitles(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	leaving, err := l.leaving(ctx, p)
	if err != nil {
		l.storeError(w, err)
		return
	}
	keys := make([]string, 0, len(leaving))
	for key := range leaving {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if !leaving[keys[i]].Equal(leaving[keys[j]]) {
			return leaving[keys[i]].Before(leaving[keys[j]])
		}
		return keys[i] < keys[j]
	})
	out := titlePage{Items: []library.TitleSummary{}}
	if len(keys) > 0 {
		if out.Items, err = l.titleSummaries(ctx, p, keys); err != nil {
			l.storeError(w, err)
			return
		}
	}
	writeOK(w, out)
}

// withLeaving sets LeavesAt on the summaries of what leaves soon for the
// profile.
func (l Library) withLeaving(ctx context.Context, p *store.Profile, items []library.TitleSummary, keys []string) {
	leaving, err := l.leaving(ctx, p)
	if err != nil {
		l.Logger.Warn("what leaves soon", "err", err)
		return
	}
	if len(leaving) == 0 {
		return
	}
	for i := range items {
		if at, ok := leavesAt(keys[i], leaving); ok {
			items[i].LeavesAt = at.UTC().Format(time.RFC3339)
		}
	}
}
