// Package jsonfile is an msfp.Store in JSON files, in one directory: for
// small hosts, with no database. Every commit writes the files it
// changed whole, each to a temporary file then renamed over the old one,
// so a file is always either the old or the new; files are mode 0600 and
// the directory 0700. One process at a time: Open takes a lock on the
// directory, and fails while another holds it.
//
// The files:
//
//	state.json        the server record, peers, codes, forgotten keys, library IDs, leftovers
//	peers/<id>.json   a peer's titles, and the keys of ours it was sent
//	own.json          the change log of the host's own catalog, when the engine keeps it
//	sealing.key       the key the seeds and secrets are sealed with (KeyFile)
//
// Seeds and secrets are never in the JSON in clear: they are sealed with
// AES-256-GCM (msfp.KeyFileSealer) under a key kept in its own file,
// sealing.key in the directory unless Options.KeyFile says elsewhere.
// Beside the data, it keeps them out of a state.json copied alone (a
// backup, a file sent with a bug report). Elsewhere (another disk, a
// path that isn't backed up with the data), it also keeps them from
// whoever copies the directory. A host with a secret store of its own
// gives msfp.Config.Sealer instead, and the key file is never made.
package jsonfile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/overfin/server/msfp"
	"github.com/overfin/server/msfp/store/internal/state"
)

// Options are the store's settings; the zero value is the default.
type Options struct {
	// KeyFile is where the sealing key is kept: sealing.key in the
	// directory by default. Made on first use.
	KeyFile string
}

// Store is an msfp.Store in a directory of JSON files.
type Store struct {
	dir  string
	opts Options
	s    *state.Store
	lock *os.File

	sealerOnce sync.Once
	sealer     msfp.Sealer
	sealerErr  error
}

var errLocked = errors.New("jsonfile: another process uses this directory")

// Open opens the store in dir, made when it doesn't exist.
func Open(dir string, opts ...Options) (*Store, error) {
	st := &Store{dir: dir}
	if len(opts) > 0 {
		st.opts = opts[0]
	}
	if st.opts.KeyFile == "" {
		st.opts.KeyFile = filepath.Join(dir, "sealing.key")
	}
	if err := os.MkdirAll(filepath.Join(dir, "peers"), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lock(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("%w: %s", err, dir)
	}
	st.lock = f
	s, err := st.load()
	if err != nil {
		f.Close()
		return nil, err
	}
	st.s = state.NewStore(s)
	st.s.Commit = st.commit
	return st, nil
}

// Close lets another process open the directory.
func (st *Store) Close() error { return st.lock.Close() }

// Read runs fn on a consistent view.
func (st *Store) Read(_ context.Context, fn func(msfp.Tx) error) error { return st.s.Read(fn) }

// Update runs fn in a transaction: all of it is written, or nothing.
func (st *Store) Update(_ context.Context, fn func(msfp.Tx) error) error { return st.s.Update(fn) }

// Sealer is an msfp.KeyFileSealer on Options.KeyFile, made on first use.
// A sealer that can't be made fails every seal, and Open of the engine
// with it.
func (st *Store) Sealer() msfp.Sealer {
	st.sealerOnce.Do(func() { st.sealer, st.sealerErr = msfp.KeyFileSealer(st.opts.KeyFile) })
	if st.sealerErr != nil {
		return failedSealer{st.sealerErr}
	}
	return st.sealer
}

type failedSealer struct{ err error }

func (f failedSealer) Seal([]byte, string) ([]byte, error) { return nil, f.err }
func (f failedSealer) Open([]byte, string) ([]byte, error) { return nil, f.err }

// peerID is the form of the engine's peer IDs, checked all the same
// before one names a file.
var peerID = regexp.MustCompile(`^[a-z0-9]{1,64}$`)

// peerFile is a peer's catalog file.
func (st *Store) peerFile(id string) (string, error) {
	if !peerID.MatchString(id) {
		return "", fmt.Errorf("jsonfile: a peer ID that can't be a file name: %q", id)
	}
	return filepath.Join(st.dir, "peers", id+".json"), nil
}

// load reads the files, those absent read as empty. A peer's catalog
// whose peer isn't in state.json (a crash between the two writes of a
// commit that deleted it) is removed.
func (st *Store) load() (*state.State, error) {
	s := state.New()
	if err := readJSON(filepath.Join(st.dir, "state.json"), s); err != nil {
		return nil, err
	}
	for _, m := range []*map[string]json.RawMessage{&s.Peers, &s.Codes, &s.Forgotten} {
		if *m == nil {
			*m = map[string]json.RawMessage{}
		}
	}
	if s.LibraryIDs == nil {
		s.LibraryIDs = map[string]string{}
	}
	if s.Leftovers == nil {
		s.Leftovers = map[string]msfp.Leftover{}
	}
	s.Catalogs, s.Own = map[string]*state.Catalog{}, &state.Own{}
	if err := readJSON(filepath.Join(st.dir, "own.json"), s.Own); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(st.dir, "peers"))
	if err != nil {
		return nil, err
	}
	for _, en := range entries {
		id, ok := strings.CutSuffix(en.Name(), ".json")
		if !ok {
			continue
		}
		path := filepath.Join(st.dir, "peers", en.Name())
		if _, known := s.Peers[id]; !known {
			if err := os.Remove(path); err != nil {
				return nil, err
			}
			continue
		}
		c := &state.Catalog{}
		if err := readJSON(path, c); err != nil {
			return nil, err
		}
		if c.Titles == nil {
			c.Titles = map[string]state.Stored{}
		}
		if c.Sent == nil {
			c.Sent = map[string]bool{}
		}
		s.Catalogs[id] = c
	}
	s.Index()
	return s, nil
}

// commit writes what an Update changed: the catalogs first, then
// state.json, so that a crash between leaves at worst a catalog without
// its peer, which load removes.
func (st *Store) commit(s *state.State, d state.Dirty) error {
	for id := range d.Catalogs {
		path, err := st.peerFile(id)
		if err != nil {
			return err
		}
		c := s.Catalogs[id]
		if c == nil {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		if err := writeJSON(path, c); err != nil {
			return err
		}
	}
	if d.Own {
		if err := writeJSON(filepath.Join(st.dir, "own.json"), s.Own); err != nil {
			return err
		}
	}
	if d.Records {
		return writeJSON(filepath.Join(st.dir, "state.json"), s)
	}
	return nil
}

// readJSON reads path into v; an absent file leaves v as it is.
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("jsonfile: %s: %w", path, err)
	}
	return nil
}

// writeJSON writes v to path atomically: to a temporary file in the same
// directory, synced, then renamed over path.
func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // nothing once renamed
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var (
	_ msfp.Store          = (*Store)(nil)
	_ msfp.SealerProvider = (*Store)(nil)
)
