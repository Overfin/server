package catalog

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strconv"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// ErrNoSource: no TMDB to ask.
var ErrNoSource = errors.New("catalog: no TMDB to ask")

// OrderSource is TMDB, asked for a series' other episode orders. A source
// without them leaves every series in TMDB's own order.
type OrderSource interface {
	OtherOrders(ctx context.Context, seriesID int) ([]tmdb.EpisodeOrder, error)
	ArrangedOrder(ctx context.Context, seriesID int, id string) (*tmdb.ArrangedSeasons, error)
}

// plainSeasonName is a group's name for a season that says only its number,
// in the language its author wrote: TMDB's own name for that number, in the
// language asked for, reads better.
var plainSeasonName = regexp.MustCompile(`(?i)^\s*(season|saison|temporada|staffel|stagione)\s*\d+\s*$`)

// orderSeasons reads the seasons of the order chosen for a series; nil when
// it has TMDB's own. full is the series' page, when it's read already. An
// order gone from TMDB (they're its users') leaves what's kept as it is.
func orderSeasons(ctx context.Context, st *store.Store, src TitleSource, series int, full *tmdb.Full) (map[int]*tmdb.SeasonFull, error) {
	id := strconv.Itoa(series)
	group, _, err := st.SeriesOrder(ctx, id)
	if err != nil || group == "" {
		return nil, err
	}
	source, ok := src.(OrderSource)
	if !ok {
		return nil, nil
	}
	seasons, err := arranged(ctx, source, src, series, group, full)
	if errors.Is(err, tmdb.ErrNotFound) {
		return keptSeasons(ctx, st, id)
	}
	return seasons, err
}

// keptSeasons are a series' seasons as they're kept.
func keptSeasons(ctx context.Context, st *store.Store, series string) (map[int]*tmdb.SeasonFull, error) {
	kept, err := st.SeriesSeasonsKept(ctx, series)
	if err != nil {
		return nil, err
	}
	out := make(map[int]*tmdb.SeasonFull, len(kept))
	for _, se := range kept {
		full := &tmdb.SeasonFull{Number: se.Number, Name: se.Name, Overview: se.Overview, AirDate: se.AirDate, Poster: se.Poster}
		for _, e := range se.Episodes {
			full.Episodes = append(full.Episodes, tmdb.Episode{ID: e.ID, Number: e.Number, Name: e.Name, Overview: e.Overview,
				AirDate: e.AirDate, Runtime: e.Runtime, Still: e.Still})
		}
		out[se.Number] = full
	}
	return out, nil
}

// arranged reads an order: its seasons, named as TMDB names the same numbers
// where the group's names say no more than that, and TMDB's specials when it
// has none of its own.
func arranged(ctx context.Context, source OrderSource, src TitleSource, series int, group string, full *tmdb.Full) (map[int]*tmdb.SeasonFull, error) {
	order, err := source.ArrangedOrder(ctx, series, group)
	if err != nil {
		return nil, err
	}
	if full == nil {
		if full, err = src.Title(ctx, "series", series); err != nil {
			return nil, err
		}
	}
	listed := map[int]string{}
	hasSpecials := false
	for _, se := range full.Seasons {
		listed[se.Number] = se.Name
		hasSpecials = hasSpecials || se.Number == 0
	}
	for n, se := range order.Seasons {
		if name := listed[n]; name != "" && plainSeasonName.MatchString(se.Name) {
			se.Name = name
		}
	}
	if order.Seasons[0] == nil && hasSpecials {
		if specials, err := src.Season(ctx, series, 0); err == nil {
			order.Seasons[0] = specials
		}
	}
	return order.Seasons, nil
}

// seasonInfos lists seasons as a series' page does, by number.
func seasonInfos(seasons map[int]*tmdb.SeasonFull) []store.SeasonInfo {
	out := make([]store.SeasonInfo, 0, len(seasons))
	for _, se := range seasons {
		out = append(out, store.SeasonInfo{Number: se.Number, Name: se.Name, EpisodeCount: len(se.Episodes), AirDate: se.AirDate})
	}
	slices.SortFunc(out, func(a, b store.SeasonInfo) int { return a.Number - b.Number })
	return out
}

// SeriesOrders are a series' orders as an admin compares them: TMDB's own
// first, its ID "".
type SeriesOrder struct {
	ID      string
	Name    string
	Type    int
	Seasons map[int]*tmdb.SeasonFull
}

// ReadOrders reads a series' orders, TMDB's own first; those that can't be
// read are left out, TMDB's own apart.
func (a *Titles) ReadOrders(ctx context.Context, series int) ([]SeriesOrder, error) {
	src := a.Source()
	if src == nil {
		return nil, ErrNoSource
	}
	own, err := ownSeasons(ctx, src, series)
	if err != nil {
		return nil, err
	}
	out := []SeriesOrder{{Seasons: own}}
	source, ok := src.(OrderSource)
	if !ok {
		return out, nil
	}
	others, err := source.OtherOrders(ctx, series)
	if err != nil {
		return nil, err
	}
	for _, o := range others {
		order, err := source.ArrangedOrder(ctx, series, o.ID)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		out = append(out, SeriesOrder{ID: o.ID, Name: o.Name, Type: o.Type, Seasons: order.Seasons})
	}
	return out, nil
}

// ownSeasons reads TMDB's own seasons of a series.
func ownSeasons(ctx context.Context, src TitleSource, series int) (map[int]*tmdb.SeasonFull, error) {
	full, err := src.Title(ctx, "series", series)
	if err != nil {
		return nil, err
	}
	numbers := make([]int, 0, len(full.Seasons))
	for _, se := range full.Seasons {
		numbers = append(numbers, se.Number)
	}
	if len(numbers) == 0 {
		return map[int]*tmdb.SeasonFull{}, nil
	}
	return src.Seasons(ctx, series, numbers)
}

// storeSeasons are seasons as the store keeps them.
func storeSeasons(seasons map[int]*tmdb.SeasonFull) []store.Season {
	out := make([]store.Season, 0, len(seasons))
	for _, se := range seasons {
		season := store.Season{Number: se.Number, Name: se.Name, Overview: se.Overview, AirDate: se.AirDate, Poster: se.Poster}
		for _, e := range se.Episodes {
			season.Episodes = append(season.Episodes, store.Episode{ID: e.ID, Number: e.Number, Name: e.Name, Overview: e.Overview,
				AirDate: e.AirDate, Runtime: e.Runtime, Still: e.Still})
		}
		out = append(out, season)
	}
	return out
}

// ChooseOrder shows a series as an order has it (group "": TMDB's own): the
// order must still be on TMDB. Which episode a file is doesn't change. See
// store.ArrangeSeries.
func (a *Titles) ChooseOrder(ctx context.Context, series int, group string) error {
	src := a.Source()
	if src == nil {
		return ErrNoSource
	}
	own, err := ownSeasons(ctx, src, series)
	if err != nil {
		return err
	}
	seasons, name := own, ""
	if group != "" {
		source, ok := src.(OrderSource)
		if !ok {
			return tmdb.ErrNotFound
		}
		others, err := source.OtherOrders(ctx, series)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(others, func(o tmdb.EpisodeOrder) bool { return o.ID == group })
		if i < 0 {
			return tmdb.ErrNotFound
		}
		name = others[i].Name
		if seasons, err = arranged(ctx, source, src, series, group, nil); err != nil {
			return err
		}
	}
	if err := a.Store.ArrangeSeries(ctx, strconv.Itoa(series), group, name, storeSeasons(seasons), storeSeasons(own), a.clock()); err != nil {
		return err
	}
	a.Wake()
	return nil
}
