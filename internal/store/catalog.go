package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/msfp-io/msfp-go"
)

// ---- Jellyfin service account ---------------------------------------------------

// The service account: an ordinary Jellyfin user of Overfin's own, never an
// administrator, that reads the whole library for the catalog. Its token is
// sealed, bound to its row.
const serviceTokenContext = "jellyfin service token"

// ServiceAccount is Overfin's Jellyfin service account.
type ServiceAccount struct {
	UserID   string
	Username string
	Token    string
}

// ErrServiceToken: the service account's token was sealed with another
// secret key (one that was lost). Service then returns the account without
// its token, for a new one.
var ErrServiceToken = errors.New("store: the service account's token can't be read with this secret key")

// Service returns the service account, or nil before it's made.
func (s *Store) Service(ctx context.Context) (*ServiceAccount, error) {
	var raw, sealed []byte
	err := s.read.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'service-account'`).Scan(&raw)
	if err == nil {
		err = s.read.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'service-token'`).Scan(&sealed)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var a ServiceAccount
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("store: the service account: %w", err)
	}
	token, err := s.box.Open(sealed, serviceTokenContext)
	if err != nil {
		return &a, ErrServiceToken
	}
	a.Token = string(token)
	return &a, nil
}

// SetService saves the service account, its token sealed.
func (s *Store) SetService(ctx context.Context, a ServiceAccount) error {
	raw, err := json.Marshal(struct{ UserID, Username string }{a.UserID, a.Username})
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for name, value := range map[string][]byte{
		"service-account": raw,
		"service-token":   s.box.Seal([]byte(a.Token), serviceTokenContext),
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES (?, ?)
			ON CONFLICT (name) DO UPDATE SET value = excluded.value`, name, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- Catalog --------------------------------------------------------------------

// CatalogItem is one title of the library, as the catalog indexes it.
type CatalogItem struct {
	ID            string
	Kind          string // movie, series, season, episode
	Name          string
	SortName      string
	Year          int // 0 when unknown
	SeriesID      string
	SeasonID      string
	SeasonNumber  *int
	EpisodeNumber *int
	TMDB          string
	IMDb          string
	TVDB          string
	Rating        string
	PrimaryTag    string
	BackdropTag   string
	ThumbTag      string
	// For listings: the library it's in (empty: unchanged), when it was
	// added, its release date (YYYY-MM-DD) and its rating score.
	LibraryID       string
	AddedAt         time.Time
	PremiereDate    string
	CommunityRating *float64
	// FolderName and FileName are the last names of where it is (a film's
	// folder and file, a series' folder); RuntimeMs, a film's length.
	FolderName, FileName string
	RuntimeMs            int64
	// AudioLanguages are a film's or an episode's audio languages, two
	// letters each.
	AudioLanguages []string
	// AudioTags and SubtitleTags are its languages as the player lists
	// them: main audio tracks (with a region when known, "fr-CA") and
	// subtitles, forced ones aside.
	AudioTags, SubtitleTags []string
	// Video is its picture's neutral name ("2160p HDR · HEVC"), as the
	// player names versions; "" when unknown.
	Video string
	// Height and HDR are its picture for paired servers (MSFP video
	// section 3.2, playback.Picture); kept stepped, as sent. Height 0:
	// unknown.
	Height int
	HDR    []string
	// EpisodeNumberEnd is the last episode a file holding several holds
	// (S01E01-E02: 2); nil for one.
	EpisodeNumberEnd *int
}

// pictureJSON is a copy's picture as kept, stepped as paired servers are
// told of it (msfp.BestPicture): NULL when its size isn't known.
func pictureJSON(height int, hdr []string) any {
	p, ok := msfp.BestPicture([]msfp.Picture{{Height: height, HDR: hdr}})
	if !ok {
		return nil
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// rangeID is the index row of an episode n held by item, after its first:
// derived from the item's ID, so it stays the same pass after pass.
func rangeID(item string, n int) string {
	sum := sha256.Sum256([]byte(item + ":" + strconv.Itoa(n)))
	return hex.EncodeToString(sum[:16])
}

// maxRange bounds how many episodes one file may hold.
const maxRange = 20

// PutCatalog adds or updates titles, marking them seen by the full pass
// pass (the current one, for changes read between passes).
func (s *Store) PutCatalog(ctx context.Context, items []CatalogItem, pass int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO catalog_items (id, kind, name, sort_name, year,
		series_id, season_id, season_number, episode_number, tmdb, imdb, tvdb, rating,
		primary_tag, backdrop_tag, thumb_tag, seen, library_id, added_at, premiere_date, community_rating,
		folder_name, file_name, runtime_ms, jellyfin_tmdb, audio_languages, audio_tags, subtitle_tags, video, picture)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, name = excluded.name,
			sort_name = excluded.sort_name, year = excluded.year, series_id = excluded.series_id,
			season_id = excluded.season_id, season_number = excluded.season_number,
			episode_number = excluded.episode_number, tmdb = excluded.tmdb, imdb = excluded.imdb,
			tvdb = excluded.tvdb, rating = excluded.rating, primary_tag = excluded.primary_tag,
			backdrop_tag = excluded.backdrop_tag, thumb_tag = excluded.thumb_tag, seen = excluded.seen,
			library_id = coalesce(excluded.library_id, library_id), added_at = excluded.added_at,
			premiere_date = excluded.premiere_date, community_rating = excluded.community_rating,
			folder_name = excluded.folder_name, file_name = excluded.file_name, runtime_ms = excluded.runtime_ms,
			jellyfin_tmdb = excluded.jellyfin_tmdb, audio_languages = excluded.audio_languages,
			audio_tags = excluded.audio_tags, subtitle_tags = excluded.subtitle_tags, video = excluded.video,
			picture = excluded.picture`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, it := range items {
		var added any
		if !it.AddedAt.IsZero() {
			added = it.AddedAt.Unix()
		}
		// A film or a season the index didn't have arrived (title_history).
		arrived := false
		if it.Kind == "movie" || it.Kind == "season" {
			var one int
			arrived = errors.Is(tx.QueryRowContext(ctx, `SELECT 1 FROM catalog_items WHERE id = ?`, it.ID).Scan(&one), sql.ErrNoRows)
		}
		tmdb := s.foundTMDB(ctx, tx, it)
		// A film's new copy, or its file replaced (an upgrade: a new item),
		// while another copy of it is here: it didn't arrive.
		if arrived && it.Kind == "movie" && tmdb != "" {
			var other int
			if tx.QueryRowContext(ctx, `SELECT 1 FROM catalog_items WHERE kind = 'movie' AND tmdb = ? AND id <> ? LIMIT 1`, tmdb, it.ID).Scan(&other) == nil {
				arrived = false
			}
		}
		if _, err := stmt.ExecContext(ctx, it.ID, it.Kind, it.Name, it.SortName, nullInt(it.Year),
			nullString(it.SeriesID), nullString(it.SeasonID), it.SeasonNumber, it.EpisodeNumber,
			nullString(tmdb), nullString(it.IMDb), nullString(it.TVDB), it.Rating,
			nullString(it.PrimaryTag), nullString(it.BackdropTag), nullString(it.ThumbTag), pass,
			nullString(it.LibraryID), added, nullString(it.PremiereDate), it.CommunityRating,
			nullString(it.FolderName), nullString(it.FileName), nullInt64(it.RuntimeMs), nullString(it.TMDB),
			jsonList(it.AudioLanguages), jsonList(it.AudioTags), jsonList(it.SubtitleTags), nullString(it.Video),
			pictureJSON(it.Height, it.HDR)); err != nil {
			return err
		}
		if arrived {
			at := time.Now()
			if !it.AddedAt.IsZero() {
				at = it.AddedAt
			}
			if err := recordHistory(ctx, tx, "arrived", it.Kind, it.ID, it.SeriesID, it.SeasonNumber, tmdb, it.TVDB, at); err != nil {
				return err
			}
		}
		// A file of several episodes: a row for each one after its first,
		// as the item is, pointing to it. Kept from one pass to the next
		// (their title key too, which RefreshTitleKeys sets): deleted and
		// made again, each would be told to paired servers as changed, and
		// go missing from listings until the keys are set again.
		if it.Kind == "episode" {
			first, last := 0, -1
			if it.EpisodeNumber != nil && it.EpisodeNumberEnd != nil {
				first, last = *it.EpisodeNumber+1, min(*it.EpisodeNumberEnd, *it.EpisodeNumber+maxRange)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_items WHERE range_of = ? AND NOT (episode_number BETWEEN ? AND ?)`,
				it.ID, first, last); err != nil {
				return err
			}
			for n := first; n <= last; n++ {
				if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_items (id, kind, name, sort_name, year,
						series_id, season_id, season_number, episode_number, tmdb, imdb, tvdb, rating,
						primary_tag, backdrop_tag, thumb_tag, seen, library_id, added_at, premiere_date, community_rating,
						folder_name, file_name, runtime_ms, jellyfin_tmdb, audio_languages, audio_tags, subtitle_tags, video, picture, range_of)
					SELECT ?, kind, name, sort_name, year, series_id, season_id, season_number, ?, tmdb, imdb, tvdb, rating,
						primary_tag, backdrop_tag, thumb_tag, seen, library_id, added_at, premiere_date, community_rating,
						folder_name, file_name, runtime_ms, jellyfin_tmdb, audio_languages, audio_tags, subtitle_tags, video, picture, id
					FROM catalog_items WHERE id = ? AND true
					ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, name = excluded.name, sort_name = excluded.sort_name,
						year = excluded.year, series_id = excluded.series_id, season_id = excluded.season_id,
						season_number = excluded.season_number, episode_number = excluded.episode_number, tmdb = excluded.tmdb,
						imdb = excluded.imdb, tvdb = excluded.tvdb, rating = excluded.rating, primary_tag = excluded.primary_tag,
						backdrop_tag = excluded.backdrop_tag, thumb_tag = excluded.thumb_tag, seen = excluded.seen,
						library_id = excluded.library_id, added_at = excluded.added_at, premiere_date = excluded.premiere_date,
						community_rating = excluded.community_rating, folder_name = excluded.folder_name,
						file_name = excluded.file_name, runtime_ms = excluded.runtime_ms, jellyfin_tmdb = excluded.jellyfin_tmdb,
						audio_languages = excluded.audio_languages, audio_tags = excluded.audio_tags,
						subtitle_tags = excluded.subtitle_tags, video = excluded.video, picture = excluded.picture,
						range_of = excluded.range_of`,
					rangeID(it.ID, n), n, it.ID); err != nil {
					return err
				}
			}
		}
	}
	// A season that arrived before its series was indexed gets its IDs once
	// the series is: kept with it, should both leave later.
	if _, err := tx.ExecContext(ctx, `UPDATE title_history SET
			tmdb = coalesce(tmdb, (SELECT s.tmdb FROM catalog_items s WHERE s.id = title_history.series_id)),
			tvdb = coalesce(tvdb, (SELECT s.tvdb FROM catalog_items s WHERE s.id = title_history.series_id))
		WHERE kind = 'season' AND (tmdb IS NULL OR tvdb IS NULL)
			AND series_id IN (SELECT id FROM catalog_items WHERE kind = 'series')`); err != nil {
		return err
	}
	return tx.Commit()
}

// UnseenCatalog lists the titles the full pass pass didn't see: gone from
// the library, or missed, which the caller checks before deleting them.
func (s *Store) UnseenCatalog(ctx context.Context, pass int64) ([]string, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT id FROM catalog_items WHERE seen < ?`, pass)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeleteCatalog removes titles from the index: the films and seasons
// among them left the libraries (title_history).
func (s *Store) DeleteCatalog(ctx context.Context, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The rows of a file's other episodes go with it.
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_items WHERE range_of = ?`, id); err != nil {
			return err
		}
	}
	// Read before anything goes: a season's series may be among them.
	type leaving struct {
		kind, id, seriesID, tmdb, tvdb string
		season                         *int
	}
	var left []leaving
	for _, id := range ids {
		var l leaving
		var seriesID, tmdb, tvdb sql.NullString
		var season sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT c.kind, c.series_id, c.season_number,
				CASE c.kind WHEN 'movie' THEN c.tmdb ELSE s.tmdb END,
				CASE c.kind WHEN 'movie' THEN c.tvdb ELSE s.tvdb END
			FROM catalog_items c LEFT JOIN catalog_items s ON s.id = c.series_id
			WHERE c.id = ? AND c.kind IN ('movie', 'season')`, id).Scan(&l.kind, &seriesID, &season, &tmdb, &tvdb)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		l.id, l.seriesID, l.tmdb, l.tvdb = id, seriesID.String, tmdb.String, tvdb.String
		if season.Valid {
			n := int(season.Int64)
			l.season = &n
		}
		left = append(left, l)
	}
	now := time.Now()
	for _, l := range left {
		// A film's copy going while another stays (its file replaced, a
		// second version removed): the film didn't leave.
		if l.kind == "movie" && l.tmdb != "" {
			var other int
			if tx.QueryRowContext(ctx, `SELECT 1 FROM catalog_items WHERE kind = 'movie' AND tmdb = ?
				AND id NOT IN (SELECT value FROM json_each(?)) LIMIT 1`, l.tmdb, jsonList(ids)).Scan(&other) == nil {
				continue
			}
		}
		if err := recordHistory(ctx, tx, "left", l.kind, l.id, l.seriesID, l.season, l.tmdb, l.tvdb, now); err != nil {
			return err
		}
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_items WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CatalogCounts counts the indexed titles, by kind.
func (s *Store) CatalogCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT kind, count(*) FROM catalog_items GROUP BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		out[kind] = n
	}
	return out, rows.Err()
}

// CatalogUnmatched counts, by kind, the titles that can't be matched to
// TMDB later: films and series without a TMDB ID, seasons without their
// number, episodes without their season or episode number (episodes and
// seasons are matched by their show's ID and their numbers).
func (s *Store) CatalogUnmatched(ctx context.Context) (map[string]int, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT kind, count(*) FROM catalog_items
		WHERE (kind IN ('movie', 'series') AND tmdb IS NULL)
		   OR (kind = 'season' AND season_number IS NULL)
		   OR (kind = 'episode' AND (season_number IS NULL OR episode_number IS NULL))
		GROUP BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		out[kind] = n
	}
	return out, rows.Err()
}

// CatalogState is where the catalog's sync stands, kept across restarts.
type CatalogState struct {
	// Pass counts full passes; titles carry the one that last saw them.
	Pass int64 `json:"pass"`
	// FullAt is when the last full pass started; ChangesAt, when the last
	// read of changes started (changes are read since then).
	FullAt    time.Time `json:"fullAt"`
	ChangesAt time.Time `json:"changesAt"`
}

// CatalogState returns the sync's state; zero before the first pass.
func (s *Store) CatalogState(ctx context.Context) (CatalogState, error) {
	var st CatalogState
	var raw []byte
	err := s.read.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'catalog-state'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(raw, &st)
}

// SetCatalogState saves the sync's state.
func (s *Store) SetCatalogState(ctx context.Context, st CatalogState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('catalog-state', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, raw)
	return err
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt64(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

// ---- Seerr ----------------------------------------------------------------------

// Seerr, as the control panel saved it: its address as is, its API key
// sealed, bound to its row.
const seerrKeyContext = "seerr api key"

// Seerr returns Seerr's address and API key, and false when none is saved.
func (s *Store) Seerr(ctx context.Context) (url, key string, ok bool, err error) {
	var u, sealed []byte
	err = s.read.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'seerr-url'`).Scan(&u)
	if err == nil {
		err = s.read.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'seerr-key'`).Scan(&sealed)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	k, err := s.box.Open(sealed, seerrKeyContext)
	if err != nil {
		return "", "", false, fmt.Errorf("store: Seerr's API key can't be read with this secret key: %w", err)
	}
	return string(u), string(k), true, nil
}

// SetSeerr saves Seerr's address and API key.
func (s *Store) SetSeerr(ctx context.Context, url, key string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for name, value := range map[string][]byte{
		"seerr-url": []byte(url),
		"seerr-key": s.box.Seal([]byte(key), seerrKeyContext),
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES (?, ?)
			ON CONFLICT (name) DO UPDATE SET value = excluded.value`, name, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ForgetSeerr removes Seerr's settings: Overfin no longer uses it.
func (s *Store) ForgetSeerr(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE name IN ('seerr-url', 'seerr-key')`)
	return err
}

// CatalogByTMDB finds library titles of a kind (movie, series) by their
// TMDB IDs: TMDB ID → item ID, for those the library has.
func (s *Store) CatalogByTMDB(ctx context.Context, kind string, tmdbIDs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, tmdb := range tmdbIDs {
		var id string
		err := s.read.QueryRowContext(ctx, `SELECT id FROM catalog_items WHERE kind = ? AND tmdb = ? ORDER BY id LIMIT 1`,
			kind, tmdb).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[tmdb] = id
	}
	return out, nil
}

// CatalogEpisodes counts a series' episodes in the library, by season
// number.
func (s *Store) CatalogEpisodes(ctx context.Context, seriesID string) (map[int]int, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT season_number, count(*) FROM catalog_items
		WHERE kind = 'episode' AND series_id = ? AND season_number IS NOT NULL GROUP BY season_number`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]int{}
	for rows.Next() {
		var season, n int
		if err := rows.Scan(&season, &n); err != nil {
			return nil, err
		}
		out[season] = n
	}
	return out, rows.Err()
}

// ListQuery is a library listing answered from the index.
type ListQuery struct {
	LibraryID string
	// ParentID, when set, lists a series' seasons or episodes, or a
	// season's episodes.
	ParentID string
	Kinds    []string // index kinds; empty: any
	// Genres (any of them, TMDB's), YearFrom and YearTo (0: no bound)
	// filter by what TMDB says of the title.
	Genres   []string
	YearFrom int
	YearTo   int
	// ProfileID, with Played, Favorite or InProgress, filters by what that
	// profile did with the titles.
	ProfileID  string
	Played     *bool
	Favorite   *bool
	InProgress bool
	// Access, when set, keeps only what the profile may see.
	Access *Access
	// Category, when set, keeps only its titles.
	Category *CategoryFilter
	// Sort is "name", "premiereDate", "addedAt", "communityRating", "rank",
	// "latest" (when the title or, for a series, its newest episode was
	// added; a film, when it first came, not its copy replaced) or "lastPlayed" (by the profile's ProfileID). Names, dates and
	// ratings are TMDB's, the index's only for a file TMDB doesn't know. A
	// Limit below 0 lists everything, for shuffling.
	Sort       string
	Descending bool
	Offset     int
	Limit      int
}

// fromTMDB is a column of a listed film's or series' TMDB page, in SQL
// (NULL for seasons, episodes, and files TMDB doesn't know): sort_name,
// release_date, rating or year, read from their index (SQLite would
// rather go through the table's own key, and read each page whole).
func fromTMDB(column string) string {
	return `(SELECT t.` + column + ` FROM catalog_titles t INDEXED BY catalog_titles_listing
		WHERE t.kind = catalog_items.kind AND t.tmdb = catalog_items.tmdb)`
}

// newestOfSeries is when the newest of a series' seasons and episodes
// arrived, in SQL (NULL for anything else), read from the series index.
const newestOfSeries = `(SELECT max(e.added_at) FROM catalog_items e WHERE e.series_id = catalog_items.id)`

// filmArrivedAt is when a film last arrived (title_history), in SQL (NULL
// for anything else): a copy replacing another (an upgrade, a new file
// name) is a new item with a new date, but the film didn't arrive again.
const filmArrivedAt = `(SELECT max(h.at) FROM title_history h
	WHERE h.kind = 'movie' AND h.event = 'arrived' AND h.tmdb = catalog_items.tmdb AND catalog_items.kind = 'movie')`

var listOrder = map[string]string{
	"name":            "coalesce(" + fromTMDB("sort_name") + ", lower(sort_name))",
	"premiereDate":    "coalesce(" + fromTMDB("release_date") + ", premiere_date)",
	"addedAt":         "added_at",
	"communityRating": "coalesce(" + fromTMDB("rating") + ", community_rating)",
	// Its rank on the server (title_ranks), 1 the most valued.
	"rank": "(SELECT r.rank FROM title_ranks r WHERE r.title_key = catalog_items.title_key)",
	// The later of its own date and its episodes' (either may be NULL,
	// which makes max of the two NULL); a film's own is the earlier of its
	// item's and its arrival's, as the valuation takes it.
	"latest": "coalesce(max(coalesce(min(added_at, " + filmArrivedAt + "), added_at), " + newestOfSeries + "), " +
		"coalesce(min(added_at, " + filmArrivedAt + "), added_at), " + newestOfSeries + ")",
}

// PeerItemPrefix starts the IDs listings give the films and series paired
// servers hold for us: "peer:" + the peer's ID + ":" + the title's key.
const PeerItemPrefix = "peer:"

// listedRows are the rows a listing reads, in SQL, under the name
// catalog_items: the index's items and, when paired servers hold films
// and series for us in the libraries of our menu (ROADMAP.md milestone 6),
// one row for each of those too (PeerItemPrefix), with what listings read
// of a row. Only those TMDB's page of is kept: there's nothing else to
// show of them. Never for a series' seasons or episodes, nor for a profile
// limited to some libraries or categories (rule 42: only what they have).
func (s *Store) listedRows(ctx context.Context, q ListQuery) (string, error) {
	if q.ParentID != "" || q.Access.Limited() || (len(q.Kinds) > 0 && !slices.Contains(q.Kinds, "movie") && !slices.Contains(q.Kinds, "series")) {
		return "catalog_items", nil
	}
	var any bool
	if err := s.read.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM peer_titles)`).Scan(&any); err != nil || !any {
		return "catalog_items", err
	}
	return `(SELECT id, kind, title_key, tmdb, library_id, series_id, season_id, sort_name, added_at, premiere_date, community_rating, year
			FROM catalog_items
		UNION ALL SELECT '` + PeerItemPrefix + `' || t.peer_id || ':' || t.title_key, t.kind, t.title_key, substr(t.title_key, length(t.kind) + 7),
			w.menu_id, NULL, NULL, t.title_key, t.added_at, NULL, NULL, NULL
		FROM peer_titles t JOIN peers p ON p.id = t.peer_id AND p.state = 'paired' AND p.suspended_at IS NULL
		JOIN peer_wants w ON w.peer_id = t.peer_id AND w.library_id = t.library_id
		WHERE t.kind IN ('movie', 'series') AND t.title_key LIKE t.kind || '/tmdb/%'
			AND EXISTS (SELECT 1 FROM catalog_titles c WHERE c.kind = t.kind AND c.tmdb = substr(t.title_key, length(t.kind) + 7)
				AND c.details IS NOT NULL)) AS catalog_items`, nil
}

// ListCatalog returns the item IDs of a listing's page, in order, a
// paired server's title by a PeerItemPrefix ID. What's missing a sort
// value comes last, whichever the order.
func (s *Store) ListCatalog(ctx context.Context, q ListQuery) ([]string, error) {
	order, ok := listOrder[q.Sort]
	var orderArgs []any
	if q.Sort == "lastPlayed" {
		// When the profile last played the title, or any of its episodes:
		// a film or an episode by its own key, a series or a season by its
		// own and its items'. Each looked up by key in what the profile
		// did (joined the other way, SQLite went through the profile's
		// whole history for every title: minutes on a large library).
		order, ok = `(CASE WHEN catalog_items.kind IN ('movie', 'episode') THEN
				(SELECT x.last_played_at FROM profile_titles x WHERE x.profile_id = ? AND x.title = catalog_items.title_key)
			ELSE (SELECT max(x.last_played_at) FROM profile_titles x WHERE x.profile_id = ? AND x.title IN
				(SELECT e.title_key FROM catalog_items e
					WHERE e.id = catalog_items.id OR e.series_id = catalog_items.id OR e.season_id = catalog_items.id)) END)`, true
		orderArgs = []any{q.ProfileID, q.ProfileID}
	}
	if !ok {
		return nil, fmt.Errorf("store: unknown sort %q", q.Sort)
	}
	where, args := q.where()
	from, err := s.listedRows(ctx, q)
	if err != nil {
		return nil, err
	}
	dir := "ASC"
	if q.Descending {
		dir = "DESC"
	}
	// One copy of each title (the same film in two libraries, 4K and HD):
	// pages and counts are of titles.
	query := `SELECT id FROM (SELECT id, sort_name, ` + order + ` AS o,
			row_number() OVER (PARTITION BY coalesce(title_key, id) ORDER BY id) AS copy
			FROM ` + from + ` WHERE ` + strings.Join(where, " AND ") + `)
		WHERE copy = 1 ORDER BY o IS NULL, o ` + dir + `, sort_name COLLATE NOCASE, id LIMIT ? OFFSET ?`
	return s.ids(ctx, query, append(append(orderArgs, args...), q.Limit, q.Offset)...)
}

// CatalogIDs lists every item a listing has, in no order, copies of a
// title included: the quickest way to know which items a profile may see.
func (s *Store) CatalogIDs(ctx context.Context, q ListQuery) ([]string, error) {
	where, args := q.where()
	return s.ids(ctx, `SELECT id FROM catalog_items WHERE `+strings.Join(where, " AND "), args...)
}

// CountCatalog counts the titles a listing has in all.
func (s *Store) CountCatalog(ctx context.Context, q ListQuery) (int, error) {
	where, args := q.where()
	from, err := s.listedRows(ctx, q)
	if err != nil {
		return 0, err
	}
	var n int
	err = s.read.QueryRowContext(ctx, `SELECT count(DISTINCT coalesce(title_key, id)) FROM `+from+` WHERE `+
		strings.Join(where, " AND "), args...).Scan(&n)
	return n, err
}

// where is the listing's conditions on catalog_items.
func (q ListQuery) where() ([]string, []any) {
	where := []string{"1 = 1"}
	var args []any
	if q.LibraryID != "" {
		where, args = append(where, "library_id = ?"), append(args, q.LibraryID)
	}
	if q.ParentID != "" {
		where, args = append(where, "(series_id = ? OR season_id = ?)"), append(args, q.ParentID, q.ParentID)
	}
	in := func(column string, values []string) {
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(values)), ", ")
		where = append(where, column+" IN ("+marks+")")
		for _, v := range values {
			args = append(args, v)
		}
	}
	if len(q.Kinds) > 0 {
		in("kind", q.Kinds)
	}
	if len(q.Genres) > 0 {
		where = append(where, `EXISTS (SELECT 1 FROM catalog_details_genres g
			WHERE g.kind = catalog_items.kind AND g.tmdb = catalog_items.tmdb AND g.name IN (`+marks(len(q.Genres))+`))`)
		for _, v := range q.Genres {
			args = append(args, v)
		}
	}
	// What the profile did with the title, through its key.
	mine := func(cond string) string {
		return "EXISTS (SELECT 1 FROM profile_titles x WHERE x.profile_id = ? AND x.title = catalog_items.title_key AND " + cond + ")"
	}
	// A series' or a season's episodes, each title once, through the series
	// or season index ("+e.kind": SQLite would rather use the kind index,
	// and go through every episode of the library for each title).
	episodes := func(col, join string) string {
		return `(SELECT count(DISTINCT e.title_key) FROM catalog_items e ` + join + `
			WHERE e.` + col + ` = catalog_items.id AND +e.kind = 'episode')`
	}
	played := `JOIN profile_titles x ON x.profile_id = ? AND x.title = e.title_key AND x.played = 1`
	started := `JOIN profile_titles x ON x.profile_id = ? AND x.title = e.title_key AND x.position_ms > 0`
	// Whether the profile did something (cond, on x) with one of a
	// series' episodes, looked up the other way: in its history, under
	// the series' key (an episode's key is its series' key, then its
	// numbers), one seek for each series. An episode the index can't name
	// so has a key of its own (item/…): those of the history are looked
	// up in the index, once for the whole listing. Counting each series'
	// episodes instead went through every episode of the library.
	seriesEpisode := func(cond string) string {
		return `(EXISTS (SELECT 1 FROM profile_titles x
				WHERE x.profile_id = ? AND x.title > catalog_items.title_key || '/' AND x.title < catalog_items.title_key || '0'
				AND ` + cond + ` AND EXISTS (SELECT 1 FROM catalog_items e
					WHERE e.title_key = x.title AND e.series_id = catalog_items.id AND e.kind = 'episode'))
			OR catalog_items.id IN (SELECT e.series_id FROM profile_titles x
				CROSS JOIN catalog_items e ON e.title_key = x.title
				WHERE x.profile_id = ? AND x.title >= 'item/' AND x.title < 'item0' AND ` + cond + `
				AND e.kind = 'episode' AND e.series_id IS NOT NULL))`
	}
	if q.Played != nil {
		// A series or a season is watched once all its episodes are. For
		// a series: one is watched (which tells most series apart at
		// once), and none with a key isn't.
		seasonAll := "(" + episodes("season_id", "") + " > 0 AND " + episodes("season_id", "") + " = " + episodes("season_id", played) + ")"
		seriesAll := "(" + seriesEpisode("x.played = 1") + ` AND NOT EXISTS (SELECT 1 FROM catalog_items e
				WHERE e.series_id = catalog_items.id AND +e.kind = 'episode' AND e.title_key IS NOT NULL
				AND NOT EXISTS (SELECT 1 FROM profile_titles x WHERE x.profile_id = ? AND x.title = e.title_key AND x.played = 1)))`
		cond := `(CASE catalog_items.kind WHEN 'series' THEN ` + seriesAll + `
			WHEN 'season' THEN ` + seasonAll + `
			ELSE ` + mine("x.played = 1") + ` END)`
		if !*q.Played {
			cond = "NOT " + cond
		}
		where, args = append(where, cond), append(args, q.ProfileID, q.ProfileID, q.ProfileID, q.ProfileID, q.ProfileID)
	}
	if q.Favorite != nil {
		cond := mine("x.favorite = 1")
		if !*q.Favorite {
			cond = "NOT " + cond
		}
		where, args = append(where, cond), append(args, q.ProfileID)
	}
	if q.InProgress {
		// A series or a season, when one of its episodes is.
		cond := `(CASE catalog_items.kind WHEN 'series' THEN ` + seriesEpisode("x.position_ms > 0") + `
			WHEN 'season' THEN ` + episodes("season_id", started) + ` > 0
			ELSE ` + mine("x.position_ms > 0") + ` END)`
		where, args = append(where, cond), append(args, q.ProfileID, q.ProfileID, q.ProfileID, q.ProfileID)
	}
	year := "coalesce(" + fromTMDB("year") + ", year)"
	if q.YearFrom > 0 {
		where, args = append(where, year+" >= ?"), append(args, q.YearFrom)
	}
	if q.YearTo > 0 {
		where, args = append(where, year+" <= ?"), append(args, q.YearTo)
	}
	if q.Access != nil {
		cond, a := q.Access.itemAllowed("catalog_items")
		where, args = append(where, cond), append(args, a...)
	}
	if q.Category != nil {
		cond, a := q.Category.itemIn("catalog_items")
		where, args = append(where, cond), append(args, a...)
	}
	return where, args
}

// GenreCount is a genre, with how many titles have it.
type GenreCount struct {
	Name  string
	Count int
}

// CatalogGenres lists the genres (TMDB's) of a library's films and series
// (any library's, with libraryID empty), within a category when one is
// given, counting only what access lets the profile see, by name.
func (s *Store) CatalogGenres(ctx context.Context, libraryID string, category *CategoryFilter, access *Access) ([]GenreCount, error) {
	where := []string{"catalog_items.kind IN ('movie', 'series')"}
	var args []any
	if libraryID != "" {
		where, args = append(where, "catalog_items.library_id = ?"), append(args, libraryID)
	}
	if category != nil {
		cond, a := category.itemIn("catalog_items")
		where, args = append(where, cond), append(args, a...)
	}
	if access != nil {
		cond, a := access.itemAllowed("catalog_items")
		where, args = append(where, cond), append(args, a...)
	}
	from, err := s.listedRows(ctx, ListQuery{Access: access})
	if err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT g.name, count(DISTINCT g.kind || g.tmdb)
		FROM `+from+` JOIN catalog_details_genres g ON g.kind = catalog_items.kind AND g.tmdb = catalog_items.tmdb
		WHERE `+strings.Join(where, " AND ")+`
		GROUP BY g.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GenreCount{}
	for rows.Next() {
		var g GenreCount
		if err := rows.Scan(&g.Name, &g.Count); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	// By name, accents aside.
	slices.SortFunc(out, func(a, b GenreCount) int { return strings.Compare(SortName(a.Name), SortName(b.Name)) })
	return out, rows.Err()
}

// RealItem is the library item that holds an index row: the row itself,
// or, for an episode of a file holding several, the file's item.
func (s *Store) RealItem(ctx context.Context, id string) string {
	var of sql.NullString
	if err := s.read.QueryRowContext(ctx, `SELECT range_of FROM catalog_items WHERE id = ?`, id).Scan(&of); err == nil && of.Valid {
		return of.String
	}
	return id
}

// RangeKeys are the title keys of the episodes a file holds after its
// first (its item's rows, range_of), for marking them all watched.
func (s *Store) RangeKeys(ctx context.Context, item string) ([]string, error) {
	var out []string
	err := s.each(ctx, func(rows *sql.Rows) error {
		var key string
		if err := rows.Scan(&key); err != nil {
			return err
		}
		out = append(out, key)
		return nil
	}, `SELECT title_key FROM catalog_items WHERE range_of = ? AND title_key IS NOT NULL ORDER BY episode_number`, item)
	return out, err
}

// SharedFiles says, of these episode keys, which are held by the same file
// as an earlier one: by key, the key of the file's first episode.
func (s *Store) SharedFiles(ctx context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	if len(keys) == 0 {
		return out, nil
	}
	err := s.each(ctx, func(rows *sql.Rows) error {
		var key, first string
		if err := rows.Scan(&key, &first); err != nil {
			return err
		}
		out[key] = first
		return nil
	}, `SELECT r.title_key, f.title_key FROM json_each(?) j
		JOIN catalog_items r ON r.title_key = j.value AND r.range_of IS NOT NULL
		JOIN catalog_items f ON f.id = r.range_of AND f.title_key IS NOT NULL`, jsonList(keys))
	return out, err
}

// ItemDescription names a catalog item in words, for the control panel's
// live view: "Name (year)" for a film, "Series · S1 E2 · Name" for an
// episode; "" when it isn't in the catalog.
func (s *Store) ItemDescription(ctx context.Context, id string) string {
	var name, series sql.NullString
	var year, season, episode sql.NullInt64
	var kind string
	// TMDB's names when known: Jellyfin's may be a folder's, with its tags.
	err := s.read.QueryRowContext(ctx, `SELECT i.kind, coalesce(t.name, i.name), coalesce(t.year, i.year), i.season_number, i.episode_number,
			coalesce(st.name, se.name)
		FROM catalog_items i
		LEFT JOIN catalog_titles t ON i.kind = 'movie' AND t.kind = 'movie' AND t.tmdb = i.tmdb
		LEFT JOIN catalog_items se ON se.id = i.series_id
		LEFT JOIN catalog_titles st ON st.kind = 'series' AND st.tmdb = se.tmdb
		WHERE i.id = ?`, id).
		Scan(&kind, &name, &year, &season, &episode, &series)
	if err != nil {
		return ""
	}
	if kind == "episode" && series.Valid {
		num := ""
		if season.Valid && episode.Valid {
			num = fmt.Sprintf("S%d E%d · ", season.Int64, episode.Int64)
		}
		return series.String + " · " + num + name.String
	}
	if year.Valid && year.Int64 > 0 {
		return fmt.Sprintf("%s (%d)", name.String, year.Int64)
	}
	return name.String
}
