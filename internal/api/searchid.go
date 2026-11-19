package api

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// A search by ID: the search box takes a title's IMDb, TMDB or TheTVDB ID
// (imdb:tt0133093, tt0133093, tmdb:movie/603, tmdb:m603, tmdb:603,
// tvdb:81189), or a link to the title on one of those sites, and answers
// with what it names instead of searching the words: an ID's characters
// would only find noise.

// idQuery is a search for a title by an ID.
type idQuery struct {
	// Source is the ID's: "imdb", "tmdb" or "tvdb".
	Source string
	// Kind is what the ID is said to name: "movie", "series" or "episode";
	// "" when it doesn't say (a TMDB ID then names a film or a series, an
	// IMDb or a TVDB ID anything).
	Kind string
	// ID is the ID itself: tt0133093, 603, 81189. A TMDB episode by its
	// numbers is its series' ID with Season and Episode, in TMDB's order.
	ID              string
	Season, Episode int
	// Valid: the ID is well formed. A query that looks like an ID but
	// isn't one (imdb:matrix, a link without one) searches nothing.
	Valid bool
}

// String is the ID as search shows it back: imdb:tt0133093,
// tmdb:movie/603, tmdb:tv/1399/s1/e1, tvdb:81189…; for one that isn't
// valid, its source alone.
func (q idQuery) String() string {
	if !q.Valid {
		return q.Source + ":"
	}
	switch {
	case q.Source == "tmdb" && q.Episode > 0:
		return "tmdb:tv/" + q.ID + "/s" + strconv.Itoa(q.Season) + "/e" + strconv.Itoa(q.Episode)
	case q.Source == "tmdb" && q.Kind == "movie":
		return "tmdb:movie/" + q.ID
	case q.Source == "tmdb" && q.Kind == "series":
		return "tmdb:tv/" + q.ID
	case q.Source == "tmdb" && q.Kind == "episode":
		return "tmdb:e" + q.ID
	case q.Kind != "":
		return q.Source + ":" + q.Kind + "/" + q.ID
	}
	return q.Source + ":" + q.ID
}

var (
	imdbID   = regexp.MustCompile(`^(?:tt)?([0-9]{7,10})$`)
	bareIMDb = regexp.MustCompile(`^tt[0-9]+$`)
	// A TMDB ID: movie/603, tv/1399 (series/1399), an episode of it by
	// its numbers (tv/1399/s1/e1, tv/1399/season/1/episode/1); m603,
	// s1399 and e63056 (an episode by TMDB's ID of it) as Overfin's own
	// keys and addresses have them; or 603, a film or a series.
	tmdbPath  = regexp.MustCompile(`^(movie|tv|series)/([0-9]{1,10})(?:/s(?:eason/)?([0-9]{1,4})/e(?:pisode/)?([0-9]{1,5}))?$`)
	tmdbShort = regexp.MustCompile(`^([mse]?)([0-9]{1,10})$`)
	tvdbID    = regexp.MustCompile(`^(?:(series|episode|movie)/)?([0-9]{1,10})$`)
	// The path of a title's page on IMDb (/title/tt0133093/, /fr/title/…)
	// and on TMDB (/movie/603-the-matrix, /tv/1399-…/season/1/episode/1).
	imdbPage = regexp.MustCompile(`^/(?:[a-z]{2}(?:-[a-z]{2})?/)?title/(tt[0-9]{7,10})(?:/|$)`)
	tmdbPage = regexp.MustCompile(`^/(?:[a-z]{2}(?:-[a-z]{2})?/)?(movie|tv)/([0-9]{1,10})(?:-[^/]*)?(?:/season/([0-9]{1,4})(?:/episode/([0-9]{1,5}))?)?(?:/|$)`)
	// TheTVDB's pages: /dereferrer/series/81189, an episode's
	// /series/the-simpsons/episodes/55452. A page by its name alone
	// (/series/the-simpsons) has no ID.
	tvdbPage    = regexp.MustCompile(`^/dereferrer/(series|episode|movie)/([0-9]{1,10})(?:/|$)`)
	tvdbEpisode = regexp.MustCompile(`^/series/[^/]+/episodes/([0-9]{1,10})(?:/|$)`)
)

// parseID reads a search's words as an ID; ok is false when they aren't
// one (a search by words). Letters' case and spaces around don't count.
func parseID(query string) (q idQuery, ok bool) {
	s := strings.ToLower(strings.TrimSpace(query))
	if site, ok := idSite(s); ok {
		return parseLink(site, s), true
	}
	// tt and digits alone: an IMDb ID, or one mistyped.
	if bareIMDb.MatchString(s) {
		q := idQuery{Source: "imdb"}
		if m := imdbID.FindStringSubmatch(s); m != nil {
			q.ID, q.Valid = "tt"+m[1], true
		}
		return q, true
	}
	source, value, found := strings.Cut(s, ":")
	if !found || (source != "imdb" && source != "tmdb" && source != "tvdb") {
		return idQuery{}, false
	}
	q = idQuery{Source: source}
	value = strings.TrimSpace(value)
	switch source {
	case "imdb":
		if m := imdbID.FindStringSubmatch(value); m != nil {
			q.ID, q.Valid = "tt"+m[1], true
		}
	case "tmdb":
		if m := tmdbPath.FindStringSubmatch(value); m != nil {
			q.Kind = map[string]string{"movie": "movie", "tv": "series", "series": "series"}[m[1]]
			q.setTMDB(m[2], m[3], m[4])
		} else if m := tmdbShort.FindStringSubmatch(value); m != nil {
			q.Kind = map[string]string{"m": "movie", "s": "series", "e": "episode"}[m[1]]
			q.setTMDB(m[2], "", "")
		}
	case "tvdb":
		if m := tvdbID.FindStringSubmatch(value); m != nil {
			q.Kind = m[1]
			q.setNumber(m[2])
		}
	}
	return q, true
}

// setTMDB sets a TMDB ID, and an episode's numbers when there are some
// (for a series only).
func (q *idQuery) setTMDB(id, season, episode string) {
	q.setNumber(id)
	if season == "" || episode == "" {
		return
	}
	if q.Kind != "series" {
		q.Valid = false
		return
	}
	q.Kind = "episode"
	q.Season, _ = strconv.Atoi(season)
	q.Episode, _ = strconv.Atoi(episode)
	if q.Season > 1000 || q.Episode < 1 {
		q.Valid = false
	}
}

// setNumber sets a TMDB or TVDB ID, written without zeros in front.
func (q *idQuery) setNumber(digits string) {
	n, err := strconv.Atoi(digits)
	if err == nil && tmdb.ValidID(n) {
		q.ID, q.Valid = strconv.Itoa(n), true
	}
}

// idSite is the site a link is to, when it's to IMDb, TMDB or TheTVDB
// (with or without https://, www. or m.).
func idSite(s string) (string, bool) {
	if strings.ContainsAny(s, " \t") {
		return "", false
	}
	host := s
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	for _, site := range []string{"imdb.com", "themoviedb.org", "thetvdb.com"} {
		if host == site || strings.HasSuffix(host, "."+site) {
			return site, true
		}
	}
	return "", false
}

// parseLink reads a link to IMDb, TMDB or TheTVDB as the ID of the title
// it shows; a link to anything else there (a person, a search, a page by
// name) is no valid ID.
func parseLink(site, s string) idQuery {
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	source := map[string]string{"imdb.com": "imdb", "themoviedb.org": "tmdb", "thetvdb.com": "tvdb"}[site]
	q := idQuery{Source: source}
	if err != nil {
		return q
	}
	switch source {
	case "imdb":
		if m := imdbPage.FindStringSubmatch(u.Path); m != nil {
			q.ID, q.Valid = m[1], true
		}
	case "tmdb":
		if m := tmdbPage.FindStringSubmatch(u.Path); m != nil {
			q.Kind = map[string]string{"movie": "movie", "tv": "series"}[m[1]]
			// A season's page is its series'.
			if m[4] == "" {
				m[3] = ""
			}
			q.setTMDB(m[2], m[3], m[4])
		}
	case "tvdb":
		if m := tvdbPage.FindStringSubmatch(u.Path); m != nil {
			q.Kind = m[1]
			q.setNumber(m[2])
		} else if m := tvdbEpisode.FindStringSubmatch(u.Path); m != nil {
			q.Kind = "episode"
			q.setNumber(m[1])
		} else if v := u.Query(); u.Path == "/" || u.Path == "" || u.Path == "/index.php" {
			// The old pages: ?tab=series&id=81189, ?tab=episode&…&id=…
			if kind := map[string]string{"series": "series", "episode": "episode"}[v.Get("tab")]; kind != "" && tvdbID.MatchString(v.Get("id")) {
				q.Kind = kind
				q.setNumber(v.Get("id"))
			}
		}
	}
	return q
}

// searchByID is what a search by ID shows back: the ID as search read it.
type searchByID struct {
	// ID is the ID read (imdb:tt0133093, tmdb:movie/603…); for one that
	// looks like an ID but isn't, its source alone (imdb:).
	ID string `json:"id"`
	// Invalid: the words looked like an ID, or a link to IMDb, TMDB or
	// TheTVDB, but none could be read: nothing was searched.
	Invalid bool `json:"invalid,omitempty"`
	// SeriesInstead: the ID names an episode this server can't show on
	// its own; its series is shown instead.
	SeriesInstead bool `json:"seriesInstead,omitempty"`
}

// idKeys returns the keys of the titles an ID names: what this server
// keeps first (the libraries', the paired servers', what TMDB said
// before), TMDB only when none is, once (catalog.Titles keeps its answers
// a day). seriesInstead: an episode named is shown as its series.
func (l Library) idKeys(ctx context.Context, account string, q idQuery) (keys []string, seriesInstead bool, err error) {
	st := l.Provisioner.Store
	may := func() bool { return l.Lookups == nil || l.Lookups.allow(account, time.Now()) }
	switch {
	case q.Source == "tmdb" && q.Kind == "episode" && q.Episode == 0:
		// An episode by TMDB's ID of it: TMDB can't be asked its series,
		// so only one this server keeps.
		n, _ := strconv.Atoi(q.ID)
		series, err := st.EpisodeSeries(ctx, n)
		if err != nil || series == "" {
			return nil, false, err
		}
		return []string{"series/tmdb/" + series + "/e" + q.ID}, false, nil
	case q.Source == "tmdb" && q.Kind == "episode":
		return l.episodeKey(ctx, account, tmdb.Named{Kind: "episode", TMDB: atoi(q.ID), Season: q.Season, Number: q.Episode})
	case q.Source == "tmdb":
		for _, kind := range []string{"movie", "series"} {
			if q.Kind == "" || q.Kind == kind {
				ref := store.TitleRef{Kind: kind, Source: "tmdb", ID: q.ID}
				l.describe(ctx, account, ref)
				keys = append(keys, ref.Key())
			}
		}
		return keys, false, nil
	}
	if keys, err = st.KeysByOtherID(ctx, q.Source, q.ID); err != nil || len(keys) > 0 {
		return l.ofKind(keys, q.Kind), false, err
	}
	if l.Titles == nil {
		return nil, false, nil
	}
	named, err := l.Titles.FindByID(ctx, q.Source, q.ID, may)
	if errors.Is(err, tmdb.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	for _, n := range named {
		if q.Kind != "" && n.Kind != q.Kind {
			continue
		}
		if n.Kind == "episode" {
			k, instead, err := l.episodeKey(ctx, account, n)
			if err != nil {
				return nil, false, err
			}
			keys, seriesInstead = append(keys, k...), seriesInstead || instead
			continue
		}
		ref := store.TitleRef{Kind: n.Kind, Source: "tmdb", ID: strconv.Itoa(n.TMDB)}
		l.describe(ctx, account, ref)
		keys = append(keys, ref.Key())
	}
	return keys, seriesInstead, nil
}

// ofKind keeps the keys of titles of a kind ("" any).
func (l Library) ofKind(keys []string, kind string) []string {
	if kind == "" {
		return keys
	}
	var out []string
	for _, k := range keys {
		if r, ok := store.ParseTitleKey(k); ok && (r.Kind == kind || r.Kind == "item" && kind != "series") {
			out = append(out, k)
		}
	}
	return out
}

// episodeKey returns the key of a series' episode (by TMDB's ID of it, or
// its numbers in TMDB's order), its series and its season fetched first
// when they aren't kept; its series' when the episode can't be shown
// alone (its season isn't on TMDB, or this server shows the series in
// another order where those numbers don't hold).
func (l Library) episodeKey(ctx context.Context, account string, n tmdb.Named) ([]string, bool, error) {
	st := l.Provisioner.Store
	series := strconv.Itoa(n.TMDB)
	kept := func(id int) (bool, error) {
		s, err := st.EpisodeSeries(ctx, id)
		return s == series, err
	}
	key := func(id int) []string { return []string{"series/tmdb/" + series + "/e" + strconv.Itoa(id)} }
	if n.Episode > 0 {
		if ok, err := kept(n.Episode); err != nil || ok {
			return key(n.Episode), false, err
		}
	}
	// Its season, then the episode in it.
	l.describe(ctx, account, store.TitleRef{Kind: "season", Source: "tmdb", ID: series, Season: n.Season})
	id := n.Episode
	if id == 0 {
		at, err := st.EpisodeAt(ctx, series, n.Season, n.Number)
		if err != nil {
			return nil, false, err
		}
		id = at
	}
	if id > 0 {
		if ok, err := kept(id); err != nil || ok {
			return key(id), false, err
		}
	}
	return []string{store.TitleRef{Kind: "series", Source: "tmdb", ID: series}.Key()}, true, nil
}

// atoi is a number already checked.
func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
