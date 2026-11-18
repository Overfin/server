package jsonfile

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/overfin/server/msfp"
)

// What a commit writes is there after reopening; a failed commit's
// changes aren't; a catalog whose peer is gone (a crash between two
// writes) is removed; no temporary file is left.
func TestReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	title := &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: "movie/tmdb/1", Kind: "movie"}}
	err = st.Update(ctx, func(tx msfp.Tx) error {
		tx.PutPeer(msfp.PeerRecord{ID: "p1", Key: []byte("k"), Name: "one"})
		return tx.PutPeerTitles("p1", 1, []msfp.Title{title})
	})
	if err != nil {
		t.Fatal(err)
	}
	st.Update(ctx, func(tx msfp.Tx) error {
		tx.PutPeer(msfp.PeerRecord{ID: "p2", Key: []byte("k2")})
		return os.ErrInvalid
	})
	st.Close()
	if err := os.WriteFile(filepath.Join(dir, "peers", "p9.json"), []byte(`{"titles":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.Read(ctx, func(tx msfp.Tx) error {
		peers, _ := tx.Peers()
		if len(peers) != 1 || peers[0].Name != "one" {
			t.Errorf("peers: %+v", peers)
		}
		if got, _ := tx.PeerTitle("p1", "movie/tmdb/1"); got == nil {
			t.Error("its title is lost")
		}
		return nil
	})
	if _, err := os.Stat(filepath.Join(dir, "peers", "p9.json")); !os.IsNotExist(err) {
		t.Error("a lone catalog kept")
	}
	tmp, _ := filepath.Glob(filepath.Join(dir, "*", ".tmp-*"))
	tmp2, _ := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	if len(tmp)+len(tmp2) != 0 {
		t.Errorf("temporary files left: %v %v", tmp, tmp2)
	}
}
