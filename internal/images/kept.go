package images

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Library titles' artwork is kept on disk, ahead of time, so browsing the
// library never waits for TMDB: under ArtworkDir, each image's original as
// <type>/original/<file> (Keep), and the sizes made of it, the first time
// they're asked for, as <type>/<width>/<file> (TMDB). The rest (titles no
// library has, people) is fetched when first shown, and kept in memory
// only. Kept files are served like the others: the access check runs
// first, every time.

// keptFile is where an image is kept at a size ("original", or a width),
// "" when nothing is kept here.
func (p *Pipeline) keptFile(ref TMDBRef, size string) string {
	if p.ArtworkDir == "" || !tmdbPath.MatchString(ref.Path) {
		return ""
	}
	return filepath.Join(p.ArtworkDir, ref.Type, size, ref.Path[1:])
}

// writeKept keeps an image, whole or not at all: written aside, then
// renamed into place.
func writeKept(file string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".new-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), file)
}

// maxFailures is how many images in a row may fail before Keep gives up
// until its next pass (TMDB unreachable, say).
const maxFailures = 10

// Keep makes sure each image in wanted (type and path; Kind and TMDBID
// unused; those without a path skipped) has its original kept: those
// missing, or kept before refetch, are fetched from TMDB. Then the files
// no longer wanted are deleted: images whose titles left the libraries (or
// TMDB changed the picture), with the sizes made of them, and what isn't an
// original or one of its sizes (an older layout's); not when nothing is
// wanted, which is more likely a fault than an empty library. It returns
// how many originals it fetched, and how many files it deleted.
func (p *Pipeline) Keep(ctx context.Context, wanted []TMDBRef, refetch time.Time) (fetched, deleted int, err error) {
	if p.ArtworkDir == "" {
		return 0, 0, nil
	}
	keep := map[string]bool{} // originals, by file
	failures := 0
	for _, ref := range wanted {
		file := p.keptFile(ref, "original")
		if file == "" || keep[file] || len(ladders[ref.Type]) == 0 {
			continue
		}
		keep[file] = true
		if info, err := os.Stat(file); err == nil && info.ModTime().After(refetch) {
			continue
		}
		data, err := p.fetchOriginal(ctx, ref, p.background)
		if errors.Is(err, ErrHidden) || errors.Is(err, ErrUnsafe) {
			continue // TMDB has no such image, or a bad one: tried again next pass
		}
		if err == nil {
			err = writeKept(file, data)
		}
		if err != nil {
			if ctx.Err() != nil {
				return fetched, deleted, ctx.Err()
			}
			if failures++; failures >= maxFailures {
				return fetched, deleted, err
			}
			continue
		}
		failures = 0
		fetched++
	}
	if len(keep) == 0 {
		return fetched, 0, nil
	}
	err = filepath.WalkDir(p.ArtworkDir, func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || p.stillKept(file, keep) {
			return nil
		}
		// What's left: unwanted images, an older layout's, and files a stop
		// left half written.
		if err := os.Remove(file); err != nil {
			return err
		}
		if !strings.HasPrefix(d.Name(), ".new-") {
			deleted++
		}
		return nil
	})
	return fetched, deleted, err
}

// stillKept says whether a file under ArtworkDir is a wanted original, or
// a size made of one: <type>/original/<file>, <type>/<width>/<file>.
func (p *Pipeline) stillKept(file string, originals map[string]bool) bool {
	rel, err := filepath.Rel(p.ArtworkDir, file)
	if err != nil {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 3 || strings.HasPrefix(parts[2], ".new-") {
		return false
	}
	typ, size, name := parts[0], parts[1], parts[2]
	if size != "original" {
		w, err := strconv.Atoi(size)
		if err != nil || !slices.Contains(ladders[typ], w) || strconv.Itoa(w) != size {
			return false
		}
	}
	return originals[filepath.Join(p.ArtworkDir, typ, "original", name)]
}

// ForgetKept deletes every kept image: Overfin no longer uses TMDB's data,
// and its terms ask for it to be deleted then (SECURITY.md rule 57).
func (p *Pipeline) ForgetKept() error {
	if p == nil || p.ArtworkDir == "" {
		return nil
	}
	entries, err := os.ReadDir(p.ArtworkDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(p.ArtworkDir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
