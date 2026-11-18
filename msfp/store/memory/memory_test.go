package memory_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/overfin/server/msfp"
	"github.com/overfin/server/msfp/store/memory"
)

// A transaction that fails keeps nothing; one that succeeds keeps all;
// what a getter returns is a copy; a read can't write.
func TestTransactions(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	p := msfp.PeerRecord{ID: "p1", Key: []byte("key one"), Name: "one", Pins: []string{"x"}}
	boom := errors.New("boom")
	err := st.Update(ctx, func(tx msfp.Tx) error {
		if err := tx.PutPeer(p); err != nil {
			return err
		}
		if err := tx.PutCode(msfp.CodeRecord{ID: "c1", SecretHash: []byte("h")}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	st.Read(ctx, func(tx msfp.Tx) error {
		if peers, _ := tx.Peers(); len(peers) != 0 {
			t.Errorf("kept %+v", peers)
		}
		if c, _ := tx.CodeByHash([]byte("h")); c != nil {
			t.Error("kept the code")
		}
		return nil
	})

	if err := st.Update(ctx, func(tx msfp.Tx) error { return tx.PutPeer(p) }); err != nil {
		t.Fatal(err)
	}
	st.Update(ctx, func(tx msfp.Tx) error {
		got, _ := tx.PeerByKey([]byte("key one"))
		got.Pins[0] = "changed"
		got.Name = "changed"
		return nil
	})
	st.Read(ctx, func(tx msfp.Tx) error {
		got, _ := tx.Peer("p1")
		if got.Name != "one" || got.Pins[0] != "x" {
			t.Errorf("a getter's copy changed the store: %+v", got)
		}
		if err := tx.PutPeer(p); err == nil {
			t.Error("a read wrote")
		}
		return nil
	})

	// A new key: found by it, no longer by the old one.
	p.Key = []byte("key two")
	st.Update(ctx, func(tx msfp.Tx) error { return tx.PutPeer(p) })
	st.Read(ctx, func(tx msfp.Tx) error {
		if old, _ := tx.PeerByKey([]byte("key one")); old != nil {
			t.Error("found by its old key")
		}
		if now, _ := tx.PeerByKey([]byte("key two")); now == nil {
			t.Error("not found by its new key")
		}
		return nil
	})
}

// A peer's titles: put, paged in key order, ended by a reading, dropped
// with the peer.
func TestPeerTitles(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	title := func(key string) msfp.Title {
		return &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: key, Kind: "movie", Libraries: []string{"L"}}, Audio: []string{"fr"}}
	}
	st.Update(ctx, func(tx msfp.Tx) error {
		tx.PutPeer(msfp.PeerRecord{ID: "p1", Key: []byte("k")})
		tx.PutPeerTitles("p1", 1, []msfp.Title{title("movie/tmdb/3"), title("movie/tmdb/1"), title("movie/tmdb/2")})
		tx.PutPeerTitles("p1", 2, []msfp.Title{title("movie/tmdb/2")})
		return tx.MarkSent("p1", []string{"movie/tmdb/9"})
	})
	st.Read(ctx, func(tx msfp.Tx) error {
		page, _ := tx.PeerTitles("p1", "movie/tmdb/1", 1)
		if len(page) != 1 || page[0].Common().Key != "movie/tmdb/2" || page[0].(*msfp.VideoTitle).Audio[0] != "fr" {
			t.Errorf("page: %+v", page)
		}
		if sent, _ := tx.Sent("p1", []string{"movie/tmdb/9", "movie/tmdb/8"}); len(sent) != 1 {
			t.Errorf("sent: %v", sent)
		}
		return nil
	})
	st.Update(ctx, func(tx msfp.Tx) error { return tx.EndReading("p1", 2) })
	st.Read(ctx, func(tx msfp.Tx) error {
		all, _ := tx.PeerTitles("p1", "", 10)
		if len(all) != 1 {
			t.Errorf("after the reading: %+v", all)
		}
		return nil
	})
	st.Update(ctx, func(tx msfp.Tx) error { return tx.DeletePeer("p1") })
	st.Read(ctx, func(tx msfp.Tx) error {
		if all, _ := tx.PeerTitles("p1", "", 10); len(all) != 0 {
			t.Error("titles outlived their peer")
		}
		return nil
	})
}

// Forgotten keys, trimmed by age.
func TestForgotten(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	old, recent := time.Unix(1000, 0).UTC(), time.Unix(5000, 0).UTC()
	st.Update(ctx, func(tx msfp.Tx) error {
		tx.Forget(msfp.ForgottenKey{KeyHash: []byte("a"), At: old})
		return tx.Forget(msfp.ForgottenKey{KeyHash: []byte("b"), At: recent, Revoked: true})
	})
	st.Update(ctx, func(tx msfp.Tx) error { return tx.TrimForgotten(time.Unix(2000, 0)) })
	st.Read(ctx, func(tx msfp.Tx) error {
		if f, _ := tx.Forgotten([]byte("a")); f != nil {
			t.Error("not trimmed")
		}
		if f, _ := tx.Forgotten([]byte("b")); f == nil || !f.Revoked || !bytes.Equal(f.KeyHash, []byte("b")) {
			t.Errorf("trimmed: %+v", f)
		}
		return nil
	})
}
