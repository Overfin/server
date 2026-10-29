package store

import (
	"context"
	"strings"
)

// infoBatch is what TitleInfos reads at once for the films and series it's
// asked for, in a few queries for all of them instead of a few for each:
// their copies, pages, artwork, ratings and ages. A title not in it is read
// on its own, as before.
type infoBatch struct {
	keys   map[string]bool              // the titles read
	items  map[string][]string          // title key → library items, by ID
	titles map[string]*Title            // title key → its row (absent: none)
	art    map[string]map[string]string // title key → type → path
	cert   map[string]string            // title key → certification in the country
	age    map[string]*int              // title key → age (absent: unknown)
}

// batchChunk is how many titles one query asks for at most.
const batchChunk = 400

// readBatch reads the films and series among refs, with their ratings in
// country.
func (s *Store) readBatch(ctx context.Context, refs []TitleRef, country string) (*infoBatch, error) {
	b := &infoBatch{keys: map[string]bool{}, items: map[string][]string{}, titles: map[string]*Title{},
		cert: map[string]string{}, age: map[string]*int{}}
	byKind := map[string][]string{}
	var keys []string
	for _, r := range refs {
		if (r.Kind != "movie" && r.Kind != "series") || r.TMDB() == "" || b.keys[r.Key()] {
			continue
		}
		b.keys[r.Key()] = true
		keys = append(keys, r.Key())
		byKind[r.Kind] = append(byKind[r.Kind], r.TMDB())
	}
	if len(keys) == 0 {
		return b, nil
	}
	for start := 0; start < len(keys); start += batchChunk {
		chunk := keys[start:min(start+batchChunk, len(keys))]
		rows, err := s.read.QueryContext(ctx, `SELECT title_key, id FROM catalog_items WHERE title_key IN (`+marks(len(chunk))+`)
			ORDER BY id`, anys(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key, id string
			if err := rows.Scan(&key, &id); err != nil {
				rows.Close()
				return nil, err
			}
			b.items[key] = append(b.items[key], id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	for kind, ids := range byKind {
		for start := 0; start < len(ids); start += batchChunk {
			chunk := ids[start:min(start+batchChunk, len(ids))]
			args := append([]any{kind}, anys(chunk)...)
			rows, err := s.read.QueryContext(ctx, `SELECT `+titleColumns+` FROM catalog_titles t
				WHERE t.kind = ? AND t.tmdb IN (`+marks(len(chunk))+`)`, args...)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				t, err := scanTitle(rows)
				if err != nil {
					rows.Close()
					return nil, err
				}
				b.titles[t.Kind+"/tmdb/"+t.TMDB] = t
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
			// The certification in the country: the one its scale puts
			// highest, as certification() picks it.
			args = append([]any{kind, country}, anys(chunk)...)
			rows, err = s.read.QueryContext(ctx, `SELECT r.tmdb, r.certification FROM title_ratings r
				LEFT JOIN rating_scales sc ON sc.kind = r.kind AND sc.country = r.country AND sc.certification = r.certification
				WHERE r.kind = ? AND r.country = ? AND r.tmdb IN (`+marks(len(chunk))+`)
				ORDER BY r.tmdb, coalesce(sc.position, -1) DESC, r.certification`, args...)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var tmdb, c string
				if err := rows.Scan(&tmdb, &c); err != nil {
					rows.Close()
					return nil, err
				}
				if _, seen := b.cert[kind+"/tmdb/"+tmdb]; !seen {
					b.cert[kind+"/tmdb/"+tmdb] = c
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
			rows, err = s.read.QueryContext(ctx, `SELECT tmdb, age FROM title_ages WHERE kind = ? AND tmdb IN (`+marks(len(chunk))+`)`,
				append([]any{kind}, anys(chunk)...)...)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var tmdb string
				var age int
				if err := rows.Scan(&tmdb, &age); err != nil {
					rows.Close()
					return nil, err
				}
				b.age[kind+"/tmdb/"+tmdb] = &age
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
		}
	}
	art, err := s.Artwork(ctx, keys)
	if err != nil {
		return nil, err
	}
	b.art = art
	return b, nil
}

func anys(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// has says whether the batch read the title at key.
func (b *infoBatch) has(key string) bool {
	return b != nil && b.keys[key]
}

// keyOf is a film's or a series' key.
func keyOf(kind, tmdb string) string {
	return strings.Join([]string{kind, "tmdb", tmdb}, "/")
}
