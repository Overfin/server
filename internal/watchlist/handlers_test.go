package watchlist

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/overfin/server/internal/feature"
	"github.com/overfin/server/internal/store"
)

// backend sees the titles of titles, by ID; items maps library items to
// their titles.
type backend struct {
	titles map[string]string // ID -> kind
	items  map[string]string
}

func (b backend) Title(_ context.Context, id string) (feature.Title, error) {
	kind, ok := b.titles[id]
	if !ok {
		return feature.Title{}, feature.ErrNotFound
	}
	return feature.Title{ID: id, Kind: kind}, nil
}

func (b backend) Titles(_ context.Context, ids []string) ([]feature.Title, error) {
	var out []feature.Title
	for _, id := range ids {
		if kind, ok := b.titles[id]; ok {
			out = append(out, feature.Title{ID: id, Kind: kind})
		}
	}
	return out, nil
}

func (b backend) TitleOfItem(_ context.Context, id string) (string, error) { return b.items[id], nil }

// The routes as a profile uses them: only titles it sees, of kinds one
// watches; adding one already there changes nothing, even on a full list;
// a full list refuses another; entries kept as library items become their
// titles once (one whose title is unknown, or already on the list, goes);
// removing; what it can't see left out of the list, kept in its export.
func TestHandlers(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenModule(ctx, "watchlist", filepath.Join(t.TempDir(), "w.db"), Feature{}.Info().Migrations)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b := backend{titles: map[string]string{"title_movie_tmdb_1": "movie", "title_series_tmdb_2": "series",
		"title_person_tmdb_3": "person", "title_movie_tmdb_4": "movie"},
		items: map[string]string{"item_a": "title_movie_tmdb_4", "item_b": "title_movie_tmdb_1"}}
	c := &feature.Caller{ProfileID: "p1", Backend: b}
	h := handlers{db: db, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	call := func(fn func(*feature.Caller, http.ResponseWriter, *http.Request), id string) (int, string) {
		t.Helper()
		r := httptest.NewRequest("GET", "/", nil)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		fn(c, w, r)
		return w.Code, w.Body.String()
	}
	listed := func() []string {
		t.Helper()
		_, body := call(h.list, "")
		var out struct{ Items []struct{ ID string } }
		json.Unmarshal([]byte(body), &out)
		var ids []string
		for _, it := range out.Items {
			ids = append(ids, it.ID)
		}
		return ids
	}

	for id, want := range map[string]int{
		"title_movie_tmdb_1":  204,
		"title_series_tmdb_2": 204,
		"title_person_tmdb_3": 400, // not a kind one watches
		"title_movie_tmdb_9":  404, // not seen
		"item_a":              404, // not a title ID
	} {
		if code, body := call(h.add, id); code != want {
			t.Errorf("add %s: %d %s, want %d", id, code, body, want)
		}
	}
	if code, body := call(h.has, "title_movie_tmdb_1"); code != 200 || !strings.Contains(body, `"on":true`) {
		t.Errorf("has: %d %s", code, body)
	}
	if code, body := call(h.has, "title_movie_tmdb_4"); code != 200 || !strings.Contains(body, `"on":false`) {
		t.Errorf("hasn't: %d %s", code, body)
	}
	if code, _ := call(h.has, "nope"); code != 404 {
		t.Errorf("has, not an ID: %d", code)
	}

	// Kept before titles had IDs: item_a becomes film 4, item_b (film 1,
	// already there) and item_c (unknown) go.
	for _, id := range []string{"item_a", "item_b", "item_c"} {
		db.ExecContext(ctx, `INSERT INTO watchlist (profile_id, item_id, added_at) VALUES ('p1', ?, 9999999999)`, id)
	}
	if got := listed(); !slices.Equal(got, []string{"title_movie_tmdb_4", "title_series_tmdb_2", "title_movie_tmdb_1"}) {
		t.Errorf("after turning items into titles: %v", got)
	}

	// Not seen any more: out of the list, in the export.
	delete(b.titles, "title_series_tmdb_2")
	if got := listed(); slices.Contains(got, "title_series_tmdb_2") {
		t.Errorf("a title no longer seen listed: %v", got)
	}
	if all, err := items(ctx, db, c, true); err != nil || len(all) != 3 || all[1].ID != "title_series_tmdb_2" {
		t.Errorf("export: %+v %v", all, err)
	}

	if code, _ := call(h.remove, "title_movie_tmdb_4"); code != 204 {
		t.Errorf("remove: %d", code)
	}
	if code, _ := call(h.remove, "x"); code != 404 {
		t.Errorf("remove, not an ID: %d", code)
	}

	// Full: another refused, one already there fine.
	for i := 0; i < Max; i++ {
		db.ExecContext(ctx, `INSERT INTO watchlist (profile_id, item_id, added_at) VALUES ('p1', ?, 1)`, "title_movie_tmdb_f"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('a'+i/26%26)))
	}
	b.titles["title_movie_tmdb_new"] = "movie"
	if code, body := call(h.add, "title_movie_tmdb_new"); code != 400 || !strings.Contains(body, "full") {
		t.Errorf("a full list: %d %s", code, body)
	}
	if code, _ := call(h.add, "title_movie_tmdb_1"); code != 204 {
		t.Errorf("one already on a full list: %d", code)
	}
}
