package catalog

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// LogoSource is TMDB answering in a language, asked for a title's logo
// alone.
type LogoSource interface {
	Logo(ctx context.Context, kind string, tmdbID int) (string, error)
}

// Logos checks again, once, the logos kept before a logo was taken only in
// its language or without text (migration 066 lists their titles): each
// title's logo is asked of TMDB alone, its page isn't fetched again. In
// the default language, the logo kept is replaced, or deleted when TMDB
// has none (the title's name is shown then); in the other languages its
// texts are kept in, which were kept without a logo, it's filled in. The
// artwork keeper (LibraryArtwork) fetches the new pictures at its next
// pass and deletes the old ones; until then they're fetched when shown.
type Logos struct {
	Store *store.Store
	// Source is TMDB answering in a language; nil when TMDB itself isn't
	// reachable. Seerr, the other source of TMDB's data, gives no title's
	// images, so without TMDB the titles wait on the list.
	Source func(language string) LogoSource
	Logger *slog.Logger
	// Every is how long it waits before trying again the titles left,
	// when TMDB isn't there or failed; Pause, between two requests, to go
	// easy on TMDB.
	Every, Pause time.Duration
}

// Run checks the titles on the list until there are none left, trying
// again every Every those left, or until ctx ends.
func (l *Logos) Run(ctx context.Context) {
	for {
		done, err := l.Once(ctx)
		if err != nil && ctx.Err() == nil {
			l.Logger.Warn("catalog: titles' logos couldn't be checked with TMDB; tried again later", "err", err)
		}
		if done {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(l.Every):
		}
	}
}

// Once checks every title on the list, and says whether none is left. A
// title TMDB fails on stays on the list for the next pass; one TMDB
// doesn't know is taken off it; ten failures in a row stop this pass.
func (l *Logos) Once(ctx context.Context) (done bool, err error) {
	// The default language's logo is in the language TMDB is asked in
	// (Titles), the others' in the languages offered.
	def, err := l.Store.TMDBLanguage(ctx)
	if err != nil {
		return false, err
	}
	offered, _, _, err := l.Store.Languages(ctx)
	if err != nil {
		return false, err
	}
	checked, streak := 0, 0
	failed := map[string]bool{}
	defer func() {
		if checked > 0 {
			l.Logger.Info("catalog: titles' logos checked with TMDB", "titles", checked)
		}
	}()
	for {
		todo, err := l.Store.LogosToCheck(ctx, 20+len(failed))
		if err != nil {
			return false, err
		}
		if len(todo) == 0 {
			return true, nil
		}
		progress := false
		for _, t := range todo {
			if failed[t.Key()] {
				continue
			}
			if l.Source(def) == nil {
				return false, nil // TMDB removed meanwhile, or never there
			}
			progress = true
			languages, err := l.Store.TextLanguages(ctx, t.Kind, t.TMDB)
			if err != nil {
				return false, err
			}
			languages = slices.DeleteFunc(languages, func(language string) bool {
				return language == def || !slices.Contains(offered, language)
			})
			logo, others, err := l.check(ctx, t, def, languages)
			switch {
			case errors.Is(err, tmdb.ErrNotFound):
				err = l.Store.LogoChecked(ctx, t)
			case errors.Is(err, tmdb.ErrUnauthorized), ctx.Err() != nil:
				return false, err
			case err != nil:
				failed[t.Key()] = true
				if streak++; streak == 10 {
					return false, err
				}
				continue
			default:
				err = l.Store.SetCheckedLogo(ctx, t, logo, others)
			}
			if err != nil {
				return false, err
			}
			streak = 0
			checked++
		}
		if !progress {
			return false, nil
		}
	}
}

// check asks TMDB for a title's logo in the default language, and in each
// of languages, one request each.
func (l *Logos) check(ctx context.Context, t store.ArtworkTarget, def string, languages []string) (string, map[string]string, error) {
	id, err := strconv.Atoi(t.TMDB)
	if err != nil {
		return "", nil, tmdb.ErrNotFound
	}
	var logo string
	others := map[string]string{}
	for _, language := range append([]string{def}, languages...) {
		src := l.Source(language)
		if src == nil {
			return "", nil, errors.New("catalog: TMDB is no longer reachable")
		}
		got, err := src.Logo(ctx, t.Kind, id)
		if err != nil {
			return "", nil, err
		}
		if language == def {
			logo = got
		} else {
			others[language] = got
		}
		if err := l.pause(ctx); err != nil {
			return "", nil, err
		}
	}
	return logo, others, nil
}

func (l *Logos) pause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(l.Pause):
		return nil
	}
}
