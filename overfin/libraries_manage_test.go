package overfin

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/overfin/server/internal/admin"
	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// libJellyfin is a fake of Jellyfin's library management: its libraries
// (an ID from the name, as Jellyfin makes it), the folders it sees, and
// the calls made, in order.
type libJellyfin struct {
	mu      sync.Mutex
	libs    []fakeLib
	folders map[string]bool // folders that exist
	refuse  map[string]bool // folders it refuses (400) to add
	calls   []string
}

type fakeLib struct {
	Name, Type string
	Folders    []string
}

func libID(name string) string {
	sum := md5.Sum([]byte(name))
	return hex.EncodeToString(sum[:])
}

func (j *libJellyfin) find(name string) int {
	return slices.IndexFunc(j.libs, func(l fakeLib) bool { return l.Name == name })
}

func (j *libJellyfin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	j.mu.Lock()
	defer j.mu.Unlock()
	q := r.URL.Query()
	call := r.Method + " " + r.URL.Path
	switch call {
	case "GET /Library/VirtualFolders":
		var out []map[string]any
		for _, l := range j.libs {
			out = append(out, map[string]any{"Name": l.Name, "ItemId": libID(l.Name), "CollectionType": l.Type, "Locations": l.Folders})
		}
		json.NewEncoder(w).Encode(out)
		return
	case "GET /Environment/DirectoryContents":
		if !j.folders[q.Get("path")] {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`[]`))
		return
	}
	j.calls = append(j.calls, call+" "+q.Get("name")+q.Get("newName")+q.Get("path"))
	switch call {
	case "POST /Library/VirtualFolders":
		paths := q["paths"]
		for _, p := range paths {
			if j.refuse[p] {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		j.libs = append(j.libs, fakeLib{Name: q.Get("name"), Type: q.Get("collectionType"), Folders: paths})
	case "POST /Library/VirtualFolders/Paths":
		var in struct {
			Name     string
			PathInfo struct{ Path string }
		}
		json.NewDecoder(r.Body).Decode(&in)
		j.calls[len(j.calls)-1] += in.Name + " " + in.PathInfo.Path
		if j.refuse[in.PathInfo.Path] {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		i := j.find(in.Name)
		j.libs[i].Folders = append(j.libs[i].Folders, in.PathInfo.Path)
	case "DELETE /Library/VirtualFolders/Paths":
		i := j.find(q.Get("name"))
		j.libs[i].Folders = slices.DeleteFunc(j.libs[i].Folders, func(f string) bool { return f == q.Get("path") })
	case "POST /Library/VirtualFolders/Name":
		j.libs[j.find(q.Get("name"))].Name = q.Get("newName")
	case "DELETE /Library/VirtualFolders":
		j.libs = slices.DeleteFunc(j.libs, func(l fakeLib) bool { return l.Name == q.Get("name") })
	case "POST /Library/Refresh":
	default:
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (j *libJellyfin) took() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	c := j.calls
	j.calls = nil
	return c
}

func libSetup(t *testing.T) (libraries, *libJellyfin) {
	t.Helper()
	j := &libJellyfin{folders: map[string]bool{"/media/films": true, "/media/more": true, "/media/series": true},
		refuse: map[string]bool{}}
	srv := httptest.NewServer(j)
	t.Cleanup(srv.Close)
	jf, err := jellyfin.New(srv.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	box, _ := seal.New(bytes.Repeat([]byte{5}, 32))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "o.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	g := &gateway{sh: &shared{jf: jf, db: st}, cfg: &config.Config{}}
	return libraries{g: g}, j
}

func isProblem(err error, has string) bool {
	var p admin.Problem
	return errors.As(err, &p) && strings.Contains(string(p), has)
}

func TestLibraryAddAndRemove(t *testing.T) {
	ctx := context.Background()
	l, j := libSetup(t)
	if err := l.Add(ctx, "Films", "films", []string{"/media/films"}); err != nil {
		t.Fatal(err)
	}
	if got := j.took(); len(got) != 2 || !strings.HasPrefix(got[0], "POST /Library/VirtualFolders Films") || got[1] != "POST /Library/Refresh " {
		t.Errorf("add: %q", got)
	}
	list, err := l.List(ctx)
	if err != nil || len(list) != 1 || list[0].Kind != "films" || !slices.Equal(list[0].Folders, []string{"/media/films"}) {
		t.Errorf("list: %+v %v", list, err)
	}
	// The same name again, a folder Jellyfin doesn't see, one it refuses:
	// problems, nothing made.
	if err := l.Add(ctx, "Films", "films", []string{"/media/more"}); !isProblem(err, "already") {
		t.Errorf("same name: %v", err)
	}
	if err := l.Add(ctx, "Other", "series", []string{"/nowhere"}); !isProblem(err, "doesn't exist") {
		t.Errorf("missing folder: %v", err)
	}
	j.refuse["/media/series"] = true
	if err := l.Add(ctx, "Series", "series", []string{"/media/series"}); !isProblem(err, "can't use") {
		t.Errorf("refused folder: %v", err)
	}
	if got := j.took(); len(got) != 1 { // only the refused add reached Jellyfin
		t.Errorf("calls for refused adds: %q", got)
	}
	if err := l.Remove(ctx, "Nothing"); !isProblem(err, "no library") {
		t.Errorf("remove a library that isn't: %v", err)
	}
	if err := l.Remove(ctx, "Films"); err != nil {
		t.Fatal(err)
	}
	if list, _ := l.List(ctx); len(list) != 0 {
		t.Errorf("after remove: %+v", list)
	}
}

// A change: folders added and taken away, the name changed; a new name
// already taken is refused before anything changes; a folder Jellyfin
// refuses stops the change with a problem.
func TestLibraryChange(t *testing.T) {
	ctx := context.Background()
	l, j := libSetup(t)
	j.libs = []fakeLib{{Name: "Films", Type: "movies", Folders: []string{"/media/films"}}, {Name: "Series", Type: "tvshows", Folders: []string{"/media/series"}}}

	if err := l.Change(ctx, "Films", "Series", []string{"/media/more"}); !isProblem(err, "already") {
		t.Fatalf("a name taken: %v", err)
	}
	if got := j.took(); len(got) != 0 {
		t.Errorf("a refused change touched Jellyfin: %q", got)
	}

	if err := l.Change(ctx, "Films", "Movies", []string{"/media/more"}); err != nil {
		t.Fatal(err)
	}
	got := j.took()
	want := []string{
		"POST /Library/VirtualFolders/Paths Films /media/more",
		"DELETE /Library/VirtualFolders/Paths Films/media/films",
		"POST /Library/VirtualFolders/Name FilmsMovies",
		"POST /Library/Refresh ",
	}
	if !slices.Equal(got, want) {
		t.Errorf("change:\n got %q\nwant %q", got, want)
	}
	list, _ := l.List(ctx)
	if i := slices.IndexFunc(list, func(m admin.MediaLibrary) bool { return m.Name == "Movies" }); i < 0 || !slices.Equal(list[i].Folders, []string{"/media/more"}) {
		t.Errorf("after the change: %+v", list)
	}

	// A rename alone: no folders touched, no scan.
	if err := l.Change(ctx, "Movies", "Films", nil); err != nil {
		t.Fatal(err)
	}
	if got := j.took(); !slices.Equal(got, []string{"POST /Library/VirtualFolders/Name MoviesFilms"}) {
		t.Errorf("rename alone: %q", got)
	}
	// The same name and the same folders: nothing but the scan.
	if err := l.Change(ctx, "Films", "Films", []string{"/media/more"}); err != nil {
		t.Fatal(err)
	}
	if got := j.took(); !slices.Equal(got, []string{"POST /Library/Refresh "}) {
		t.Errorf("no change: %q", got)
	}
	// A folder Jellyfin refuses, one it doesn't see.
	j.refuse["/media/films"] = true
	if err := l.Change(ctx, "Films", "", []string{"/media/films"}); !isProblem(err, "can't use /media/films") {
		t.Errorf("refused folder: %v", err)
	}
	if err := l.Change(ctx, "Films", "", []string{"/gone"}); !isProblem(err, "doesn't exist") {
		t.Errorf("missing folder: %v", err)
	}
	if err := l.Change(ctx, "Nothing", "X", nil); !isProblem(err, "no library") {
		t.Errorf("a library that isn't: %v", err)
	}
}
