package store

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go"
)

// Groups are kept as given, and so are a code's uses and group.
func TestMSFPGroups(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, ok := any(&msfpTx{}).(msfp.GroupsTx); !ok {
		t.Fatal("the store's Tx doesn't keep groups")
	}
	g := msfp.GroupRecord{ID: "g1", Me: "me", Offers: []string{"lib"}, MailKey: []byte{1, 2},
		Pool: msfp.Limits{Streams: 3}, Events: []msfp.StoredEvent{{Event: "e", Signature: "s"}}}
	code := msfp.CodeRecord{ID: "c1", Secret: []byte("sealed"), SecretHash: bytes.Repeat([]byte{7}, 32),
		Created: time.Unix(1, 5), Expires: time.Unix(99, 7), Uses: 4, Group: "g1"}
	err := s.MSFP().Update(ctx, func(tx msfp.Tx) error {
		gt := tx.(msfp.GroupsTx)
		if err := gt.PutGroup(g); err != nil {
			return err
		}
		return tx.PutCode(code)
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.MSFP().Read(ctx, func(tx msfp.Tx) error {
		gt := tx.(msfp.GroupsTx)
		all, err := gt.Groups()
		if err != nil || len(all) != 1 || all[0].Me != "me" || all[0].Pool.Streams != 3 || len(all[0].Events) != 1 {
			t.Errorf("groups: %+v, %v", all, err)
		}
		if none, err := gt.Group("nope"); none != nil || err != nil {
			t.Errorf("an absent group: %+v, %v", none, err)
		}
		c, err := tx.CodeByHash(code.SecretHash)
		if err != nil || c == nil || c.Uses != 4 || c.Group != "g1" || !c.Expires.Equal(code.Expires) {
			t.Errorf("code: %+v, %v", c, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MSFP().Update(ctx, func(tx msfp.Tx) error { return tx.(msfp.GroupsTx).DeleteGroup("g1") }); err != nil {
		t.Fatal(err)
	}
	_ = s.MSFP().Read(ctx, func(tx msfp.Tx) error {
		if all, _ := tx.(msfp.GroupsTx).Groups(); len(all) != 0 {
			t.Errorf("deleted, still: %+v", all)
		}
		return nil
	})
}

// A renamed library's new ID follows it in what groups are offered, and
// a gone library that had the new ID is no longer offered.
func TestReplaceLibraryIDInGroups(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	oldID, newID, other := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	peer, err := s.AddPeer(ctx, bytes.Repeat([]byte{9}, 32), "https://peer.example.org", "Peer", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.changePeer(ctx, peer.ID, func(p *msfp.PeerRecord) { p.GroupOffers = []string{newID, oldID} }); err != nil {
		t.Fatal(err)
	}
	err = s.MSFP().Update(ctx, func(tx msfp.Tx) error {
		return tx.(msfp.GroupsTx).PutGroup(msfp.GroupRecord{ID: "g1", Offers: []string{oldID, other}})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceLibraryID(ctx, oldID, newID); err != nil {
		t.Fatal(err)
	}
	_ = s.MSFP().Read(ctx, func(tx msfp.Tx) error {
		g, _ := tx.(msfp.GroupsTx).Group("g1")
		if g == nil || !slices.Equal(g.Offers, []string{newID, other}) {
			t.Errorf("the group's offers: %+v", g)
		}
		p, _ := tx.Peer(peer.ID)
		if p == nil || !slices.Equal(p.GroupOffers, []string{newID}) {
			t.Errorf("the link's group offers: %+v", p)
		}
		return nil
	})
}
