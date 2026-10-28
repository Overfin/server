package store

import (
	"context"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
)

// Related titles: those of the library's films (or series) that have the
// most in common with one, by what Overfin knows of each: TMDB's genres,
// its keywords (Jellyfin's tags), the people Jellyfin lists (cast and
// crew), the original language and the year. What they share counts by how
// rare it is in the library: two films sharing "Drama" have little in
// common, two sharing a director or "time travel" much. The same title
// always gets the same list. The library's facts, no one's, are kept in
// memory a few minutes (relatedFor).

// relatedFor is how long the library's facts for related titles are kept.
const relatedFor = 5 * time.Minute

// What each kind of thing in common weighs, times how rare it is; the
// language and the year add a little; the rating only orders titles with
// as much in common.
const (
	relatedGenre    = 1.0
	relatedTag      = 1.0
	relatedPerson   = 1.2
	relatedLanguage = 0.4
	relatedYear     = 0.6 // at most, for the same year; none 20 years apart
	relatedRating   = 0.02
	// relatedEnough is what a title must have in common to be related: more
	// than one common genre alone.
	relatedEnough = 1.2
)

type relatedFacts struct {
	key      string
	kind     string
	year     int
	rating   float64
	language string
	// Its genres, tags and people, as feature keys ("g:18", "t:time
	// travel", "p:<id>").
	features []string
}

type relatedCache struct {
	mu     sync.Mutex
	at     time.Time
	titles []*relatedFacts
	byKey  map[string]*relatedFacts
	// df is how many titles of each kind have each feature, and the
	// language under "l:<language>".
	df    map[string]map[string]int
	count map[string]int // titles of each kind
}

// RelatedTitles returns the library's titles of the same kind as the one
// at key that have the most in common with it, most first, limit at most;
// the title itself and those with too little in common are left out.
func (s *Store) RelatedTitles(ctx context.Context, key string, limit int) ([]string, error) {
	c, err := s.relatedFacts(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	target := c.byKey[key]
	if target == nil || limit <= 0 {
		return nil, nil
	}
	n := float64(c.count[target.kind])
	df := c.df[target.kind]
	idf := func(f string) float64 {
		if d := df[f]; d > 0 {
			return math.Log(n / float64(d))
		}
		return 0
	}
	weight := make(map[string]float64, len(target.features))
	for _, f := range target.features {
		w := relatedTag
		switch f[0] {
		case 'g':
			w = relatedGenre
		case 'p':
			w = relatedPerson
		}
		weight[f] = w * idf(f)
	}
	type scored struct {
		key   string
		score float64
	}
	var found []scored
	for _, t := range c.titles {
		if t.kind != target.kind || t.key == target.key {
			continue
		}
		shared := 0.0
		for _, f := range t.features {
			shared += weight[f]
		}
		if shared < relatedEnough {
			continue
		}
		score := shared
		if t.language != "" && t.language == target.language {
			score += relatedLanguage * idf("l:"+t.language)
		}
		if t.year > 0 && target.year > 0 {
			gap := math.Abs(float64(t.year - target.year))
			score += relatedYear * math.Max(0, 1-gap/20)
		}
		score += relatedRating * t.rating
		found = append(found, scored{t.key, score})
	}
	slices.SortFunc(found, func(a, b scored) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		}
		return strings.Compare(a.key, b.key)
	})
	out := make([]string, 0, min(limit, len(found)))
	for _, f := range found[:min(limit, len(found))] {
		out = append(out, f.key)
	}
	return out, nil
}

// relatedFacts returns the library's facts for related titles, read again
// when older than relatedFor.
func (s *Store) relatedFacts(ctx context.Context) (*relatedCache, error) {
	c := &s.related
	c.mu.Lock()
	if now := s.now(); c.byKey != nil && now.Sub(c.at) < relatedFor && !now.Before(c.at) {
		c.mu.Unlock()
		return c, nil
	}
	defer c.mu.Unlock()
	titles := map[string]*relatedFacts{}
	var order []*relatedFacts
	// Each film and series once, by its title key: its year, rating,
	// language and genres.
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT i.title_key, i.kind, coalesce(t.year, 0), coalesce(t.rating, 0),
			coalesce(l.language, ''),
			(SELECT group_concat(g.genre) FROM catalog_title_genres g WHERE g.kind = i.kind AND g.tmdb = i.tmdb)
		FROM catalog_items i
		LEFT JOIN catalog_titles t ON t.kind = i.kind AND t.tmdb = i.tmdb
		LEFT JOIN catalog_details_language l ON l.kind = i.kind AND l.tmdb = i.tmdb
		WHERE i.kind IN ('movie', 'series') AND i.title_key IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		f := &relatedFacts{}
		var language any
		var genres *string
		if err := rows.Scan(&f.key, &f.kind, &f.year, &f.rating, &language, &genres); err != nil {
			rows.Close()
			return nil, err
		}
		switch l := language.(type) {
		case string:
			f.language = l
		case []byte:
			f.language = string(l)
		}
		if titles[f.key] != nil {
			continue // two copies: the first one's
		}
		if genres != nil {
			for _, g := range strings.Split(*genres, ",") {
				if g != "" {
					f.features = append(f.features, "g:"+g)
				}
			}
		}
		titles[f.key] = f
		order = append(order, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Its tags and people, over all its copies.
	for _, q := range []struct{ prefix, sql string }{
		{"t:", `SELECT DISTINCT i.title_key, lower(t.tag) FROM catalog_tags t JOIN catalog_items i ON i.id = t.item_id
			WHERE i.kind IN ('movie', 'series') AND i.title_key IS NOT NULL`},
		{"p:", `SELECT DISTINCT i.title_key, p.person_id FROM catalog_people p JOIN catalog_items i ON i.id = p.item_id
			WHERE i.kind IN ('movie', 'series') AND i.title_key IS NOT NULL`},
	} {
		rows, err := s.db.QueryContext(ctx, q.sql)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				rows.Close()
				return nil, err
			}
			if f := titles[key]; f != nil && value != "" {
				f.features = append(f.features, q.prefix+value)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	df := map[string]map[string]int{}
	count := map[string]int{}
	for _, f := range order {
		slices.Sort(f.features)
		f.features = slices.Compact(f.features)
		if df[f.kind] == nil {
			df[f.kind] = map[string]int{}
		}
		count[f.kind]++
		for _, x := range f.features {
			df[f.kind][x]++
		}
		if f.language != "" {
			df[f.kind]["l:"+f.language]++
		}
	}
	c.titles, c.byKey, c.df, c.count, c.at = order, titles, df, count, s.now()
	return c, nil
}
