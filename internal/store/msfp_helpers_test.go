package store

import (
	"context"
	"slices"
	"sync/atomic"
	"time"

	"github.com/overfin/server/msfp"
)

// Peers as the engine writes them (store/msfp.go), for the tests of what
// Overfin's pages read of them.

type testPeer struct{ ID string }

// PeerTitle is a paired server's title, as these tests write it.
type PeerTitle struct {
	Key, Kind, Library     string
	Audio, Subtitles, With []string
}

// PeerLibrary is one of a paired server's libraries we want.
type PeerLibrary struct{ ID, Name, Kind string }

const (
	PeerTheirs = "theirs"
	PeerPaired = "paired"
)

var testPeers atomic.Int64

// AddPeer adds a peer waiting for the other side's operator.
func (s *Store) AddPeer(ctx context.Context, key []byte, address, name string, _ []byte) (*testPeer, error) {
	id := "p" + string(rune('a'+testPeers.Add(1)%26)) + time.Now().Format("150405.000000")
	p := msfp.PeerRecord{ID: id, Key: key, Address: address, Name: name, State: msfp.Theirs, Inbound: msfp.InboundYes,
		Created: s.now().UTC(), Limits: msfp.Limits{Streams: 2, Transcodes: 1}}
	return &testPeer{ID: id}, s.MSFP().Update(ctx, func(tx msfp.Tx) error { return tx.PutPeer(p) })
}

// changePeer changes a peer's record.
func (s *Store) changePeer(ctx context.Context, id string, fn func(*msfp.PeerRecord)) error {
	return s.MSFP().Update(ctx, func(tx msfp.Tx) error {
		p, err := tx.Peer(id)
		if err != nil || p == nil {
			return err
		}
		fn(p)
		return tx.PutPeer(*p)
	})
}

func (s *Store) SetPeerState(ctx context.Context, id, _, to string) (bool, error) {
	return true, s.changePeer(ctx, id, func(p *msfp.PeerRecord) {
		p.State = msfp.PeerState(to)
		if to == PeerPaired {
			p.Paired = s.now().UTC().Truncate(time.Second)
		}
	})
}

func (s *Store) SetPeerOffers(ctx context.Context, id string, libs []string) error {
	return s.changePeer(ctx, id, func(p *msfp.PeerRecord) {
		p.Offers = slices.Compact(slices.Sorted(slices.Values(libs)))
	})
}

func (s *Store) PeerOffers(ctx context.Context, id string) ([]string, error) {
	var out []string
	err := s.MSFP().Read(ctx, func(tx msfp.Tx) error {
		p, err := tx.Peer(id)
		if p != nil {
			out = p.Offers
		}
		return err
	})
	return out, err
}

func (s *Store) SetPeerWants(ctx context.Context, id string, libs []PeerLibrary) error {
	return s.changePeer(ctx, id, func(p *msfp.PeerRecord) {
		p.Wants = nil
		for _, l := range libs {
			p.Wants = append(p.Wants, msfp.WantedLibrary{ID: l.ID, Name: l.Name, Kind: l.Kind, Wanted: true})
		}
	})
}

var lastReading atomic.Int64

// PeerReading numbers a reading, above every one before.
func PeerReading() int64 { return lastReading.Add(1) }

// PutPeerTitles keeps a page of a reading; the libraries named are
// wanted (a peer's titles are those of libraries wanted).
func (s *Store) PutPeerTitles(ctx context.Context, id string, reading int64, titles []PeerTitle) error {
	return s.MSFP().Update(ctx, func(tx msfp.Tx) error {
		p, err := tx.Peer(id)
		if err != nil {
			return err
		}
		var page []msfp.Title
		for _, t := range titles {
			if !slices.ContainsFunc(p.Wants, func(w msfp.WantedLibrary) bool { return w.ID == t.Library }) {
				p.Wants = append(p.Wants, msfp.WantedLibrary{ID: t.Library, Name: t.Library, Kind: "movies", Wanted: true})
			}
			page = append(page, &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: t.Key, Kind: t.Kind, Libraries: []string{t.Library}},
				Audio: t.Audio, Subtitles: t.Subtitles, With: t.With})
		}
		if err := tx.PutPeer(*p); err != nil {
			return err
		}
		return tx.PutPeerTitles(id, reading, page)
	})
}

func (s *Store) EndPeerTitles(ctx context.Context, id string, reading int64) error {
	return s.MSFP().Update(ctx, func(tx msfp.Tx) error { return tx.EndReading(id, reading) })
}

// SetPeerTitles replaces a peer's titles whole.
func (s *Store) SetPeerTitles(ctx context.Context, id string, titles []PeerTitle) error {
	reading := PeerReading()
	if err := s.PutPeerTitles(ctx, id, reading, titles); err != nil {
		return err
	}
	return s.EndPeerTitles(ctx, id, reading)
}

func (s *Store) DeletePeer(ctx context.Context, id string) error {
	return s.MSFP().Update(ctx, func(tx msfp.Tx) error { return tx.DeletePeer(id) })
}

// PublicLibraryIDs gives libraries their public IDs, made the first time,
// as the engine does when it first offers one.
func (s *Store) PublicLibraryIDs(ctx context.Context, hostIDs []string) (map[string]string, error) {
	out := map[string]string{}
	err := s.MSFP().Update(ctx, func(tx msfp.Tx) error {
		ids, err := tx.LibraryIDs()
		if err != nil {
			return err
		}
		for _, id := range hostIDs {
			if ids[id] == "" {
				ids[id] = "lib_" + id + "_" + time.Now().Format("150405.000000000")
				if err := tx.PutLibraryID(id, ids[id]); err != nil {
					return err
				}
			}
			out[id] = ids[id]
		}
		return nil
	})
	return out, err
}
