package files

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Only a library's regular files open: a link out of the libraries, a
// folder, a FIFO, a path that is relative or goes nowhere are refused; a
// link inside the libraries opens its target, named relative to its own
// library.
func TestOpen(t *testing.T) {
	dir := t.TempDir()
	films, series, secret := filepath.Join(dir, "films"), filepath.Join(dir, "series"), filepath.Join(dir, "data")
	write(t, filepath.Join(films, "A (2001)", "a.mkv"), []byte("film"))
	write(t, filepath.Join(series, "S", "e1.mkv"), []byte("episode"))
	write(t, filepath.Join(secret, "overfin.db"), []byte("secret"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Symlink(filepath.Join(secret, "overfin.db"), filepath.Join(films, "escape.mkv")))
	must(os.Symlink(secret, filepath.Join(films, "folder-escape")))
	must(os.Symlink(filepath.Join(series, "S", "e1.mkv"), filepath.Join(films, "inside.mkv")))
	must(syscall.Mkfifo(filepath.Join(films, "fifo.mkv"), 0o600))
	// A library folder that is itself a link resolves to its target.
	must(os.Symlink(series, filepath.Join(dir, "series-link")))

	libs := NewLibraries([]string{films, filepath.Join(dir, "series-link"), "relative/folder", filepath.Join(dir, "missing")})
	if len(libs.roots) != 2 {
		t.Fatalf("roots: %v", libs.roots)
	}
	f, err := libs.Open(filepath.Join(films, "A (2001)", "a.mkv"))
	must(err)
	if f.Rel != "A (2001)/a.mkv" || f.Info.Size() != 4 {
		t.Errorf("rel %q size %d", f.Rel, f.Info.Size())
	}
	f.Close()
	f, err = libs.Open(filepath.Join(films, "inside.mkv"))
	must(err)
	if f.Rel != "S/e1.mkv" {
		t.Errorf("a link inside: %q", f.Rel)
	}
	f.Close()

	for name, want := range map[string]error{
		filepath.Join(films, "escape.mkv"):                  ErrOutside,
		filepath.Join(films, "folder-escape", "overfin.db"): ErrOutside,
		filepath.Join(secret, "overfin.db"):                 ErrOutside,
		filepath.Join(films, "A (2001)"):                    ErrNotRegular,
		filepath.Join(films, "fifo.mkv"):                    ErrNotRegular,
		films:                                               ErrOutside, // the folder itself
		"A (2001)/a.mkv":                                    ErrOutside,
		filepath.Join(films, "gone.mkv"):                    ErrUnreadable,
	} {
		f, err := libs.Open(name)
		if !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", name, err, want)
		}
		if f != nil {
			f.Close()
		}
		if err != nil && bytes.Contains([]byte(err.Error()), []byte(dir)) {
			t.Errorf("an error names the path: %v", err)
		}
	}
}

// Overfin's own folders are never a library's, even when Jellyfin lists
// one, nor is anything under them.
func TestOwnFolders(t *testing.T) {
	dir := t.TempDir()
	own := filepath.Join(dir, "cache")
	write(t, filepath.Join(own, "media", "a.mkv"), []byte("x"))
	saved := Own
	Own = append(slices.Clone(Own), own)
	defer func() { Own = saved }()
	if libs := NewLibraries([]string{filepath.Join(own, "media")}); !libs.Empty() {
		t.Error("a folder under Overfin's own kept")
	}
	libs := NewLibraries([]string{dir})
	if _, err := libs.Open(filepath.Join(own, "media", "a.mkv")); !errors.Is(err, ErrOutside) {
		t.Errorf("a file under Overfin's own folder: %v", err)
	}
	if _, _, err := (Libraries{}).Resolve(filepath.Join(own, "media", "a.mkv")); !errors.Is(err, ErrOutside) {
		t.Errorf("no library: %v", err)
	}
}

// A file's fingerprint follows its content: a copy elsewhere has the same,
// a change anywhere it reads, another; a small file is read whole.
func TestFingerprint(t *testing.T) {
	dir := t.TempDir()
	big := bytes.Repeat([]byte("frame "), 1<<20) // 6 MiB
	write(t, filepath.Join(dir, "a.mkv"), big)
	write(t, filepath.Join(dir, "moved", "b.mkv"), big)
	changed := slices.Clone(big)
	changed[len(changed)-10] = 'X'
	write(t, filepath.Join(dir, "c.mkv"), changed)
	write(t, filepath.Join(dir, "small.mkv"), []byte("tiny"))
	libs := NewLibraries([]string{dir})
	fp := func(name string) string {
		t.Helper()
		f, err := libs.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		s, err := Fingerprint(f)
		if err != nil {
			t.Fatal(err)
		}
		if !FingerprintForm.MatchString(s) {
			t.Fatalf("%s: %q", name, s)
		}
		return s
	}
	if fp("a.mkv") != fp("moved/b.mkv") {
		t.Error("a copy has another fingerprint")
	}
	if fp("a.mkv") == fp("c.mkv") {
		t.Error("a changed end kept the fingerprint")
	}
	fp("small.mkv")
}

// The cache keeps a version's fingerprint while its size and time stay,
// and takes it again when either changes.
func TestCache(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	db, err := store.Open(ctx, filepath.Join(dir, "overfin.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	media := filepath.Join(dir, "media")
	path := filepath.Join(media, "a.mkv")
	write(t, path, []byte("first"))
	libs := NewLibraries([]string{media})
	c := Cache{DB: db}
	get := func() string {
		t.Helper()
		f, err := libs.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		fp, err := c.Fingerprint(ctx, "item", "source", f, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return fp
	}
	first := get()
	// Same size and time, other bytes: the kept one (not read again).
	write(t, path, []byte("other"))
	old := time.Now().Add(-time.Hour)
	os.Chtimes(path, old, old)
	second := get()
	write(t, path, []byte("other"))
	os.Chtimes(path, old, old)
	if third := get(); third != second {
		t.Error("taken again with the same size and time")
	}
	if first == second {
		t.Error("not taken again after a change of time")
	}
}
