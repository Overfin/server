package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

func id(n int) string { return fmt.Sprintf("%032x", n) }

// library is a fake Jellyfin with Overfin's plugin: its items, by ID, the
// plugin's change feed, and the tokens it accepts (the service account's
// for the libraries' list, Overfin's key for the plugin).
type library struct {
	mu         sync.Mutex
	items      map[string]map[string]any
	generation string
	changes    []map[string]any
	last       int64
	token      string
	pages      int // index pages read
}

func (l *library) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	auth := r.Header.Get("Authorization")
	q := r.URL.Query()
	switch {
	case r.URL.Path == "/UserViews" && strings.Contains(auth, `Token="`+l.token+`"`):
		json.NewEncoder(w).Encode(map[string]any{"Items": []any{map[string]any{"Id": id(900), "Name": "All", "Path": "/media"}}})
	case !strings.Contains(auth, `Token="admin"`) || !strings.HasPrefix(r.URL.Path, "/Overfin/"):
		w.WriteHeader(http.StatusUnauthorized)
	case r.URL.Path == "/Overfin/Changes":
		since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
		if q.Get("generation") != l.generation {
			json.NewEncoder(w).Encode(map[string]any{"Generation": l.generation, "Last": l.last, "Reset": true, "Changes": []any{}})
			return
		}
		var out []map[string]any
		for _, c := range l.changes {
			if c["Seq"].(int64) > since {
				out = append(out, c)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"Generation": l.generation, "Last": l.last, "Changes": out})
	case r.URL.Path == "/Overfin/Items" && q.Has("ids"):
		var out []map[string]any
		for _, i := range strings.Split(q.Get("ids"), ",") {
			if it, ok := l.items[i]; ok {
				out = append(out, it)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": out})
	case r.URL.Path == "/Overfin/Items":
		l.pages++
		ids := slices.Sorted(maps.Keys(l.items))
		limit, _ := strconv.Atoi(q.Get("limit"))
		var out []map[string]any
		next := ""
		for _, i := range ids {
			if after := q.Get("after"); after != "" && i <= after {
				continue
			}
			if len(out) == limit {
				next = out[len(out)-1]["Id"].(string)
				break
			}
			out = append(out, l.items[i])
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": out, "Next": next})
	default:
		http.NotFound(w, r)
	}
}

// add puts an item in the library; change notes it in the feed.
func (l *library) add(n int, it map[string]any) {
	it["Id"] = id(n)
	if _, ok := it["LibraryId"]; !ok {
		it["LibraryId"] = id(900)
	}
	l.items[id(n)] = it
}

func (l *library) change(n int, removed bool) {
	l.last++
	l.changes = append(l.changes, map[string]any{"Seq": l.last, "Id": id(n), "Removed": removed})
}

type service struct {
	lib     *library
	refused int
}

func (s *service) Auth(context.Context) (jellyfin.Auth, string, error) {
	return jellyfin.Auth{Token: "token-" + strconv.Itoa(s.refused), DeviceID: "d"}, id(999), nil
}

func (s *service) Refused(context.Context) error {
	s.refused++
	return nil
}

func setup(t *testing.T) (*Sync, *library, *store.Store, *time.Time) {
	t.Helper()
	lib := &library{items: map[string]map[string]any{}, generation: "g1", token: "token-0"}
	srv := httptest.NewServer(lib)
	t.Cleanup(srv.Close)
	jf, _ := jellyfin.New(srv.URL, "test")
	box, _ := seal.New(bytes.Repeat([]byte{3}, 32))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	s := &Sync{Jellyfin: jf, Store: st, Service: &service{lib: lib}, Admin: func() jellyfin.Auth { return jellyfin.Auth{Token: "admin", DeviceID: "a"} },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Every: time.Second, now: func() time.Time { return now }}
	return s, lib, st, &now
}

func counts(t *testing.T, st *store.Store) map[string]int {
	t.Helper()
	c, err := st.CatalogCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSync(t *testing.T) {
	s, lib, st, now := setup(t)
	ctx := context.Background()
	lib.add(1, map[string]any{"Type": "Movie", "Name": "Heat <b>1995</b>", "ProductionYear": 1995,
		"ProviderIds":    map[string]string{"Tmdb": "949", "Imdb": "tt0113277", "Tvdb": "not-an-id"},
		"OfficialRating": "R", "FolderName": "Heat (1995)", "FileName": "Heat.mkv"})
	lib.add(2, map[string]any{"Type": "Series", "Name": "Dark", "FolderName": "Dark"})
	lib.add(3, map[string]any{"Type": "Season", "Name": "Season 1", "SeriesId": id(2), "IndexNumber": 1})
	lib.add(4, map[string]any{"Type": "Episode", "Name": "Secrets", "SeriesId": id(2), "SeasonId": id(3),
		"ParentIndexNumber": 1, "IndexNumber": 1})
	lib.add(5, map[string]any{"Type": "MusicAlbum", "Name": "not indexed"})
	// An ID that isn't one is left out.
	lib.items["../bad"] = map[string]any{"Type": "Movie", "Id": "../bad", "Name": "x"}

	// Each read that found titles says so (the titles' keeper wakes).
	reads := 0
	s.Read = func() { reads++ }

	// The first step reads everything, and the libraries' list.
	s.once(ctx)
	if c := counts(t, st); c["movie"] != 1 || c["series"] != 1 || c["season"] != 1 || c["episode"] != 1 {
		t.Fatalf("first read: %v (%s)", c, s.lastErr)
	}
	if libs, err := st.Libraries(ctx); err != nil || len(libs) != 1 || libs[0].ID != id(900) || strings.Contains(string(libs[0].Item), "/media") {
		t.Errorf("libraries: %v %v", libs, err)
	}
	if reads != 1 {
		t.Errorf("first read: told %d times", reads)
	}
	// Nothing changed: nothing read, nothing said.
	pages := lib.pages
	s.once(ctx)
	if reads != 1 || lib.pages != pages {
		t.Errorf("nothing new: told %d times, %d pages read", reads, lib.pages-pages)
	}

	// A change: the item read again by its ID, without a full read.
	lib.add(7, map[string]any{"Type": "Movie", "Name": "Arrival"})
	lib.change(7, false)
	s.once(ctx)
	if c := counts(t, st); c["movie"] != 2 || reads != 2 || lib.pages != pages {
		t.Errorf("after a change: %v, told %d times, %d pages read", c, reads, lib.pages-pages)
	}
	// A removal: the item no longer there, it leaves the index; one the
	// feed says changed but no longer in a library leaves it too.
	delete(lib.items, id(7))
	lib.change(7, true)
	delete(lib.items, id(4))
	lib.change(4, false)
	s.once(ctx)
	if c := counts(t, st); c["movie"] != 1 || c["episode"] != 0 {
		t.Errorf("after removals: %v (%s)", c, s.lastErr)
	}
	if st, _ := st.CatalogState(ctx); st.Pass != 1 {
		t.Errorf("a full read ran: %+v", st)
	}

	// Jellyfin started again (a new generation of the feed), and an item
	// left meanwhile without a word: everything is read again, and what
	// wasn't seen is removed.
	*now = now.Add(time.Hour)
	lib.generation, lib.changes, lib.last = "g2", nil, 0
	delete(lib.items, id(3))
	s.once(ctx)
	if c := counts(t, st); c["season"] != 0 || c["movie"] != 1 || c["series"] != 1 {
		t.Errorf("after a new generation: %v (%s)", c, s.lastErr)
	}
	stat, _ := s.Status(ctx)
	if !stat.FullAt.Equal(*now) || stat.Error != "" {
		t.Errorf("status: %+v", stat)
	}
	// The series has no TMDB ID; the film can be matched.
	if len(stat.Unmatched) != 1 || stat.Unmatched["series"] != 1 {
		t.Errorf("unmatched: %v", stat.Unmatched)
	}
	// The feed's place is the new generation's: a change in it is read.
	lib.add(8, map[string]any{"Type": "Movie", "Name": "Sicario"})
	lib.change(8, false)
	s.once(ctx)
	if c := counts(t, st); c["movie"] != 2 {
		t.Errorf("a change after the new generation: %v (%s)", c, s.lastErr)
	}

	// Jellyfin refuses the service account's token: it gets a new one,
	// which Jellyfin accepts, and the libraries are read.
	lib.token = "token-1"
	svc := s.Service.(*service)
	if err := s.libraries(ctx); err != nil || svc.refused != 1 {
		t.Errorf("refused token: %v, renewed %d times", err, svc.refused)
	}
}

// A full read goes page by page, by ID, to the end.
func TestFullPages(t *testing.T) {
	s, lib, st, _ := setup(t)
	ctx := context.Background()
	for n := 1; n <= pageSize+5; n++ {
		lib.add(n, map[string]any{"Type": "Movie", "Name": "Film " + strconv.Itoa(n)})
	}
	if err := s.Full(ctx); err != nil {
		t.Fatal(err)
	}
	if c := counts(t, st); c["movie"] != pageSize+5 || lib.pages != 2 {
		t.Errorf("%v in %d pages", c, lib.pages)
	}
}

func TestConvert(t *testing.T) {
	it, ok := convert(jellyfin.IndexItem{RawItem: jellyfin.RawItem{Id: id(1), Type: "Movie", Name: "<script>x</script>Heat",
		ProviderIds: map[string]string{"Tmdb": "949; DROP", "Imdb": "tt0113277"}},
		LibraryId: id(900), FolderName: "Heat (1995)\x00", FileName: "Heat.mkv"})
	if !ok || strings.Contains(it.Name, "<") || it.TMDB != "" || it.IMDb != "tt0113277" || it.SortName != it.Name ||
		it.LibraryID != id(900) || it.FolderName != "Heat (1995)" || it.FileName != "Heat.mkv" {
		t.Errorf("converted: %+v", it)
	}
	for raw, want := range map[string]string{"949": "949", "0949": "949", "0": "", "": ""} {
		it, _ := convert(jellyfin.IndexItem{RawItem: jellyfin.RawItem{Id: id(1), Type: "Movie", Name: "Heat", ProviderIds: map[string]string{"Tmdb": raw}}})
		if it.TMDB != want {
			t.Errorf("TMDB %q: %q, want %q", raw, it.TMDB, want)
		}
	}
	if _, ok := convert(jellyfin.IndexItem{RawItem: jellyfin.RawItem{Id: id(1), Type: "Folder"}}); ok {
		t.Error("a folder was indexed")
	}
	// The audio's languages are kept, two letters each, once.
	it, _ = convert(jellyfin.IndexItem{RawItem: jellyfin.RawItem{Id: id(1), Type: "Movie", Name: "19-2", MediaStreams: []jellyfin.MediaStream{
		{Type: "Video", Language: "eng"}, {Type: "Audio", Language: "fre"}, {Type: "Audio", Language: "fra"},
		{Type: "Audio", Language: "eng"}, {Type: "Subtitle", Language: "spa"}, {Type: "Audio", Language: "und"}}}})
	if !slices.Equal(it.AudioLanguages, []string{"fr", "en"}) {
		t.Errorf("audio %v", it.AudioLanguages)
	}
}
