package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/textclean"
)

// This server's own catalog, as the MSFP engine reads it to answer paired
// servers (msfp.Catalog and msfp.ChangeLog): its libraries, the titles
// the index holds in them by their TMDB keys (films and series only:
// Overfin shares no music), their copies, and the change log the
// database's triggers keep (title_changes). The engine filters by what
// is offered, pages and checks; this only reads.

// MSFPCatalog is the catalog on this database.
type MSFPCatalog struct{ S *Store }

var (
	_ msfp.Catalog   = MSFPCatalog{}
	_ msfp.ChangeLog = MSFPCatalog{}
	_ msfp.SeqBumper = MSFPCatalog{}
)

// The library kinds of video: those this server's catalog lists to
// paired servers. A library of music, books or photos holds nothing
// that is shared.
var notVideo = []string{"music", "books", "photos", "playlists", "livetv"}

// Libraries lists the index's libraries of video, by Jellyfin's IDs (the
// engine gives them their public IDs: Jellyfin's never leaves).
func (c MSFPCatalog) Libraries(ctx context.Context) ([]msfp.Library, error) {
	libs, err := c.S.Libraries(ctx)
	if err != nil {
		return nil, err
	}
	out := []msfp.Library{}
	for _, l := range libs {
		var item struct{ Name, CollectionType string }
		json.Unmarshal(l.Item, &item)
		kind := strings.ToLower(item.CollectionType)
		if slices.Contains(notVideo, kind) {
			continue
		}
		out = append(out, msfp.Library{ID: l.ID, Name: textclean.Plain(item.Name, 64)})
	}
	return out, nil
}

// Titles lists, in key order after after, at most limit of the identified
// titles the libraries hold (films, series, seasons, episodes, by their
// TMDB keys: files TMDB doesn't know are left out), each once, with the
// libraries of those holding it.
func (c MSFPCatalog) Titles(ctx context.Context, libraries []string, after string, limit int) (msfp.TitlePage, error) {
	titles, more, err := c.S.titlesIn(ctx, libraries, nil, after, limit)
	return msfp.TitlePage{Titles: titles, More: more}, err
}

// OwnCopy is one of this server's copies of a film or an episode, as
// Copies lists it: its handle (the Jellyfin item's ID), its library, its
// picture's neutral name ("2160p HDR · HEVC") and its audio languages.
//
// It was msfp.Copy, which msfp-go's revised v0 removed (titles carry
// their copies, wire.Copy). Kept, unused, until the federation phase
// (plan phase 7) builds each title's copies from the copy index.
type OwnCopy struct {
	Handle, Library, Name string
	Audio                 []string
}

// Copies lists this server's copies of a film or an episode in the
// libraries: each a Jellyfin item, an episode a file holds after its
// first being that file's item (range_of), what plays. Its handle is the
// item's ID; its name, the copy's video as a neutral name ("2160p HDR ·
// HEVC"). The best first, as the engine ranks pictures (MSFP video
// section 3.2: the greatest height, then HDR): play without a version
// plays the copy the title's picture describes.
func (c MSFPCatalog) Copies(ctx context.Context, key string, libraries []string) ([]OwnCopy, error) {
	if len(libraries) == 0 {
		return nil, nil
	}
	var out []OwnCopy
	var pictures []Picture
	err := c.S.each(ctx, func(rows *sql.Rows) error {
		var cp OwnCopy
		var audio, picture sql.NullString
		if err := rows.Scan(&cp.Handle, &audio, &cp.Library, &cp.Name, &picture); err != nil {
			return err
		}
		cp.Audio = mergeTags(nil, audio.String)
		out = append(out, cp)
		p, _ := readPicture(picture)
		pictures = append(pictures, p)
		return nil
	}, `SELECT coalesce(i.range_of, i.id) AS item, min(i.audio_tags), min(i.library_id), coalesce(min(f.video), 'Version'), min(f.picture)
		FROM catalog_items i LEFT JOIN catalog_items f ON f.id = coalesce(i.range_of, i.id)
		WHERE i.title_key = ? AND i.kind IN ('movie', 'episode') AND i.library_id IN (SELECT value FROM json_each(?))
		GROUP BY item ORDER BY item`, key, jsonOrEmpty(libraries))
	if err != nil {
		return nil, err
	}
	// Stable: copies alike stay in the order of their IDs.
	order := make([]int, len(out))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		switch {
		case pictures[a].Better(pictures[b]):
			return -1
		case pictures[b].Better(pictures[a]):
			return 1
		}
		return 0
	})
	sorted := make([]OwnCopy, len(out))
	for i, j := range order {
		sorted[i] = out[j]
	}
	return sorted, nil
}

// Seq is the number of the last change noted in the catalog.
func (c MSFPCatalog) Seq(ctx context.Context) (int64, error) {
	head, _, err := c.S.TitleChanges(ctx)
	return head, err
}

// BumpSeq takes a change number with no title changed (a key no reading
// lists, item/offers), after a change of what a paired server sees: its
// readings are of changes again after one whole reading.
func (c MSFPCatalog) BumpSeq(ctx context.Context) (int64, error) {
	var seq int64
	err := c.S.db.QueryRowContext(ctx, `INSERT INTO title_changes (title_key, at) VALUES ('item/offers', unixepoch()) RETURNING seq`).Scan(&seq)
	return seq, err
}

// Changes gives what changed in the libraries after since, in key order
// after after: the titles they hold now, and the keys they no longer do
// (the engine tells a peer only of those it was sent). msfp.ErrTooOld
// when the log no longer reaches back to since.
func (c MSFPCatalog) Changes(ctx context.Context, libraries []string, since int64, after string, limit int) (msfp.ChangePage, error) {
	_, floor, err := c.S.TitleChanges(ctx)
	if err != nil {
		return msfp.ChangePage{}, err
	}
	if since < floor {
		return msfp.ChangePage{}, msfp.ErrTooOld
	}
	var keys []string
	err = c.S.each(ctx, func(rows *sql.Rows) error {
		var k string
		if err := rows.Scan(&k); err != nil {
			return err
		}
		keys = append(keys, k)
		return nil
	}, `SELECT DISTINCT title_key FROM title_changes
		WHERE seq > ? AND title_key > ? AND title_key NOT LIKE 'item/%' ORDER BY title_key LIMIT ?`, since, after, limit)
	if err != nil || len(keys) == 0 {
		return msfp.ChangePage{}, err
	}
	present, _, err := c.S.titlesIn(ctx, libraries, keys, "", len(keys))
	if err != nil {
		return msfp.ChangePage{}, err
	}
	held := map[string]bool{}
	for _, t := range present {
		held[t.Common().Key] = true
	}
	page := msfp.ChangePage{Present: present, Last: keys[len(keys)-1], More: len(keys) == limit}
	for _, k := range keys {
		if !held[k] {
			page.Gone = append(page.Gone, k)
		}
	}
	return page, nil
}

// titlesIn lists the titles the libraries hold, of keys only when keys
// isn't nil (TitlesInLibraries). more says whether it looked at limit
// titles: there may be more after the last.
func (s *Store) titlesIn(ctx context.Context, libraries, keys []string, after string, limit int) ([]msfp.Title, bool, error) {
	out := []msfp.Title{}
	if len(libraries) == 0 {
		return out, false, nil
	}
	libs := jsonOrEmpty(libraries)
	args := []any{after, libs}
	only := ""
	if keys != nil {
		only = ` AND c.title_key IN (SELECT value FROM json_each(?))`
		args = append(args, jsonOrEmpty(keys))
	}
	args = append(args, limit)
	// Only keys of TMDB's (MSFP video, section 4): a title Overfin knows
	// by another source's ID isn't sent.
	looked := 0
	var videos []*msfp.VideoTitle
	err := s.each(ctx, func(rows *sql.Rows) error {
		var v msfp.VideoTitle
		var held string
		if err := rows.Scan(&v.Key, &v.Kind, &held); err != nil {
			return err
		}
		looked++
		if err := json.Unmarshal([]byte(held), &v.Libraries); err != nil {
			return err
		}
		slices.Sort(v.Libraries)
		v.Libraries = slices.Compact(v.Libraries)
		// In MSFP's one form only: a key in another would be one more
		// title there, which nobody can play.
		if k, ok := wire.ParseVideoKey(v.Key); ok && k.Kind == v.Kind {
			videos = append(videos, &v)
		}
		return nil
	}, `SELECT c.title_key, min(c.kind), json_group_array(c.library_id) FROM catalog_items c
		WHERE c.title_key > ? AND c.library_id IN (SELECT value FROM json_each(?)) AND c.kind IN ('movie', 'series', 'season', 'episode')
			AND (c.title_key GLOB 'movie/tmdb/*' OR c.title_key GLOB 'series/tmdb/*')`+only+`
		GROUP BY c.title_key ORDER BY c.title_key LIMIT ?`, args...)
	if err != nil {
		return nil, false, err
	}
	// Each film's and episode's copies: msfp-go's revised v0 sends them
	// on the title (VideoTitle.Copies, wire.Copy), in place of the
	// languages and pictures this read before. Federation is off on this
	// branch until plan phase 7, which fills them from the copy index;
	// until then titles leave without copies.
	if err := s.addWith(ctx, videos, libs); err != nil {
		return nil, false, err
	}
	if err := s.addOtherIDs(ctx, videos); err != nil {
		return nil, false, err
	}
	if err := s.addTMDBPlaces(ctx, videos); err != nil {
		return nil, false, err
	}
	for _, v := range videos {
		out = append(out, v)
	}
	return out, looked == limit, nil
}

// addWith gives each episode of titles the other episodes its copies in
// the libraries (a JSON list) hold: those of the same file (range_of).
func (s *Store) addWith(ctx context.Context, titles []*msfp.VideoTitle, libs string) error {
	at := map[string]*msfp.VideoTitle{}
	var episodes []string
	for _, t := range titles {
		if t.Kind == "episode" {
			at[t.Key] = t
			episodes = append(episodes, t.Key)
		}
	}
	if len(episodes) == 0 {
		return nil
	}
	// The copies of these episodes that hold several, and every episode
	// key of the same file: its first (the item) and the rest (range_of).
	err := s.each(ctx, func(rows *sql.Rows) error {
		var key, other string
		if err := rows.Scan(&key, &other); err != nil {
			return err
		}
		t := at[key]
		if k, ok := wire.ParseVideoKey(other); ok && k.Kind == "episode" && other != key && !slices.Contains(t.With, other) {
			t.With = append(t.With, other)
		}
		return nil
	}, `WITH files AS (
			SELECT c.title_key AS key, coalesce(c.range_of, c.id) AS file FROM catalog_items c
			WHERE c.kind = 'episode' AND c.title_key IN (SELECT value FROM json_each(?)) AND c.library_id IN (SELECT value FROM json_each(?))
				AND (c.range_of IS NOT NULL OR EXISTS (SELECT 1 FROM catalog_items r WHERE r.range_of = c.id)))
		SELECT f.key, o.title_key FROM files f JOIN catalog_items o ON o.id = f.file OR o.range_of = f.file
		WHERE o.title_key IS NOT NULL`, jsonOrEmpty(episodes), libs)
	if err != nil {
		return err
	}
	for _, t := range titles {
		if len(t.With) > 0 {
			slices.Sort(t.With)
			t.With = t.With[:min(len(t.With), wire.MaxWith)]
		}
	}
	return nil
}

// addTMDBPlaces gives each episode keyed by its TMDB ID its place in TMDB's
// numbering (MSFP video section 4.1): own_episodes holds TMDB's own order,
// whatever order an admin chose to show the series in. An episode not found
// there gets none, and the engine leaves it out.
func (s *Store) addTMDBPlaces(ctx context.Context, titles []*msfp.VideoTitle) error {
	at := map[string]*msfp.VideoTitle{}
	var keys [][]any
	for _, t := range titles {
		if k, ok := wire.ParseVideoKey(t.Key); ok && k.Kind == "episode" && k.EpisodeID > 0 {
			keys = append(keys, []any{t.Key, strconv.FormatInt(k.ID, 10), k.EpisodeID})
			at[t.Key] = t
		}
	}
	if len(keys) == 0 {
		return nil
	}
	b, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	return s.each(ctx, func(rows *sql.Rows) error {
		var key string
		var p msfp.Place
		if err := rows.Scan(&key, &p.Season, &p.Episode); err != nil {
			return err
		}
		at[key].Place = &p
		return nil
	}, `SELECT j.value ->> 0, e.season, e.episode FROM json_each(?) j
		JOIN own_episodes e ON e.series = j.value ->> 1 AND e.tmdb_id = j.value ->> 2`, string(b))
}

// addOtherIDs gives films, series and episodes their other IDs, as TMDB
// gives them: a film's or a series' IMDb and TheTVDB IDs, an episode's
// IMDb ID (only those known by TMDB's ID of it).
func (s *Store) addOtherIDs(ctx context.Context, titles []*msfp.VideoTitle) error {
	// Each looked for by its table's key, [key, kind or series, TMDB ID],
	// so that it's found by index.
	at := map[string]*msfp.VideoTitle{}
	var titleKeys, episodeKeys [][]any
	for _, t := range titles {
		k, _ := wire.ParseVideoKey(t.Key)
		switch {
		case k.Kind == "movie" || k.Kind == "series":
			titleKeys = append(titleKeys, []any{t.Key, k.Kind, strconv.FormatInt(k.ID, 10)})
		case k.Kind == "episode" && k.EpisodeID > 0:
			episodeKeys = append(episodeKeys, []any{t.Key, strconv.FormatInt(k.ID, 10), k.EpisodeID})
		default:
			continue
		}
		at[t.Key] = t
	}
	list := func(l [][]any) string {
		b, _ := json.Marshal(l)
		return string(b)
	}
	add := func(key, scheme string, value sql.NullString) {
		if !value.Valid || !wire.OtherID(scheme, value.String) {
			return
		}
		t := at[key]
		if t.IDs == nil {
			t.IDs = map[string]string{}
		}
		t.IDs[scheme] = value.String
	}
	if len(titleKeys) > 0 {
		err := s.each(ctx, func(rows *sql.Rows) error {
			var key string
			var imdb, tvdb sql.NullString
			if err := rows.Scan(&key, &imdb, &tvdb); err != nil {
				return err
			}
			add(key, "imdb", imdb)
			add(key, "tvdb", tvdb)
			return nil
		}, `SELECT j.value ->> 0, t.imdb, t.tvdb FROM json_each(?) j
			JOIN catalog_titles t ON t.kind = j.value ->> 1 AND t.tmdb = j.value ->> 2`, list(titleKeys))
		if err != nil {
			return err
		}
	}
	if len(episodeKeys) > 0 {
		err := s.each(ctx, func(rows *sql.Rows) error {
			var key string
			var imdb sql.NullString
			if err := rows.Scan(&key, &imdb); err != nil {
				return err
			}
			add(key, "imdb", imdb)
			return nil
		}, `SELECT j.value ->> 0, i.imdb FROM json_each(?) j
			JOIN episode_imdb i ON i.series = j.value ->> 1 AND i.tmdb_id = j.value ->> 2
			WHERE i.imdb IS NOT NULL`, list(episodeKeys))
		if err != nil {
			return err
		}
	}
	return nil
}
