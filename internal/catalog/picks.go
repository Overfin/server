package catalog

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// PickSource is where the picks' lists come from: TMDB itself.
type PickSource interface {
	// Recommendations are TMDB's titles like a film or a series.
	Recommendations(ctx context.Context, kind string, tmdbID int) (*tmdb.Page, error)
	// ByGenre is one page of a kind's titles in a genre, the most popular
	// first.
	ByGenre(ctx context.Context, kind string, genre, page int) (*tmdb.Page, error)
}

// Picks keeps the lists of titles Overfin may suggest beside TMDB's
// trending and popular ones (store.PickLists): "recommended", those like
// what people here watch and like, and "liked", those in the genres
// accounts said they like. The titles keeper fetches their pages, and the
// valuation values those the libraries lack.
type Picks struct {
	Source func() PickSource
	Store  *store.Store
	Logger *slog.Logger
	// Every is how often the lists are made again; Pause, how long it waits
	// between two requests.
	Every time.Duration
	Pause time.Duration

	now func() time.Time
}

const (
	// pickSeeds is how many of the titles people here watch the most have
	// their look-alikes picked; pickGenres, how many of the genres
	// accounts like the most are read, pickGenrePages pages each.
	pickSeeds      = 25
	pickGenres     = 5
	pickGenrePages = 2
	// seedsSince is how far back what people watched counts.
	seedsSince = 183 * 24 * time.Hour
)

// NewPicks returns the picks' keeper.
func NewPicks(source func() PickSource, st *store.Store, logger *slog.Logger, every, pause time.Duration) *Picks {
	return &Picks{Source: source, Store: st, Logger: logger, Every: every, Pause: pause}
}

func (k *Picks) clock() time.Time {
	if k.now != nil {
		return k.now()
	}
	return time.Now()
}

// Run makes the lists now, then every Every, until ctx ends. Without TMDB,
// it looks again every hour.
func (k *Picks) Run(ctx context.Context) {
	for {
		wait := k.Every
		if k.Source() == nil {
			wait = min(wait, time.Hour)
		} else if err := k.Refresh(ctx); err != nil && ctx.Err() == nil {
			k.Logger.Error("catalog: making the picks' lists failed", "err", err)
			wait = min(wait, time.Hour)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Refresh makes both lists again.
func (k *Picks) Refresh(ctx context.Context) error {
	src := k.Source()
	if src == nil {
		return nil
	}
	if err := k.recommended(ctx, src); err != nil {
		return err
	}
	return k.liked(ctx, src)
}

// pickList collects a list's titles by kind, each once, in the order seen.
type pickList struct {
	ids  map[string][]string
	seen map[string]bool
}

func newList() *pickList { return &pickList{ids: map[string][]string{}, seen: map[string]bool{}} }

// add keeps a page's titles and adds them to the list.
func (k *Picks) add(ctx context.Context, l *pickList, kind string, p *tmdb.Page) error {
	titles := make([]store.Title, 0, len(p.Titles))
	for _, t := range p.Titles {
		st := StoreTitle(t)
		titles = append(titles, st)
		if !l.seen[kind+"/"+st.TMDB] {
			l.seen[kind+"/"+st.TMDB] = true
			l.ids[kind] = append(l.ids[kind], st.TMDB)
		}
	}
	return k.Store.PutTitles(ctx, titles, k.clock())
}

func (k *Picks) save(ctx context.Context, name string, l *pickList) error {
	for _, kind := range []string{"movie", "series"} {
		if err := k.Store.SetList(ctx, name, kind, l.ids[kind]); err != nil {
			return err
		}
	}
	return nil
}

func (k *Picks) recommended(ctx context.Context, src PickSource) error {
	seeds, err := k.Store.PickSeeds(ctx, k.clock().Add(-seedsSince), pickSeeds)
	if err != nil {
		return err
	}
	l := newList()
	for _, r := range seeds {
		id, err := strconv.Atoi(r.ID)
		if err != nil {
			continue
		}
		p, err := src.Recommendations(ctx, r.Kind, id)
		switch {
		case errors.Is(err, tmdb.ErrNotFound):
			continue
		case err != nil:
			return err
		}
		if err := k.add(ctx, l, r.Kind, p); err != nil {
			return err
		}
		if err := k.pause(ctx); err != nil {
			return err
		}
	}
	return k.save(ctx, "recommended", l)
}

func (k *Picks) liked(ctx context.Context, src PickSource) error {
	l := newList()
	asked, err := k.Store.TastesAsked(ctx)
	if err != nil {
		return err
	}
	if !asked {
		return k.save(ctx, "liked", l) // what accounts said goes unused
	}
	tastes, err := k.Store.AllTastes(ctx)
	if err != nil {
		return err
	}
	votes := map[int]int{} // genre → accounts that like it
	for _, t := range tastes {
		for _, g := range t.Genres {
			votes[g]++
		}
	}
	genres := make([]int, 0, len(votes))
	for g := range votes {
		genres = append(genres, g)
	}
	sort.Slice(genres, func(i, j int) bool {
		if votes[genres[i]] != votes[genres[j]] {
			return votes[genres[i]] > votes[genres[j]]
		}
		return genres[i] < genres[j]
	})
	for _, g := range genres[:min(pickGenres, len(genres))] {
		// TMDB numbers some genres for films only, others for series only:
		// asking both kinds finds whichever it is.
		for _, kind := range []string{"movie", "series"} {
			for page := 1; page <= pickGenrePages; page++ {
				p, err := src.ByGenre(ctx, kind, g, page)
				if err != nil {
					return err
				}
				if err := k.add(ctx, l, kind, p); err != nil {
					return err
				}
				if err := k.pause(ctx); err != nil {
					return err
				}
				if page >= p.TotalPages {
					break
				}
			}
		}
	}
	return k.save(ctx, "liked", l)
}

func (k *Picks) pause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(k.Pause):
		return nil
	}
}
