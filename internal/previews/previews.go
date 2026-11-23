// Package previews makes and keeps seek-bar previews (trickplay) itself,
// in Jellyfin's format: sheets of thumbnails, one every few seconds. They
// are kept by each file's fingerprint, not by Jellyfin's item, so a file
// moved or renamed keeps its previews, and another copy of the same file
// shares them. The backend reads the files (read-only, where Jellyfin
// sees them) only for that: their paths are used and forgotten, never
// kept, logged or served (rule 78).
package previews

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/overfin/server/internal/files"
)

// Set describes a file's previews: thumbnails Width x Height, on sheets of
// Columns x Rows, Count in all, one every IntervalMs.
type Set struct {
	Width      int `json:"width"`
	Height     int `json:"height"`
	Columns    int `json:"columns"`
	Rows       int `json:"rows"`
	Count      int `json:"count"`
	IntervalMs int `json:"intervalMs"`
}

// Sheets is how many sheets the set has.
func (s Set) Sheets() int {
	per := s.Columns * s.Rows
	if per <= 0 {
		return 0
	}
	return (s.Count + per - 1) / per
}

// valid says whether a set is one Overfin would make or keep.
func (s Set) valid() bool {
	return s.Width > 0 && s.Width <= 1024 && s.Height > 0 && s.Height <= 1024 && s.Columns > 0 && s.Columns <= 32 &&
		s.Rows > 0 && s.Rows <= 32 && s.Count > 0 && s.Count <= 100_000 && s.IntervalMs >= 1000
}

// fingerprintRe is what a fingerprint looks like (files.Fingerprint).
var fingerprintRe = files.FingerprintForm

// Store keeps previews by fingerprint, one folder each: set.json and the
// sheets, 0.jpg on.
type Store struct {
	dir string
}

// NewStore keeps previews in dir.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, ".making"), 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) folder(fp string) (string, error) {
	if !fingerprintRe.MatchString(fp) {
		return "", errors.New("previews: not a fingerprint")
	}
	return filepath.Join(s.dir, fp[len(fp)-32:len(fp)-30], fp), nil
}

// Get is a fingerprint's set; ok is false when it has none.
func (s *Store) Get(fp string) (set Set, ok bool) {
	dir, err := s.folder(fp)
	if err != nil {
		return Set{}, false
	}
	b, err := os.ReadFile(filepath.Join(dir, "set.json"))
	if err != nil || json.Unmarshal(b, &set) != nil || !set.valid() {
		return Set{}, false
	}
	return set, true
}

// Sheet is one sheet of a fingerprint's previews.
func (s *Store) Sheet(fp string, n int) ([]byte, error) {
	dir, err := s.folder(fp)
	if err != nil {
		return nil, err
	}
	set, ok := s.Get(fp)
	if !ok || n < 0 || n >= set.Sheets() {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(filepath.Join(dir, strconv.Itoa(n)+".jpg"))
}

// put keeps a set whose sheets are in made (a folder of the store's own),
// as the fingerprint's, whole: a set is there with all its sheets, or not
// at all.
func (s *Store) put(fp string, set Set, made string) error {
	if !set.valid() {
		return errors.New("previews: not a set to keep")
	}
	dir, err := s.folder(fp)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(set)
	if err := os.WriteFile(filepath.Join(made, "set.json"), b, 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	os.RemoveAll(dir) // a set made again replaces the former
	return os.Rename(made, dir)
}

// making is a new folder to make a set in, on the store's own disk.
func (s *Store) making() (string, error) {
	return os.MkdirTemp(filepath.Join(s.dir, ".making"), "set-")
}

// Remove deletes a fingerprint's previews.
func (s *Store) Remove(fp string) error {
	dir, err := s.folder(fp)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// All are the fingerprints the store has sets for.
func (s *Store) All() ([]string, error) {
	var out []string
	groups, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if !g.IsDir() || strings.HasPrefix(g.Name(), ".") {
			continue
		}
		sets, err := os.ReadDir(filepath.Join(s.dir, g.Name()))
		if err != nil {
			return nil, err
		}
		for _, e := range sets {
			if e.IsDir() && fingerprintRe.MatchString(e.Name()) {
				out = append(out, e.Name())
			}
		}
	}
	return out, nil
}

// clearMaking empties what a stopped run left half made.
func (s *Store) clearMaking() {
	left, _ := os.ReadDir(filepath.Join(s.dir, ".making"))
	for _, e := range left {
		os.RemoveAll(filepath.Join(s.dir, ".making", e.Name()))
	}
}

// adopt copies a set of sheets made elsewhere (Jellyfin's) into the store
// under fp, then deletes them there: across disks or mounts, a copy, file
// by file.
func (s *Store) adopt(fp string, set Set, from string) error {
	made, err := s.making()
	if err != nil {
		return err
	}
	for n := range set.Sheets() {
		name := strconv.Itoa(n) + ".jpg"
		if err := copyFile(filepath.Join(from, name), filepath.Join(made, name)); err != nil {
			os.RemoveAll(made)
			return fmt.Errorf("sheet %d: %w", n, err)
		}
	}
	if err := s.put(fp, set, made); err != nil {
		os.RemoveAll(made)
		return err
	}
	return os.RemoveAll(from)
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
