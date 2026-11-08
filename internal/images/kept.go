package images

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"image"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Library titles' artwork is kept on disk, ahead of time, so browsing the
// library never waits for TMDB or for a size to be made: under ArtworkDir,
// each image's original as <type>/original/<file>, and every size of it
// served narrower than it, as <type>/<width>/<file> (Keep, in the
// background; TMDB makes one asked for before Keep got to it). The
// original, no wider than the largest size served, is itself every size
// it's no wider than: those have no file of their own. Which images are
// kept is the caller's (catalog.LibraryArtwork: the library's, and what
// Discover shows); the rest (other titles no library has, people) is
// fetched when first shown, and kept in memory only. Kept files are
// served like the others: the access check runs first, every time.

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
// missing, or kept before refetch, are fetched from TMDB. Then every size
// served narrower than it is made, those missing, one image at a time in
// the background slot. Then the files no longer wanted are deleted: images
// whose titles left the libraries (or TMDB changed the picture), with the
// sizes made of them, and what isn't an original or one of its sizes (an
// older layout's); not when nothing is wanted, which is more likely a
// fault than an empty library. It returns how many originals it fetched,
// how many sizes it made, and how many files it deleted.
func (p *Pipeline) Keep(ctx context.Context, wanted []TMDBRef, refetch time.Time) (fetched, made, deleted int, err error) {
	if p.ArtworkDir == "" {
		return 0, 0, 0, nil
	}
	keep := map[string]bool{} // originals, by file
	failures := 0
	// failed says whether a failure ends the pass: a stop, or too many in
	// a row.
	failed := func(err error) bool {
		if ctx.Err() != nil {
			return true
		}
		failures++
		return failures >= maxFailures
	}
	for _, ref := range wanted {
		file := p.keptFile(ref, "original")
		if file == "" || keep[file] || len(ladders[ref.Type]) == 0 {
			continue
		}
		keep[file] = true
		if info, err := os.Stat(file); err != nil || !info.ModTime().After(refetch) {
			data, err := p.fetchOriginal(ctx, ref, p.background)
			if errors.Is(err, ErrHidden) || errors.Is(err, ErrUnsafe) {
				continue // TMDB has no such image, or a bad one: tried again next pass
			}
			if err == nil {
				err = writeKept(file, data)
			}
			if err != nil {
				if failed(err) {
					return fetched, made, deleted, cmp.Or(ctx.Err(), err)
				}
				continue
			}
			fetched++
		}
		n, gone, err := p.keepSizes(ctx, ref, file)
		made, deleted = made+n, deleted+gone
		if errors.Is(err, ErrUnsafe) {
			continue // a damaged original: fetched again when old
		}
		if err != nil {
			if failed(err) {
				return fetched, made, deleted, cmp.Or(ctx.Err(), err)
			}
			continue
		}
		failures = 0
	}
	if len(keep) == 0 {
		return fetched, made, 0, nil
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
	if err != nil {
		return fetched, made, deleted, err
	}
	// An older layout's folders, emptied: gone too.
	types, err := os.ReadDir(p.ArtworkDir)
	if err != nil {
		return fetched, made, deleted, err
	}
	for _, t := range types {
		sizes, err := os.ReadDir(filepath.Join(p.ArtworkDir, t.Name()))
		if err != nil {
			continue
		}
		for _, size := range sizes {
			w, convErr := strconv.Atoi(size.Name())
			if size.IsDir() && size.Name() != "original" && (convErr != nil || !slices.Contains(ladders[t.Name()], w)) {
				// Only empty ones: os.Remove leaves the others.
				_ = os.Remove(filepath.Join(p.ArtworkDir, t.Name(), size.Name()))
			}
		}
	}
	return fetched, made, deleted, nil
}

// keepSizes makes the sizes served narrower than an image's original,
// kept as file, that aren't kept yet, and deletes those it isn't narrower
// than (the original serves them: an older Overfin kept copies). It
// returns how many it made and deleted. It reads the original's header
// every time, the whole of it only when a size is missing.
func (p *Pipeline) keepSizes(ctx context.Context, ref TMDBRef, file string) (made, deleted int, err error) {
	wide, err := imageWidth(file)
	if err != nil {
		return 0, 0, err
	}
	var missing []int
	for _, w := range ladders[ref.Type] {
		size := p.keptFile(ref, strconv.Itoa(w))
		_, err := os.Stat(size)
		switch {
		case w >= wide && err == nil:
			if err := os.Remove(size); err != nil {
				return 0, deleted, err
			}
			deleted++
		case w < wide && err != nil:
			missing = append(missing, w)
		}
	}
	if len(missing) == 0 {
		return 0, deleted, nil
	}
	original, err := os.ReadFile(file)
	if err != nil {
		return 0, deleted, err
	}
	select {
	case p.background <- struct{}{}:
	case <-ctx.Done():
		return 0, deleted, ctx.Err()
	}
	defer func() { <-p.background }()
	img, err := decodeChecked(original)
	if err != nil {
		return 0, deleted, err
	}
	for _, w := range missing {
		if ctx.Err() != nil {
			return made, deleted, ctx.Err()
		}
		data, err := encode(Shrink(img, w), contentTypeOf(ref.Type))
		if err == nil {
			err = writeKept(p.keptFile(ref, strconv.Itoa(w)), data)
		}
		if err != nil {
			return made, deleted, err
		}
		made++
	}
	return made, deleted, nil
}

// imageWidth reads a kept image's width from its header: ErrUnsafe when
// it isn't one.
func imageWidth(file string) (int, error) {
	f, err := os.Open(file)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(bufio.NewReader(f))
	if err != nil {
		return 0, ErrUnsafe
	}
	return cfg.Width, nil
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
