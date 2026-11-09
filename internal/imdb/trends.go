package imdb

import (
	"container/heap"
	"math"
	"time"
)

// Overfin's own trending and popular lists, from how many votes titles
// gained on IMDb, day to day: what IMDb's people watch now, in place of
// TMDB's lists (rule 68).
const (
	// TrendingDays and PopularDays are how far back each list looks, at
	// most: with fewer days of votes kept, as far as they go.
	TrendingDays = 7
	PopularDays  = 30
	// ListLength is how many films, and series, each list holds at most.
	ListLength = 200
	// minGain is the fewest new votes that count: below, it's noise.
	minGain = 20
	// candidates is how many titles are ranked before keeping only films
	// and series: episodes gain votes too.
	candidates = 5000
)

// Ranked is a list's films and series, by IMDb ID, the first first.
type Ranked struct {
	Movies, Series []string
}

// Rank makes a list from the votes kept in dir: popular, the titles that
// gained the most votes in PopularDays; trending (fast), in TrendingDays,
// those gaining fast for their size first, so a new title rises above an
// old one gaining as many. Adult titles are left out. It compares the
// latest day with the oldest kept within the days; ok is false with fewer
// than two days, or without d.
func Rank(dir string, d *Data, fast bool) (r Ranked, ok bool, err error) {
	if d == nil {
		return Ranked{}, false, nil
	}
	days, err := VoteDays(dir)
	if err != nil || len(days) < 2 {
		return Ranked{}, false, err
	}
	latest := days[len(days)-1]
	span := PopularDays
	if fast {
		span = TrendingDays
	}
	end, err := time.Parse(time.DateOnly, latest)
	if err != nil {
		return Ranked{}, false, err
	}
	from := end.AddDate(0, 0, -span).Format(time.DateOnly)
	start := ""
	for _, day := range days[:len(days)-1] {
		if day >= from {
			start = day
			break
		}
	}
	if start == "" {
		return Ranked{}, false, nil
	}
	before := map[uint32]uint32{}
	if err := ReadVotes(dir, start, func(id string, votes int) {
		if n, ok := number(id); ok {
			before[uint32(n)] = uint32(votes)
		}
	}); err != nil {
		return Ranked{}, false, err
	}
	top := &gains{}
	if err := ReadVotes(dir, latest, func(id string, votes int) {
		n, ok := number(id)
		if !ok {
			return
		}
		old := int(before[uint32(n)])
		gain := votes - old
		if gain < minGain {
			return
		}
		score := float64(gain)
		if fast {
			score *= math.Sqrt(float64(gain) / float64(votes))
		}
		if top.Len() < candidates {
			heap.Push(top, gainer{n, score})
		} else if score > (*top)[0].score {
			(*top)[0] = gainer{n, score}
			heap.Fix(top, 0)
		}
	}); err != nil {
		return Ranked{}, false, err
	}
	// The highest first.
	ranked := make([]gainer, top.Len())
	for i := len(ranked) - 1; i >= 0; i-- {
		ranked[i] = heap.Pop(top).(gainer)
	}
	for _, g := range ranked {
		t, ok := d.Title(text(g.id))
		if !ok || t.Adult {
			continue
		}
		switch {
		case t.Kind == Movie && len(r.Movies) < ListLength:
			r.Movies = append(r.Movies, t.ID)
		case t.Kind == Series && len(r.Series) < ListLength:
			r.Series = append(r.Series, t.ID)
		}
	}
	// Nothing gained enough (the same votes two days running, or a latest
	// day nearly empty): no list, rather than an empty one in TMDB's place.
	if len(r.Movies) == 0 && len(r.Series) == 0 {
		return Ranked{}, false, nil
	}
	return r, true, nil
}

// gainer is a title and how it ranks.
type gainer struct {
	id    uint64
	score float64
}

// gains is a heap of gainers, the lowest on top: the one to drop.
type gains []gainer

func (g gains) Len() int           { return len(g) }
func (g gains) Less(i, j int) bool { return g[i].score < g[j].score }
func (g gains) Swap(i, j int)      { g[i], g[j] = g[j], g[i] }
func (g *gains) Push(x any)        { *g = append(*g, x.(gainer)) }
func (g *gains) Pop() any {
	old := *g
	x := old[len(old)-1]
	*g = old[:len(old)-1]
	return x
}
