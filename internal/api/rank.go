package api

import (
	"context"

	"github.com/overfin/server/internal/store"
)

// rankFor is a film's or a series' rank for profile p: the server's (store
// title_ranks), or, for a profile that may not see everything, its place
// among the titles of its kind it may see. nil when it has none.
func (l Library) rankFor(ctx context.Context, p *store.Profile, key string) (*int, error) {
	st := l.Provisioner.Store
	r, ok, err := st.RankOf(ctx, key)
	if err != nil || !ok {
		return nil, err
	}
	a, err := l.access(ctx, p)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return &r.Rank, nil
	}
	before, err := st.RankedBefore(ctx, r.Kind, r.Rank)
	if err != nil {
		return nil, err
	}
	var copies []string
	for _, ids := range before {
		copies = append(copies, ids...)
	}
	n := 1
	if len(copies) > 0 {
		allowed, _, err := st.AllowedItems(ctx, *a, copies)
		if err != nil {
			return nil, err
		}
		for _, ids := range before {
			for _, id := range ids {
				if allowed[id] {
					n++
					break
				}
			}
		}
	}
	return &n, nil
}
