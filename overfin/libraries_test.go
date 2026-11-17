package overfin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/jellyfin"
)

// In the backend, the folder chooser leaves out the image's own folders
// (Overfin's data and secrets, Jellyfin's settings): they aren't media.
func TestBrowseLeavesOutBackendFolders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"Name":"media","Path":"/media"},{"Name":"secrets","Path":"/secrets"},{"Name":"config","Path":"/config"},` +
			`{"Name":"data","Path":"/data"},{"Name":"cache","Path":"/cache"},{"Name":"pairing","Path":"/pairing"}]`))
	}))
	defer srv.Close()
	jf, err := jellyfin.New(srv.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	g := &gateway{sh: &shared{jf: jf}, cfg: &config.Config{}}

	got, err := libraries{g: g, own: backendFolders}.Browse(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"/media"}) {
		t.Errorf("backend: %v, want only /media", got)
	}
	// Elsewhere Jellyfin has its own container: /data may well be media.
	got, _ = libraries{g: g}.Browse(context.Background(), "/")
	if len(got) != 6 {
		t.Errorf("all-in-one: %v, want all six", got)
	}
}
