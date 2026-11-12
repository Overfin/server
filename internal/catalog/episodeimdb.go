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

// noneAgain is how soon an episode TMDB gave no IMDb ID is asked again:
// TMDB often adds it a while after the episode airs.
const noneAgain = 30 * 24 * time.Hour

// EpisodeIMDbSource is TMDB itself, asked for an episode's IMDb ID. Seerr,
// the other source of TMDB's data, can't say it.
type EpisodeIMDbSource interface {
	EpisodeIMDb(ctx context.Context, seriesID, season, episode int) (string, error)
}

// EpisodeIMDb asks TMDB the IMDb ID of each of the libraries' episodes,
// while the operator has IMDb's data on (rule 115): a season's rating is
// then its own episodes', matched by ID whatever order each site numbers
// them in. TMDB has no way to ask for many episodes at once, so it's one
// request per episode, once, a while apart; asked again only within
// TMDB's six months, like the rest of its data.
type EpisodeIMDb struct {
	Store *store.Store
	// Source is TMDB itself, nil when it isn't reachable; On, whether
	// IMDb's data is on.
	Source func() EpisodeIMDbSource
	On     func(ctx context.Context) (bool, error)
	Logger *slog.Logger
	// Every is how often it looks for episodes to ask; Pause, how long it
	// waits between two, to go easy on TMDB.
	Every, Pause time.Duration
	now          func() time.Time
}

func (e *EpisodeIMDb) clock() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now()
}

// Run asks now, then every Every, until ctx ends.
func (e *EpisodeIMDb) Run(ctx context.Context) {
	for {
		if err := e.Once(ctx); err != nil && ctx.Err() == nil {
			e.Logger.Warn("catalog: episodes' IMDb IDs couldn't be asked of TMDB; tried again later", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(e.Every):
		}
	}
}

// Once deletes what's too old to keep, then, with IMDb's data on, asks
// every episode not asked lately.
func (e *EpisodeIMDb) Once(ctx context.Context) error {
	if err := e.Store.PurgeEpisodeIMDb(ctx, e.clock().Add(-Kept)); err != nil {
		return err
	}
	on, err := e.On(ctx)
	if err != nil || !on {
		return err
	}
	asked := 0
	defer func() {
		if asked > 0 {
			e.Logger.Info("catalog: episodes' IMDb IDs asked of TMDB", "episodes", asked)
		}
	}()
	for {
		src := e.Source()
		if src == nil {
			return nil
		}
		now := e.clock()
		todo, err := e.Store.EpisodesToLink(ctx, now, now.Add(-TitlesFresh), now.Add(-noneAgain), 100)
		if err != nil || len(todo) == 0 {
			return err
		}
		for _, ep := range todo {
			id, err := strconv.Atoi(ep.Series)
			if err != nil {
				continue
			}
			imdb, err := src.EpisodeIMDb(ctx, id, ep.Season, ep.Episode)
			switch {
			case errors.Is(err, tmdb.ErrNotFound):
				// TMDB doesn't have it at those numbers any more: kept as
				// none, asked again with the rest of the series' data.
			case err != nil:
				return err
			}
			if err := e.Store.SetEpisodeIMDb(ctx, ep.Series, ep.TMDBID, imdb, e.clock()); err != nil {
				return err
			}
			asked++
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(e.Pause):
			}
		}
	}
}
