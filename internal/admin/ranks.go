package admin

import (
	"context"
	"time"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/valuation"
)

// KeepRanks writes each film's and series' rank on the server (store
// title_ranks) now and every every, until ctx ends: title pages show it
// and libraries sort by it.
func (p *Panel) KeepRanks(ctx context.Context, every time.Duration) {
	for {
		if err := p.writeRanks(ctx); err != nil && ctx.Err() == nil {
			p.Logger.Warn("the titles' ranks", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// writeRanks ranks the libraries' films among films and series among
// series, by their value under the operator's weights, the most valued
// first (ties by key, so the order holds still).
func (p *Panel) writeRanks(ctx context.Context) error {
	saved, err := p.Store.ValuationWeights(ctx)
	if err != nil {
		return err
	}
	m, err := p.measured(ctx)
	if err != nil {
		return err
	}
	return p.Store.SetTitleRanks(ctx, ranksOf(m.Scored(valuation.Weights(saved))))
}

// ranksOf numbers the libraries' films and series, each kind on its own,
// in the order of scored (the most valued first).
func ranksOf(scored []valuation.Value) map[string]store.TitleRank {
	out := map[string]store.TitleRank{}
	next := map[string]int{}
	for _, v := range scored {
		r, _ := store.ParseTitleKey(v.Key)
		if !v.InLibrary || (r.Kind != "movie" && r.Kind != "series") {
			continue
		}
		next[r.Kind]++
		out[v.Key] = store.TitleRank{Kind: r.Kind, Rank: next[r.Kind]}
	}
	return out
}
