package overfin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// A change to what a profile may see reaches the gateway as an access
// event, on the stream already open.
func TestEventsAccess(t *testing.T) {
	ctx := context.Background()
	box, _ := seal.New(bytes.Repeat([]byte{7}, 32))
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	acct, prof, err := db.CreateAccount(ctx, "subject", store.NewOwner{Name: "Me", JellyfinUserID: "jf", JellyfinUsername: "of", JellyfinToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(eventsHandler(db))
	defer srv.Close()
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(sctx, http.MethodGet, srv.URL+"?since=0", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	time.Sleep(1500 * time.Millisecond) // the stream has noted where access stands
	if err := db.SetProfileAccess(ctx, acct.ID, prof.ID, []string{"lib1"}, nil); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.Contains(sc.Text(), `"access"`) {
			return
		}
	}
	t.Error("no access event")
}

// Revocations come numbered, after the one asked for; a quiet stream pings.
func TestEventsRevokedAndPing(t *testing.T) {
	ctx := context.Background()
	box, _ := seal.New(bytes.Repeat([]byte{7}, 32))
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	was := pingAfter
	pingAfter = 200 * time.Millisecond
	t.Cleanup(func() { pingAfter = was })
	var ids []string
	for range 2 {
		l, _, err := db.CreateLogin(ctx, "subject", nil, "", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, l.ID)
	}
	for _, id := range ids {
		if err := db.DeleteLogin(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(eventsHandler(db))
	defer srv.Close()
	read := func(since string) (revoked []string, pinged bool) {
		sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(sctx, http.MethodGet, srv.URL+"?since="+since, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			var e struct {
				Type string `json:"type"`
				Seq  int64  `json:"seq"`
			}
			json.Unmarshal(sc.Bytes(), &e)
			switch e.Type {
			case "revoked":
				revoked = append(revoked, strconv.FormatInt(e.Seq, 10))
			case "ping":
				return revoked, true
			}
		}
		return revoked, false
	}
	all, pinged := read("0")
	if len(all) != 2 || !pinged {
		t.Fatalf("from the start: %v, pinged %v", all, pinged)
	}
	if after, _ := read(all[0]); len(after) != 1 || after[0] != all[1] {
		t.Errorf("after %s: %v", all[0], after)
	}
	if after, pinged := read(all[1]); len(after) != 0 || !pinged {
		t.Errorf("after the last: %v, pinged %v", after, pinged)
	}
}
