package overfin

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
