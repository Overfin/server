// Package files is how the backend reads the libraries' files (rule 78):
// a path Jellyfin gives is resolved to its real path, which must be under
// one of the libraries' folders and under none of Overfin's own; the file
// is opened read-only and checked to be a regular file, the one the real
// path names. A path is never logged or kept: only what a file holds (its
// fingerprint, its facts) is.
//
// Its Cache keeps each version's fingerprint while the file's size and
// time don't change, for the seek-bar previews and the copies alike.
package files

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// Own are the folders of Overfin's container that are never a library's,
// whatever Jellyfin says: its database, secrets, settings, caches, local
// socket, pairing, and temporary files.
var Own = []string{"/data", "/secrets", "/config", "/cache", "/local", "/pairing", "/tmp"}

// Errors of Open and Resolve. None carries the path.
var (
	// ErrOutside is a path whose real path isn't under a library folder,
	// or is under one of Own.
	ErrOutside = errors.New("files: not in a library folder")
	// ErrNotRegular is a path that isn't a regular file: a directory, a
	// FIFO, a device, a socket.
	ErrNotRegular = errors.New("files: not a regular file")
	// ErrUnreadable is a file that can't be resolved, opened or read now.
	ErrUnreadable = errors.New("files: can't be read")
)

// Libraries are the libraries' folders, each as its real path; folders
// that don't resolve, or are under one of Own, are left out.
type Libraries struct {
	roots []string
}

// NewLibraries resolves the libraries' folders as Jellyfin lists them.
func NewLibraries(folders []string) Libraries {
	var l Libraries
	for _, f := range folders {
		if !filepath.IsAbs(f) {
			continue
		}
		real, err := filepath.EvalSymlinks(f)
		if err != nil || ownFolder(real) {
			continue
		}
		l.roots = append(l.roots, filepath.Clean(real))
	}
	return l
}

// Empty says no folder is left: every file is refused.
func (l Libraries) Empty() bool { return len(l.roots) == 0 }

// under says whether path is dir or below it.
func under(path, dir string) bool {
	if dir == string(filepath.Separator) {
		return true
	}
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

func ownFolder(path string) bool {
	for _, o := range Own {
		if under(path, o) {
			return true
		}
	}
	return false
}

// Resolve is path's real path and its library folder: links resolved, the
// result under a library folder and none of Own.
func (l Libraries) Resolve(path string) (real, root string, err error) {
	if !filepath.IsAbs(path) {
		return "", "", ErrOutside
	}
	real, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", ErrUnreadable
	}
	real = filepath.Clean(real)
	if ownFolder(real) {
		return "", "", ErrOutside
	}
	// The deepest folder holding it, so a library inside another's folder
	// is the file's library whatever order Jellyfin lists them in.
	for _, r := range l.roots {
		if under(real, r) && real != r && len(r) > len(root) {
			root = r
		}
	}
	if root == "" {
		return "", "", ErrOutside
	}
	return real, root, nil
}

// File is a library's file, open read-only.
type File struct {
	*os.File
	// Real is its real path; Root, its library folder (real path); Rel,
	// its path relative to Root. Root and Rel are the copy ID's input.
	// None is ever logged or kept.
	Real, Root, Rel string
	Info            fs.FileInfo
}

// Open resolves path (Resolve) and opens the real file read-only, without
// following a last link nor blocking on a FIFO: it must be a regular file,
// the same one the real path names.
func (l Libraries) Open(path string) (*File, error) {
	real, root, err := l.Resolve(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Lstat(real)
	if err != nil {
		return nil, ErrUnreadable
	}
	if !st.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	f, err := os.OpenFile(real, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrUnreadable
	}
	fst, err := f.Stat()
	if err != nil || !fst.Mode().IsRegular() || !os.SameFile(st, fst) {
		f.Close()
		return nil, ErrNotRegular
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		f.Close()
		return nil, ErrOutside
	}
	return &File{File: f, Real: real, Root: root, Rel: filepath.ToSlash(rel), Info: fst}, nil
}

// chunk is how much of a file its fingerprint reads, at its start, middle
// and end.
const chunk = 256 << 10

// Fingerprint names a file by what's in it: its size and a hash of three
// pieces of it. The same file anywhere has the same; another encode, cut
// or upgrade, another.
func Fingerprint(f *File) (string, error) {
	size := f.Info.Size()
	h := sha256.New()
	binary.Write(h, binary.BigEndian, size)
	buf := make([]byte, chunk)
	for _, at := range []int64{0, size/2 - chunk/2, size - chunk} {
		if at < 0 {
			at = 0
		}
		n, err := f.ReadAt(buf, at)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", ErrUnreadable
		}
		h.Write(buf[:n])
	}
	return strconv.FormatInt(size, 36) + "-" + hex.EncodeToString(h.Sum(nil))[:32], nil
}

// FingerprintForm is what a fingerprint looks like.
var FingerprintForm = regexp.MustCompile(`^[0-9a-z]{1,14}-[0-9a-f]{32}$`)
