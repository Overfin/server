// Package state is what the shipped stores share: the engine's records
// in memory, read and written in transactions, copied on write. The
// memory store keeps it as it is; the JSON file store writes it to files
// at each commit.
//
// Records are kept as their JSON: a record read back is a new copy, so
// nothing the engine changes reaches the store until it puts it back,
// and a transaction that fails leaves nothing behind.
package state

import (
	"cmp"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/overfin/server/msfp"
)

// State is everything a store holds.
type State struct {
	Server     json.RawMessage            `json:"server,omitempty"`
	Peers      map[string]json.RawMessage `json:"peers"`
	Codes      map[string]json.RawMessage `json:"codes"`
	Forgotten  map[string]json.RawMessage `json:"forgotten"` // by the key hash, hex
	LibraryIDs map[string]string          `json:"libraryIds"`
	Leftovers  map[string]msfp.Leftover   `json:"leftovers"` // by peer, a line feed, playback ID

	// Catalogs are the peers' titles and the keys each was sent, by peer
	// ID: kept apart, since they are big and change on their own.
	Catalogs map[string]*Catalog `json:"-"`
	// Own is the change log of the host's own catalog.
	Own *Own `json:"-"`

	// byKey finds a peer by its current key (hex); made from Peers.
	byKey map[string]string
}

// Catalog is one peer's titles, and the keys of our titles it was sent.
type Catalog struct {
	Titles map[string]Stored `json:"titles"`
	Sent   map[string]bool   `json:"sent"`
}

// Stored is a peer's title: the reading it was last seen in, and the
// title as msfp.MarshalTitle writes it.
type Stored struct {
	Reading int64           `json:"reading"`
	Title   json.RawMessage `json:"title"`
}

// Own is the change log of the host's own catalog.
type Own struct {
	Snapshot map[string]json.RawMessage `json:"snapshot"`
	Changes  []msfp.OwnChange           `json:"changes"`
	Floor    int64                      `json:"floor"`
}

// New is an empty state.
func New() *State {
	s := &State{Peers: map[string]json.RawMessage{}, Codes: map[string]json.RawMessage{},
		Forgotten: map[string]json.RawMessage{}, LibraryIDs: map[string]string{},
		Leftovers: map[string]msfp.Leftover{}, Catalogs: map[string]*Catalog{}, Own: &Own{}}
	s.Index()
	return s
}

// Index makes the lookups made from what was read (after loading).
func (s *State) Index() {
	s.byKey = map[string]string{}
	for id, raw := range s.Peers {
		var p struct {
			Key []byte `json:"key"`
		}
		if json.Unmarshal(raw, &p) == nil {
			s.byKey[hex.EncodeToString(p.Key)] = id
		}
	}
}

// Dirty says what a transaction changed: the records, which peers'
// catalogs, the own log.
type Dirty struct {
	Records  bool
	Catalogs map[string]bool
	Own      bool
}

// Store holds a State, one Update at a time; Commit, when set, writes
// what an Update changed before it takes effect (the JSON file store).
type Store struct {
	mu  sync.RWMutex
	cur *State
	// Commit is told the new state and what changed; an error undoes the
	// Update.
	Commit func(s *State, d Dirty) error
}

// NewStore holds s.
func NewStore(s *State) *Store { return &Store{cur: s} }

// Read runs fn on the state as it is.
func (st *Store) Read(fn func(msfp.Tx) error) error {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return fn(&tx{s: st.cur, readOnly: true})
}

// Update runs fn on a copy, kept when fn and the commit succeed.
func (st *Store) Update(fn func(msfp.Tx) error) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	t := &tx{s: shallow(st.cur), dirty: Dirty{Catalogs: map[string]bool{}}, cloned: map[string]bool{}}
	if err := fn(t); err != nil {
		return err
	}
	if st.Commit != nil {
		if err := st.Commit(t.s, t.dirty); err != nil {
			return err
		}
	}
	st.cur = t.s
	return nil
}

// shallow is a copy of s sharing its maps: each is cloned on its first
// write (tx.own).
func shallow(s *State) *State {
	c := *s
	return &c
}

// errReadOnly is a write inside Read.
var errReadOnly = errors.New("store: a write inside a read")

// tx is a transaction on a state.
type tx struct {
	s        *State
	readOnly bool
	dirty    Dirty
	cloned   map[string]bool
}

// own clones a collection before its first write in this transaction.
func (t *tx) own(name string) error {
	if t.readOnly {
		return errReadOnly
	}
	if t.cloned[name] {
		return nil
	}
	t.cloned[name] = true
	switch name {
	case "peers":
		t.s.Peers, t.s.byKey = maps.Clone(t.s.Peers), maps.Clone(t.s.byKey)
	case "codes":
		t.s.Codes = maps.Clone(t.s.Codes)
	case "forgotten":
		t.s.Forgotten = maps.Clone(t.s.Forgotten)
	case "libraryIds":
		t.s.LibraryIDs = maps.Clone(t.s.LibraryIDs)
	case "leftovers":
		t.s.Leftovers = maps.Clone(t.s.Leftovers)
	case "catalogs":
		t.s.Catalogs = maps.Clone(t.s.Catalogs)
	case "own":
		o := *t.s.Own
		o.Snapshot, o.Changes = maps.Clone(o.Snapshot), slices.Clone(o.Changes)
		t.s.Own = &o
	}
	if name == "own" {
		t.dirty.Own = true
	} else if name != "catalogs" {
		t.dirty.Records = true
	}
	return nil
}

// catalog is a peer's catalog, cloned for writing (made when absent).
func (t *tx) catalog(peer string) (*Catalog, error) {
	if err := t.own("catalogs"); err != nil {
		return nil, err
	}
	t.dirty.Catalogs[peer] = true
	if t.cloned["catalog "+peer] {
		return t.s.Catalogs[peer], nil
	}
	t.cloned["catalog "+peer] = true
	c := &Catalog{Titles: map[string]Stored{}, Sent: map[string]bool{}}
	if old := t.s.Catalogs[peer]; old != nil {
		c.Titles, c.Sent = maps.Clone(old.Titles), maps.Clone(old.Sent)
	}
	t.s.Catalogs[peer] = c
	return c, nil
}

func get[T any](raw json.RawMessage) (*T, error) {
	if raw == nil {
		return nil, nil
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func (t *tx) Server() (*msfp.ServerRecord, error) { return get[msfp.ServerRecord](t.s.Server) }

func (t *tx) PutServer(r msfp.ServerRecord) error {
	if t.readOnly {
		return errReadOnly
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	t.s.Server, t.dirty.Records = b, true
	return nil
}

func (t *tx) Peers() ([]msfp.PeerRecord, error) {
	out := make([]msfp.PeerRecord, 0, len(t.s.Peers))
	for _, id := range slices.Sorted(maps.Keys(t.s.Peers)) {
		p, err := get[msfp.PeerRecord](t.s.Peers[id])
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, nil
}

func (t *tx) Peer(id string) (*msfp.PeerRecord, error) { return get[msfp.PeerRecord](t.s.Peers[id]) }

func (t *tx) PeerByKey(key []byte) (*msfp.PeerRecord, error) {
	id, ok := t.s.byKey[hex.EncodeToString(key)]
	if !ok {
		return nil, nil
	}
	return t.Peer(id)
}

func (t *tx) PutPeer(p msfp.PeerRecord) error {
	if err := t.own("peers"); err != nil {
		return err
	}
	if old, err := t.Peer(p.ID); err != nil {
		return err
	} else if old != nil {
		delete(t.s.byKey, hex.EncodeToString(old.Key))
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	t.s.Peers[p.ID] = b
	t.s.byKey[hex.EncodeToString(p.Key)] = p.ID
	return nil
}

func (t *tx) DeletePeer(id string) error {
	if err := t.own("peers"); err != nil {
		return err
	}
	if old, err := t.Peer(id); err != nil {
		return err
	} else if old != nil {
		delete(t.s.byKey, hex.EncodeToString(old.Key))
	}
	delete(t.s.Peers, id)
	if _, ok := t.s.Catalogs[id]; ok {
		if err := t.own("catalogs"); err != nil {
			return err
		}
		delete(t.s.Catalogs, id)
		t.dirty.Catalogs[id] = true
	}
	if err := t.own("leftovers"); err != nil {
		return err
	}
	maps.DeleteFunc(t.s.Leftovers, func(_ string, l msfp.Leftover) bool { return l.Peer == id })
	return nil
}

func (t *tx) Codes() ([]msfp.CodeRecord, error) {
	out := make([]msfp.CodeRecord, 0, len(t.s.Codes))
	for _, id := range slices.Sorted(maps.Keys(t.s.Codes)) {
		c, err := get[msfp.CodeRecord](t.s.Codes[id])
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, nil
}

func (t *tx) CodeByHash(hash []byte) (*msfp.CodeRecord, error) {
	codes, err := t.Codes()
	for _, c := range codes {
		if string(c.SecretHash) == string(hash) {
			return &c, nil
		}
	}
	return nil, err
}

func (t *tx) PutCode(c msfp.CodeRecord) error {
	if err := t.own("codes"); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	t.s.Codes[c.ID] = b
	return nil
}

func (t *tx) DeleteCode(id string) error {
	if err := t.own("codes"); err != nil {
		return err
	}
	delete(t.s.Codes, id)
	return nil
}

func (t *tx) Forgotten(hash []byte) (*msfp.ForgottenKey, error) {
	return get[msfp.ForgottenKey](t.s.Forgotten[hex.EncodeToString(hash)])
}

func (t *tx) Forget(f msfp.ForgottenKey) error {
	if err := t.own("forgotten"); err != nil {
		return err
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	t.s.Forgotten[hex.EncodeToString(f.KeyHash)] = b
	return nil
}

func (t *tx) Unforget(hash []byte) error {
	if _, ok := t.s.Forgotten[hex.EncodeToString(hash)]; !ok {
		return nil
	}
	if err := t.own("forgotten"); err != nil {
		return err
	}
	delete(t.s.Forgotten, hex.EncodeToString(hash))
	return nil
}

func (t *tx) TrimForgotten(before time.Time) error {
	var old []string
	for h, raw := range t.s.Forgotten {
		f, err := get[msfp.ForgottenKey](raw)
		if err != nil {
			return err
		}
		if f.At.Before(before) {
			old = append(old, h)
		}
	}
	if len(old) == 0 {
		return nil
	}
	if err := t.own("forgotten"); err != nil {
		return err
	}
	for _, h := range old {
		delete(t.s.Forgotten, h)
	}
	return nil
}

func (t *tx) LibraryIDs() (map[string]string, error) { return maps.Clone(t.s.LibraryIDs), nil }

func (t *tx) PutLibraryID(hostID, publicID string) error {
	if err := t.own("libraryIds"); err != nil {
		return err
	}
	t.s.LibraryIDs[hostID] = publicID
	return nil
}

func (t *tx) Sent(peer string, keys []string) ([]string, error) {
	c := t.s.Catalogs[peer]
	if c == nil {
		return nil, nil
	}
	var out []string
	for _, k := range keys {
		if c.Sent[k] {
			out = append(out, k)
		}
	}
	return out, nil
}

func (t *tx) MarkSent(peer string, keys []string) error {
	c, err := t.catalog(peer)
	if err != nil {
		return err
	}
	for _, k := range keys {
		c.Sent[k] = true
	}
	return nil
}

func leftoverKey(l msfp.Leftover) string { return l.Peer + "\n" + l.PlaybackID }

func (t *tx) Leftovers() ([]msfp.Leftover, error) {
	out := slices.Collect(maps.Values(t.s.Leftovers))
	slices.SortFunc(out, func(a, b msfp.Leftover) int { return strings.Compare(leftoverKey(a), leftoverKey(b)) })
	return out, nil
}

func (t *tx) PutLeftover(l msfp.Leftover) error {
	if err := t.own("leftovers"); err != nil {
		return err
	}
	t.s.Leftovers[leftoverKey(l)] = l
	return nil
}

func (t *tx) DeleteLeftover(l msfp.Leftover) error {
	if err := t.own("leftovers"); err != nil {
		return err
	}
	delete(t.s.Leftovers, leftoverKey(l))
	return nil
}

func (t *tx) PutPeerTitles(peer string, reading int64, titles []msfp.Title) error {
	c, err := t.catalog(peer)
	if err != nil {
		return err
	}
	for _, title := range titles {
		b, err := msfp.MarshalTitle(title)
		if err != nil {
			return err
		}
		c.Titles[title.Common().Key] = Stored{Reading: reading, Title: b}
	}
	return nil
}

func (t *tx) EndReading(peer string, reading int64) error {
	c, err := t.catalog(peer)
	if err != nil {
		return err
	}
	maps.DeleteFunc(c.Titles, func(_ string, s Stored) bool { return s.Reading != reading })
	return nil
}

func (t *tx) DropPeerTitles(peer string, keys []string) error {
	c, err := t.catalog(peer)
	if err != nil {
		return err
	}
	for _, k := range keys {
		delete(c.Titles, k)
	}
	return nil
}

func (t *tx) PeerTitles(peer, after string, limit int) ([]msfp.Title, error) {
	c := t.s.Catalogs[peer]
	if c == nil {
		return nil, nil
	}
	var out []msfp.Title
	for _, k := range slices.Sorted(maps.Keys(c.Titles)) {
		if k <= after {
			continue
		}
		if len(out) == limit {
			break
		}
		title, err := msfp.UnmarshalTitle(c.Titles[k].Title)
		if err != nil {
			return nil, err
		}
		out = append(out, title)
	}
	return out, nil
}

func (t *tx) PeerTitle(peer, key string) (msfp.Title, error) {
	c := t.s.Catalogs[peer]
	if c == nil {
		return nil, nil
	}
	s, ok := c.Titles[key]
	if !ok {
		return nil, nil
	}
	return msfp.UnmarshalTitle(s.Title)
}

// The own catalog's log (msfp.OwnLogTx).

func (t *tx) OwnSnapshot() (map[string]msfp.Title, error) {
	out := map[string]msfp.Title{}
	for k, raw := range t.s.Own.Snapshot {
		title, err := msfp.UnmarshalTitle(raw)
		if err != nil {
			return nil, err
		}
		out[k] = title
	}
	return out, nil
}

func (t *tx) PutOwnSnapshot(m map[string]msfp.Title) error {
	if err := t.own("own"); err != nil {
		return err
	}
	snap := make(map[string]json.RawMessage, len(m))
	for k, title := range m {
		b, err := msfp.MarshalTitle(title)
		if err != nil {
			return err
		}
		snap[k] = b
	}
	t.s.Own.Snapshot = snap
	return nil
}

func (t *tx) OwnChanges(since int64, after string, limit int) ([]msfp.OwnChange, error) {
	latest := map[string]msfp.OwnChange{}
	for _, c := range t.s.Own.Changes {
		if c.Seq > since && c.Key > after {
			if l, ok := latest[c.Key]; !ok || c.Seq > l.Seq {
				latest[c.Key] = c
			}
		}
	}
	out := slices.SortedFunc(maps.Values(latest), func(a, b msfp.OwnChange) int { return cmp.Compare(a.Key, b.Key) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (t *tx) AppendOwnChanges(changes []msfp.OwnChange) error {
	if err := t.own("own"); err != nil {
		return err
	}
	t.s.Own.Changes = append(t.s.Own.Changes, changes...)
	return nil
}

func (t *tx) TrimOwnChanges(before time.Time) error {
	if err := t.own("own"); err != nil {
		return err
	}
	kept := t.s.Own.Changes[:0]
	for _, c := range t.s.Own.Changes {
		if c.At.Before(before) {
			t.s.Own.Floor = max(t.s.Own.Floor, c.Seq)
			continue
		}
		kept = append(kept, c)
	}
	t.s.Own.Changes = kept
	return nil
}

func (t *tx) OwnFloor() (int64, error) { return t.s.Own.Floor, nil }

// The checks that the stores are what the engine needs.
var (
	_ msfp.Tx       = (*tx)(nil)
	_ msfp.OwnLogTx = (*tx)(nil)
)
