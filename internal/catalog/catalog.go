// Package catalog keeps Overfin's index of the library: every title, with
// its TMDB, IMDb and TVDB IDs, its rating and its image references, read
// through Overfin's Jellyfin service account and kept in Overfin's
// database. Nothing personal is read or kept.
//
// Overfin's plugin gives what the index keeps, and a feed of what changed;
// a full read happens only when the feed can't say.
package catalog

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/lang"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/textclean"
	"github.com/overfin/server/internal/tracks"
)

// Service is the Jellyfin service account the catalog reads the libraries
// with.
type Service interface {
	Auth(ctx context.Context) (jellyfin.Auth, string, error)
	// Refused tells it Jellyfin refused its token; it gets a new one.
	Refused(ctx context.Context) error
}

// Sync keeps the index in step with the library, through Overfin's plugin:
// its change feed says which items changed since the last read, and only
// those are read again. The whole library is read when the feed can't say
// (a first run, a Jellyfin that started again, a place the feed let go),
// and only then are titles not seen removed.
type Sync struct {
	Jellyfin *jellyfin.Client
	Store    *store.Store
	// Service reads the libraries' list; Admin is Overfin's key, which the
	// plugin answers.
	Service Service
	Admin   func() jellyfin.Auth
	Logger  *slog.Logger
	// Every is how often the feed is asked.
	Every time.Duration
	// Read, when set, runs once titles were read: the titles' keeper
	// matches the new ones and fetches their pages then, not on its next
	// pass.
	Read func()

	mu      sync.Mutex
	lastErr string
	now     func() time.Time
	// generation and since are where the feed was read to: Jellyfin's
	// start, and the last change read.
	generation string
	since      int64
}

// pageSize is how many titles one page of a full read holds.
const pageSize = 1000

func (s *Sync) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Run keeps the index in step until ctx ends.
func (s *Sync) Run(ctx context.Context) {
	// Title keys first, for items indexed before they existed.
	if err := s.Store.RefreshTitleKeys(ctx); err != nil && ctx.Err() == nil {
		s.Logger.Error("catalog: computing title keys failed", "err", err)
	}
	for {
		s.once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.Every):
		}
	}
}

func (s *Sync) once(ctx context.Context) {
	err := s.step(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil && ctx.Err() == nil {
		if err.Error() != s.lastErr {
			s.Logger.Error("catalog: reading the library failed", "err", err)
		}
		s.lastErr = err.Error()
	} else {
		s.lastErr = ""
	}
}

// step reads what the feed says changed, or the whole library when it
// can't say.
func (s *Sync) step(ctx context.Context) error {
	st, err := s.Store.CatalogState(ctx)
	if err != nil {
		return err
	}
	ch, err := s.Jellyfin.IndexChanges(ctx, s.Admin(), s.generation, s.since, changesLimit)
	if err != nil {
		return err
	}
	if ch.Reset || st.Pass == 0 {
		// The feed's place now, before the read: what changes while it
		// runs is read after.
		if err := s.Full(ctx); err != nil {
			return err
		}
		s.generation, s.since = ch.Generation, ch.Last
		return nil
	}
	for {
		if err := s.apply(ctx, ch.Changes); err != nil {
			return err
		}
		if len(ch.Changes) > 0 {
			s.since = ch.Changes[len(ch.Changes)-1].Seq
		}
		if len(ch.Changes) < changesLimit {
			return nil
		}
		if ch, err = s.Jellyfin.IndexChanges(ctx, s.Admin(), s.generation, s.since, changesLimit); err != nil {
			return err
		}
		if ch.Reset {
			s.generation = "" // read everything at the next step
			return nil
		}
	}
}

const changesLimit = 1000

// Full reads the whole library, then removes the titles it didn't see.
func (s *Sync) Full(ctx context.Context) error {
	st, err := s.Store.CatalogState(ctx)
	if err != nil {
		return err
	}
	start := s.clock()
	pass := st.Pass + 1
	if err := s.libraries(ctx); err != nil {
		return err
	}
	n, after := 0, ""
	for {
		page, err := s.Jellyfin.IndexPage(ctx, s.Admin(), after, pageSize)
		if err != nil {
			return err
		}
		items := converted(page.Items)
		if err := s.Store.PutCatalog(ctx, items, pass); err != nil {
			return err
		}
		n += len(items)
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	// Pages by ID don't shift: a title not seen is gone.
	unseen, err := s.Store.UnseenCatalog(ctx, pass)
	if err != nil {
		return err
	}
	if err := s.Store.DeleteCatalog(ctx, unseen); err != nil {
		return err
	}
	if err := s.Store.RefreshTitleKeys(ctx); err != nil {
		return err
	}
	st.Pass, st.FullAt, st.ChangesAt = pass, start, start
	if err := s.Store.SetCatalogState(ctx, st); err != nil {
		return err
	}
	s.Logger.Info("catalog: the library was read", "titles", n, "removed", len(unseen),
		"seconds", s.clock().Sub(start).Round(100*time.Millisecond).Seconds())
	s.notify()
	return nil
}

// apply reads the items changes name again: those still in a library are
// kept, the others removed.
func (s *Sync) apply(ctx context.Context, changes []jellyfin.IndexChange) error {
	if len(changes) == 0 {
		return nil
	}
	st, err := s.Store.CatalogState(ctx)
	if err != nil {
		return err
	}
	var ids []string
	seen := map[string]bool{}
	for _, c := range changes {
		if id, err := jellyfin.NormalizeID(c.Id); err == nil && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	read, removed := 0, 0
	for batch := range slices.Chunk(ids, 200) {
		page, err := s.Jellyfin.IndexItems(ctx, s.Admin(), batch)
		if err != nil {
			return err
		}
		items := converted(page.Items)
		if err := s.Store.PutCatalog(ctx, items, st.Pass); err != nil {
			return err
		}
		present := map[string]bool{}
		for _, it := range items {
			present[it.ID] = true
		}
		var gone []string
		for _, id := range batch {
			if !present[id] {
				gone = append(gone, id)
			}
		}
		if err := s.Store.DeleteCatalog(ctx, gone); err != nil {
			return err
		}
		read, removed = read+len(items), removed+len(gone)
	}
	// A new library's titles come with a change: the list is read again.
	if err := s.libraries(ctx); err != nil {
		return err
	}
	if err := s.Store.RefreshTitleKeys(ctx); err != nil {
		return err
	}
	st.ChangesAt = s.clock()
	if err := s.Store.SetCatalogState(ctx, st); err != nil {
		return err
	}
	s.Logger.Info("catalog: changes read", "titles", read, "removed", removed)
	s.notify()
	return nil
}

func converted(raws []jellyfin.IndexItem) []store.CatalogItem {
	var items []store.CatalogItem
	for _, raw := range raws {
		if it, ok := convert(raw); ok {
			items = append(items, it)
		}
	}
	return items
}

// notify runs Read, when set.
func (s *Sync) notify() {
	if s.Read != nil {
		s.Read()
	}
}

// libraries keeps the libraries' list, as the service account sees them.
func (s *Sync) libraries(ctx context.Context) error {
	var libs []jellyfin.RawItem
	if err := s.withService(ctx, func(a jellyfin.Auth, userID string) error {
		var err error
		libs, err = s.Jellyfin.Libraries(ctx, a, userID)
		return err
	}); err != nil {
		return err
	}
	kept := make([]store.Library, 0, len(libs))
	for _, lib := range libs {
		if id, err := jellyfin.NormalizeID(lib.Id); err == nil {
			item, err := json.Marshal(lib)
			if err != nil {
				return err
			}
			kept = append(kept, store.Library{ID: id, Item: item})
		}
	}
	return s.Store.SetLibraries(ctx, kept)
}

// withService runs fn with the service account, and once more with a new
// token if Jellyfin refused the first.
func (s *Sync) withService(ctx context.Context, fn func(jellyfin.Auth, string) error) error {
	a, userID, err := s.Service.Auth(ctx)
	if err != nil {
		return err
	}
	err = fn(a, userID)
	if !jellyfin.IsStatus(err, http.StatusUnauthorized) {
		return err
	}
	if err := s.Service.Refused(ctx); err != nil {
		return err
	}
	if a, userID, err = s.Service.Auth(ctx); err != nil {
		return err
	}
	return fn(a, userID)
}

// Status is where the index stands, for the control panel.
type Status struct {
	Titles map[string]int `json:"titles"`
	// Unmatched counts, by kind, the titles that can't be matched to TMDB
	// (store.CatalogUnmatched).
	Unmatched map[string]int `json:"unmatched"`
	FullAt    time.Time      `json:"fullAt"`
	ChangesAt time.Time      `json:"changesAt"`
	// Error is the last read's failure, if it failed.
	Error string `json:"error,omitempty"`
}

// Status says where the index stands.
func (s *Sync) Status(ctx context.Context) (Status, error) {
	st, err := s.Store.CatalogState(ctx)
	if err != nil {
		return Status{}, err
	}
	titles, err := s.Store.CatalogCounts(ctx)
	if err != nil {
		return Status{}, err
	}
	unmatched, err := s.Store.CatalogUnmatched(ctx)
	if err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{Titles: titles, Unmatched: unmatched, FullAt: st.FullAt, ChangesAt: st.ChangesAt, Error: s.lastErr}, nil
}

// ---- From Jellyfin's items to the index -----------------------------------------

var kinds = map[string]string{"Movie": "movie", "Series": "series", "Season": "season", "Episode": "episode"}

// What an upstream ID may look like (SECURITY.md rule 2): anything else is
// left out.
var (
	tmdbID   = regexp.MustCompile(`^[0-9]{1,10}$`)
	imdbID   = regexp.MustCompile(`^tt[0-9]{1,10}$`)
	tvdbID   = regexp.MustCompile(`^[0-9]{1,10}$`)
	imageTag = regexp.MustCompile(`^[0-9A-Za-z]{1,64}$`)
)

// convert turns one of the plugin's items into a title of the index, its
// text made plain and its IDs checked. Items of other types, or with an ID
// that isn't one, are left out.
func convert(raw jellyfin.IndexItem) (store.CatalogItem, bool) {
	kind, ok := kinds[raw.Type]
	if !ok {
		return store.CatalogItem{}, false
	}
	id, err := jellyfin.NormalizeID(raw.Id)
	if err != nil {
		return store.CatalogItem{}, false
	}
	it := store.CatalogItem{
		ID:       id,
		Kind:     kind,
		Name:     textclean.Plain(raw.Name, 300),
		SortName: textclean.Plain(raw.SortName, 300),
		Year:     raw.ProductionYear,
		Rating:   textclean.Plain(raw.OfficialRating, 32),
		TMDB:     tmdbNumber(raw.ProviderIds["Tmdb"]),
		IMDb:     matching(imdbID, raw.ProviderIds["Imdb"]),
		TVDB:     matching(tvdbID, raw.ProviderIds["Tvdb"]),
	}
	if it.SortName == "" {
		it.SortName = it.Name
	}
	if kind == "season" || kind == "episode" {
		it.SeriesID, _ = jellyfin.NormalizeID(raw.SeriesId)
	}
	switch kind {
	case "season":
		it.SeasonNumber = raw.IndexNumber
	case "episode":
		it.SeasonID, _ = jellyfin.NormalizeID(raw.SeasonId)
		it.SeasonNumber, it.EpisodeNumber = raw.ParentIndexNumber, raw.IndexNumber
	}
	if t, err := time.Parse(time.RFC3339Nano, raw.DateCreated); err == nil {
		it.AddedAt = t
	}
	it.LibraryID, _ = jellyfin.NormalizeID(raw.LibraryId)
	// What the file and its folder are named, for telling a misidentified
	// title (the control panel's Identification): a film's file and the
	// folder it's in, a series' folder. The plugin gives the names, never
	// the path.
	switch kind {
	case "movie":
		it.FolderName, it.FileName = raw.FolderName, raw.FileName
		if raw.RunTimeTicks > 0 {
			it.RuntimeMs = raw.RunTimeTicks / 10_000
		}
	case "series":
		it.FolderName = raw.FolderName
	}
	it.FolderName, it.FileName = textclean.Plain(it.FolderName, 255), textclean.Plain(it.FileName, 255)
	if len(raw.PremiereDate) >= 10 {
		if _, err := time.Parse("2006-01-02", raw.PremiereDate[:10]); err == nil {
			it.PremiereDate = raw.PremiereDate[:10]
		}
	}
	if r := raw.CommunityRating; r != nil && *r >= 0 && *r <= 10 {
		it.CommunityRating = r
	}
	// The languages of a film's or an episode's audio, for telling which
	// title it is (identify.Match): two letters each, once.
	for _, s := range raw.MediaStreams {
		if l := lang.Base(s.Language); s.Type == "Audio" && l != "" && !slices.Contains(it.AudioLanguages, l) &&
			len(it.AudioLanguages) < maxAudioLanguages {
			it.AudioLanguages = append(it.AudioLanguages, l)
		}
	}
	// Its languages as the player lists them, for its title's sources.
	audio, subtitles, _ := tracks.Read(raw.MediaStreams)
	for _, t := range audio {
		if t.Role == tracks.Main && t.Language != "" && !slices.Contains(it.AudioTags, t.Language) && len(it.AudioTags) < maxAudioLanguages {
			it.AudioTags = append(it.AudioTags, t.Language)
		}
	}
	for _, t := range subtitles {
		if t.Language != "" && !slices.Contains(it.SubtitleTags, t.Language) && len(it.SubtitleTags) < maxAudioLanguages {
			it.SubtitleTags = append(it.SubtitleTags, t.Language)
		}
	}
	it.PrimaryTag = matching(imageTag, raw.ImageTags["Primary"])
	it.ThumbTag = matching(imageTag, raw.ImageTags["Thumb"])
	if len(raw.BackdropImageTags) > 0 {
		it.BackdropTag = matching(imageTag, raw.BackdropImageTags[0])
	}
	return it, true
}

// tmdbNumber is a TMDB ID as TMDB writes it ("0949" is 949, and 0 is
// none), so a title has one key whatever the folder or a hand edit says.
func tmdbNumber(s string) string {
	if !tmdbID.MatchString(s) {
		return ""
	}
	n, err := strconv.Atoi(s)
	if err != nil || n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func matching(re *regexp.Regexp, s string) string {
	if re.MatchString(s) {
		return s
	}
	return ""
}

// maxAudioLanguages is how many audio languages of a file the index keeps.
const maxAudioLanguages = 12
