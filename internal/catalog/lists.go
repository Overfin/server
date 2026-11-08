package catalog

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/overfin/server/internal/imdb"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// Lists keeps discovery's lists (trending, popular and upcoming films and
// series) and the titles in them, from TMDB (itself, or through Seerr), in
// Overfin's database, so discovery is answered from there, at once, and
// TMDB is asked in the background only. While IMDb's data is on, trending
// and popular are made of IMDb's daily votes instead (rule 68), their
// titles found on TMDB by IMDb ID, each once.
type Lists struct {
	// Source is where TMDB's data comes from, nil when there's none.
	Source func() tmdb.Source
	Store  *store.Store
	Logger *slog.Logger
	// Every is how often the lists are read again; Depth, how many of
	// TMDB's pages (20 titles each) each list keeps.
	Every time.Duration
	Depth int
	// IMDb is trending's or popular's films and series made of IMDb's
	// votes; nil, or false, keeps TMDB's (imdb.Keeper.Lists).
	IMDb func(list string) (imdb.Ranked, bool)
	// Pause is how long it waits between two titles it finds on TMDB.
	Pause time.Duration

	wake chan struct{}
	now  func() time.Time
}

// Kept is how long a title fetched from TMDB stays without
// being fetched again: TMDB's terms allow six months at most.
const Kept = 150 * 24 * time.Hour

// NewLists returns the lists' keeper, reading depth pages per list every
// every.
func NewLists(s func() tmdb.Source, st *store.Store, logger *slog.Logger, every time.Duration, depth int) *Lists {
	return &Lists{Source: s, Store: st, Logger: logger, Every: every, Depth: depth, wake: make(chan struct{}, 1)}
}

func (l *Lists) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// Wake asks for the lists to be read again now, for example right after
// Seerr was connected.
func (l *Lists) Wake() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// Run reads the lists now, then every Every and whenever woken, until ctx
// ends.
func (l *Lists) Run(ctx context.Context) {
	for {
		if err := l.Refresh(ctx); err != nil && ctx.Err() == nil {
			l.Logger.Error("catalog: reading discovery's lists failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-l.wake:
		case <-time.After(l.Every):
		}
	}
}

// Refresh reads every list from TMDB, keeps their titles, and deletes
// those too old to keep.
func (l *Lists) Refresh(ctx context.Context) error {
	if _, err := l.Store.PurgeTitles(ctx, l.clock().Add(-Kept)); err != nil {
		return err
	}
	s := l.Source()
	if s == nil {
		return nil
	}
	for _, list := range tmdb.Lists {
		for _, kind := range []string{"movie", "series"} {
			if err := l.read(ctx, s, list, kind); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *Lists) read(ctx context.Context, s tmdb.Source, list, kind string) error {
	if l.IMDb != nil && (list == "trending" || list == "popular") {
		finder, can := s.(titleFinder)
		if ranked, ok := l.IMDb(list); ok && can {
			ids := ranked.Movies
			if kind == "series" {
				ids = ranked.Series
			}
			return l.readIMDb(ctx, finder, list, kind, ids)
		}
	}
	at := l.clock()
	var ids []string
	seen := map[string]bool{}
	for page := 1; page <= l.Depth; page++ {
		p, err := s.Discover(ctx, list, kind, page)
		if err != nil {
			return err
		}
		titles := make([]store.Title, 0, len(p.Titles))
		for _, t := range p.Titles {
			st := StoreTitle(t)
			titles = append(titles, st)
			if !seen[st.TMDB] { // a title can move between pages while they're read
				seen[st.TMDB] = true
				ids = append(ids, st.TMDB)
			}
		}
		if err := l.Store.PutTitles(ctx, titles, at); err != nil {
			return err
		}
		if page >= p.TotalPages {
			break
		}
	}
	return l.Store.SetList(ctx, list, kind, ids)
}

// titleFinder finds a title on TMDB by its IMDb ID: TMDB itself does,
// Seerr doesn't.
type titleFinder interface {
	FindTitle(ctx context.Context, kind, imdbID string) (tmdb.Title, error)
}

// noneFoundAgain is how soon an IMDb ID TMDB knew no title for is asked
// again: TMDB adds titles, and IDs to them.
const noneFoundAgain = 30 * 24 * time.Hour

// readIMDb keeps a list made of IMDb's votes (imdbIDs, the first first):
// each title's TMDB ID, known (store.IMDbTMDB) or found on TMDB now, its
// data kept, until the list holds as many as TMDB's would (Depth pages).
func (l *Lists) readIMDb(ctx context.Context, f titleFinder, list, kind string, imdbIDs []string) error {
	var ids []string
	seen := map[string]bool{}
	found := 0
	defer func() {
		if found > 0 {
			l.Logger.Info("catalog: titles of IMDb's lists found on TMDB", "list", list, "kind", kind, "titles", found)
		}
	}()
	for _, id := range imdbIDs {
		if len(ids) >= l.Depth*20 {
			break
		}
		at := l.clock()
		tmdbID, ok, err := l.Store.IMDbTMDB(ctx, kind, id, at.Add(-TitlesFresh), at.Add(-noneFoundAgain))
		if err != nil {
			return err
		}
		if !ok {
			t, err := f.FindTitle(ctx, kind, id)
			switch {
			case errors.Is(err, tmdb.ErrNotFound):
				tmdbID = ""
			case err != nil:
				return err
			default:
				st := StoreTitle(t)
				if err := l.Store.PutTitles(ctx, []store.Title{st}, at); err != nil {
					return err
				}
				tmdbID = st.TMDB
				found++
			}
			if err := l.Store.PutFound(ctx, kind, id, tmdbID, at); err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(l.Pause):
			}
		}
		if tmdbID != "" && !seen[tmdbID] {
			seen[tmdbID] = true
			ids = append(ids, tmdbID)
		}
	}
	return l.Store.SetList(ctx, list, kind, ids)
}

// StoreTitle is a title from TMDB as the catalog keeps it.
func StoreTitle(t tmdb.Title) store.Title {
	return store.Title{Kind: t.Kind, TMDB: strconv.Itoa(t.TMDBID), Name: t.Name, Year: t.Year, Overview: t.Overview,
		Poster: t.Poster, Backdrop: t.Backdrop, Status: t.Status, ListGenres: t.ListGenres}
}
