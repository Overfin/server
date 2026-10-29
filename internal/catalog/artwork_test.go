package catalog

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// The artwork kept on disk includes the library's collections' poster and
// backdrop.
func TestArtworkWanted(t *testing.T) {
	ctx := context.Background()
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: "a1000000000000000000000000000000", Kind: "movie", Name: "One", SortName: "one", TMDB: "1", LibraryID: "f1000000000000000000000000000000"},
	}, 1)
	st.RefreshTitleKeys(ctx)
	now := time.Now()
	if err := st.SetCollection(ctx, store.Collection{ID: 10, Name: "Saga", Poster: "/sagaposter1.jpg", Backdrop: "/sagaback123.jpg", Parts: []string{"1"}},
		[]store.Title{{Kind: "movie", TMDB: "1", Name: "One"}}, now); err != nil {
		t.Fatal(err)
	}
	wanted, err := (&LibraryArtwork{Store: st}).wanted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []images.TMDBRef{{Type: "poster", Path: "/sagaposter1.jpg"}, {Type: "backdrop", Path: "/sagaback123.jpg"}} {
		if !slices.Contains(wanted, ref) {
			t.Errorf("not kept: %v (%v)", ref, wanted)
		}
	}
}
