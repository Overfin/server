package catalog

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// CollectionSource is TMDB, asked for collections: series of films, such
// as a saga.
type CollectionSource interface {
	// FilmCollection is the collection a film belongs to, 0 for none.
	FilmCollection(ctx context.Context, tmdbID int) (int, error)
	Collection(ctx context.Context, id int) (*tmdb.Collection, error)
}

// Collections keeps the collections the library's films belong to. A
// film's collection comes with its page (Titles); for the films whose page
// was kept before, it's asked of TMDB once (migration 067 lists them).
// Each collection is kept with its films, and the page of each film no
// library has is fetched too, so its release and its ratings are known:
// a collection's films are shown as any title is, within the profile's
// limits. Everything is fetched again within TMDB's six months.
type Collections struct {
	Store *store.Store
	// Source is TMDB; nil when it isn't reachable (Seerr has no
	// collections).
	Source func() CollectionSource
	// Titles fetches the pages of the films no library has.
	Titles *Titles
	Logger *slog.Logger
	// Every is how often it looks for collections to fetch; Pause, how
	// long it waits between two requests, to go easy on TMDB.
	Every, Pause time.Duration

	now func() time.Time
}

func (c *Collections) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// Run fetches what's to fetch every Every, until ctx ends.
func (c *Collections) Run(ctx context.Context) {
	for {
		if err := c.Once(ctx); err != nil && ctx.Err() == nil {
			c.Logger.Warn("catalog: collections couldn't be fetched from TMDB; tried again later", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.Every):
		}
	}
}

// Once deletes the collections too old to keep, finds the collections of
// the films listed, then fetches the collections not kept or too old. A
// film or a collection TMDB fails on is left for the next pass; ten
// failures in a row stop this one.
func (c *Collections) Once(ctx context.Context) error {
	if err := c.Store.PurgeCollections(ctx, c.clock().Add(-Kept)); err != nil {
		return err
	}
	if c.Source() == nil {
		return nil
	}
	if err := c.find(ctx); err != nil {
		return err
	}
	return c.fetch(ctx)
}

// find asks TMDB the collection of each film listed.
func (c *Collections) find(ctx context.Context) error {
	found, streak := 0, 0
	failed := map[string]bool{}
	defer func() {
		if found > 0 {
			c.Logger.Info("catalog: films' collections found on TMDB", "films", found)
		}
	}()
	for {
		todo, err := c.Store.FilmsToFind(ctx, 20+len(failed))
		if err != nil {
			return err
		}
		progress := false
		for _, film := range todo {
			if failed[film] {
				continue
			}
			src := c.Source()
			if src == nil {
				return nil // TMDB removed meanwhile
			}
			progress = true
			collection := 0
			id, err := strconv.Atoi(film)
			if err == nil {
				collection, err = src.FilmCollection(ctx, id)
			}
			switch {
			case errors.Is(err, tmdb.ErrUnauthorized), ctx.Err() != nil:
				return err
			case err != nil && !errors.Is(err, tmdb.ErrNotFound) && !errors.Is(err, strconv.ErrSyntax):
				failed[film] = true
				if streak++; streak == 10 {
					return err
				}
				continue
			}
			// One TMDB doesn't know belongs to none.
			if err := c.Store.SetFilmCollection(ctx, film, collection); err != nil {
				return err
			}
			streak = 0
			found++
			if err := c.pause(ctx); err != nil {
				return err
			}
		}
		if !progress {
			return nil
		}
	}
}

// fetch keeps each collection not kept or too old, with its films.
func (c *Collections) fetch(ctx context.Context) error {
	fetched, streak := 0, 0
	failed := map[int]bool{}
	defer func() {
		if fetched > 0 {
			c.Logger.Info("catalog: collections fetched from TMDB", "collections", fetched)
		}
	}()
	for {
		todo, err := c.Store.CollectionsToFetch(ctx, c.clock().Add(-TitlesFresh), 20+len(failed))
		if err != nil {
			return err
		}
		progress := false
		for _, id := range todo {
			if failed[id] {
				continue
			}
			src := c.Source()
			if src == nil {
				return nil
			}
			progress = true
			err := c.one(ctx, src, id)
			switch {
			case errors.Is(err, tmdb.ErrUnauthorized), ctx.Err() != nil:
				return err
			case err != nil:
				failed[id] = true
				if streak++; streak == 10 {
					return err
				}
				continue
			}
			streak = 0
			fetched++
		}
		if !progress {
			return nil
		}
	}
}

// one keeps a collection and its films, and fetches the pages of its films
// not kept or too old. A film's page TMDB fails on is left for the next
// time the collection is fetched (it's shown as it is meanwhile).
func (c *Collections) one(ctx context.Context, src CollectionSource, id int) error {
	got, err := src.Collection(ctx, id)
	if err != nil {
		return err
	}
	kept := store.Collection{ID: got.ID, Name: got.Name, Overview: got.Overview, Poster: got.Poster, Backdrop: got.Backdrop}
	films := make([]store.Title, 0, len(got.Parts))
	for _, p := range got.Parts {
		t := StoreTitle(p)
		films = append(films, t)
		kept.Parts = append(kept.Parts, t.TMDB)
	}
	if err := c.Store.SetCollection(ctx, kept, films, c.clock()); err != nil {
		return err
	}
	if err := c.pause(ctx); err != nil {
		return err
	}
	todo, err := c.Store.FilmsToDescribe(ctx, kept.Parts, c.clock().Add(-TitlesFresh))
	if err != nil {
		return err
	}
	for _, film := range todo {
		if c.Titles == nil {
			break
		}
		err := c.Titles.FetchTitle(ctx, "movie", film)
		if errors.Is(err, tmdb.ErrUnauthorized) || ctx.Err() != nil {
			return err
		}
		if err != nil && !errors.Is(err, tmdb.ErrNotFound) {
			c.Logger.Warn("catalog: a collection's film couldn't be fetched from TMDB; tried again later", "film", film, "err", err)
		}
		if err := c.pause(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (c *Collections) pause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(c.Pause):
		return nil
	}
}
