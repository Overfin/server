package api

import (
	"context"
	"slices"
	"strings"

	"github.com/overfin/server/internal/lang"

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

// copyJSON is one of a title's copies as its page shows it.
type copyJSON struct {
	Video     string   `json:"video,omitempty"`
	Audio     []string `json:"audio,omitempty"`
	Subtitles []string `json:"subtitles,omitempty"`
}

// copiesFor are a title's copies (items) the profile may see, as their
// page shows them: languages named in themselves, as the player does; a
// copy like another shown once.
func (l Library) copiesFor(ctx context.Context, p *store.Profile, items []string) ([]copyJSON, error) {
	st := l.Provisioner.Store
	a, err := l.access(ctx, p)
	if err != nil {
		return nil, err
	}
	if a != nil && len(items) > 0 {
		allowed, _, err := st.AllowedItems(ctx, *a, items)
		if err != nil {
			return nil, err
		}
		items = slices.DeleteFunc(slices.Clone(items), func(id string) bool { return !allowed[id] })
	}
	copies, err := st.Copies(ctx, items)
	if err != nil {
		return nil, err
	}
	names := func(tags []string) []string {
		var out []string
		for _, t := range tags {
			if n := lang.Name(t); n != "?" && !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
		return out
	}
	var out []copyJSON
	seen := map[string]bool{}
	for _, c := range copies {
		j := copyJSON{Video: c.Video, Audio: names(c.Audio), Subtitles: names(c.Subtitles)}
		k := j.Video + "|" + strings.Join(j.Audio, ",") + "|" + strings.Join(j.Subtitles, ",")
		if seen[k] || (j.Video == "" && len(j.Audio) == 0 && len(j.Subtitles) == 0) {
			continue
		}
		seen[k] = true
		out = append(out, j)
	}
	return out, nil
}
