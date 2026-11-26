package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go"
)

// The engine's download records here, as msfp-go's own stores keep them
// (store/jsonfile's TestDownloadsKept): the same ID kept apart for each
// side; a record replaced whole; one deleted by its side and ID; a
// peer's go with it; a failed transaction keeps none of its writes.
func TestMSFPDownloads(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	ms := st.MSFP()
	played := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	err := ms.Update(ctx, func(tx msfp.Tx) error {
		d := tx.(msfp.DownloadsTx)
		if err := tx.PutPeer(msfp.PeerRecord{ID: "p1", Key: []byte("k1"), State: msfp.Paired}); err != nil {
			return err
		}
		if err := tx.PutPeer(msfp.PeerRecord{ID: "p2", Key: []byte("k2"), State: msfp.Paired}); err != nil {
			return err
		}
		for _, r := range []msfp.DownloadRecord{
			{ID: "play_a", Peer: "p1", Title: "movie/tmdb/1", Handle: "h", PlayedAt: played},
			{ID: "play_a", Viewer: true, Peer: "p1", TheirID: "x", Title: "movie/tmdb/2", PlayedAt: played},
			{ID: "play_b", Peer: "p2", Title: "movie/tmdb/3", PlayedAt: played},
		} {
			if err := d.PutDownload(r); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	read := func() []msfp.DownloadRecord {
		t.Helper()
		var got []msfp.DownloadRecord
		ms.Read(ctx, func(tx msfp.Tx) error {
			var err error
			got, err = tx.(msfp.DownloadsTx).Downloads()
			return err
		})
		return got
	}
	if got := read(); len(got) != 3 || !got[0].PlayedAt.Equal(played) {
		t.Fatalf("downloads: %+v", got)
	}
	// Replaced whole: the owner's side of play_a, its handle and last read.
	last := played.Add(time.Hour)
	ms.Update(ctx, func(tx msfp.Tx) error {
		return tx.(msfp.DownloadsTx).PutDownload(msfp.DownloadRecord{ID: "play_a", Peer: "p1", Title: "movie/tmdb/1", Handle: "h2", PlayedAt: played, LastAt: last})
	})
	for _, r := range read() {
		if r.ID == "play_a" && !r.Viewer && (r.Handle != "h2" || !r.LastAt.Equal(last)) {
			t.Errorf("replaced: %+v", r)
		}
		if r.ID == "play_a" && r.Viewer && r.TheirID != "x" {
			t.Errorf("the other side changed: %+v", r)
		}
	}
	// A failed transaction keeps nothing.
	ms.Update(ctx, func(tx msfp.Tx) error {
		tx.(msfp.DownloadsTx).DeleteDownload(false, "play_b")
		return errors.New("no")
	})
	if got := read(); len(got) != 3 {
		t.Errorf("after a failed transaction: %+v", got)
	}
	// One deleted by its side; a peer's go with it.
	err = ms.Update(ctx, func(tx msfp.Tx) error {
		if err := tx.(msfp.DownloadsTx).DeleteDownload(true, "play_a"); err != nil {
			return err
		}
		if err := tx.(msfp.DownloadsTx).DeleteDownload(true, "nothing"); err != nil {
			return err
		}
		return tx.DeletePeer("p2")
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := read(); len(got) != 1 || got[0].ID != "play_a" || got[0].Viewer {
		t.Errorf("after a deletion and a peer's: %+v", got)
	}
}
