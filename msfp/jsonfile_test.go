package msfp_test

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/overfin/server/msfp"
	"github.com/overfin/server/msfp/store/jsonfile"
)

// An engine on the JSON file store pairs, and after a restart it is the
// same server with the same peer; no seed is in the files in clear, and
// every file is the owner's alone.
func TestPairingJSONFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	const seed = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	random := &script{}
	random.queue(seed)
	st, err := jsonfile.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := newServer(t, msfp.Config{Store: st, Random: random})
	b := newServer(t, msfp.Config{})
	onA, _ := pair(t, a, b)
	before, _ := a.eng.Server(ctx)
	if before.Fingerprint != "KZDV-VJ2U-MNDU-YAUF-35O3-6K6K" {
		t.Errorf("the key isn't the seed's: %s", before.Fingerprint)
	}
	if _, err := jsonfile.Open(dir); err == nil {
		t.Error("opened twice")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = jsonfile.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	again, err := msfp.Open(ctx, msfp.Config{Address: a.srv.URL, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := again.Server(ctx)
	if after.Fingerprint != before.Fingerprint || after.NextKey != before.NextKey {
		t.Errorf("another server: %+v, was %+v", after, before)
	}
	if p := onlyPeer(t, again); p.ID != onA.ID || p.State != msfp.Paired {
		t.Errorf("the peer: %+v", p)
	}

	raw, _ := hex.DecodeString(seed)
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v", path, info.Mode().Perm())
		}
		b, _ := os.ReadFile(path)
		if strings.Contains(string(b), base64.StdEncoding.EncodeToString(raw)) || strings.Contains(string(b), string(raw)) {
			t.Errorf("%s holds the seed in clear", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Another sealing key: the stored keys don't open, and Open says so.
func TestJSONFileOtherKey(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := jsonfile.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := msfp.Open(ctx, msfp.Config{Address: "https://a.example.org", Store: st}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = jsonfile.Open(dir, jsonfile.Options{KeyFile: filepath.Join(t.TempDir(), "other.key")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := msfp.Open(ctx, msfp.Config{Address: "https://a.example.org", Store: st}); !errors.Is(err, msfp.ErrSealed) {
		t.Errorf("opened with another key: %v", err)
	}
}
