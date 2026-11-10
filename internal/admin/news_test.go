package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// The panel hears of accounts made since it asked: first only the time
// to start from (nothing from before it was opened), then each account,
// with its invitation's name when it came from one.
func TestNews(t *testing.T) {
	ctx := context.Background()
	p, b, db, _ := signedInPanel(t)
	var got newsJSON
	rec := b.do("GET", "/admin/api/news", "", nil)
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Now == 0 || len(got.Accounts) != 0 {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	since := got.Now
	a1, _, _ := db.CreateAccount(ctx, "person-1", store.NewOwner{Name: "A", JellyfinUserID: strings.Repeat("a", 32), JellyfinUsername: "of-1", JellyfinToken: "t"})
	a2, _, _ := db.CreateAccount(ctx, "person-2", store.NewOwner{Name: "B", JellyfinUserID: strings.Repeat("b", 32), JellyfinUsername: "of-2", JellyfinToken: "t"})
	db.SetAccountLabel(ctx, a2.ID, "Bob")
	rec = b.do("GET", "/admin/api/news?since="+strconv.FormatInt(since, 10), "", nil)
	got = newsJSON{}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || len(got.Accounts) != 2 ||
		got.Accounts[0].ID+got.Accounts[1].ID != a1.ID+a2.ID && got.Accounts[0].ID+got.Accounts[1].ID != a2.ID+a1.ID {
		t.Fatalf("since: %d %s", rec.Code, rec.Body)
	}
	for _, a := range got.Accounts {
		if (a.ID == a2.ID) != (a.Invitation == "Bob") {
			t.Errorf("invitation: %+v", a)
		}
	}
	for _, bad := range []string{"x", "-1", strconv.FormatInt(p.clock().Unix()+3600, 10)} {
		if rec := b.do("GET", "/admin/api/news?since="+bad, "", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("since=%s: %d", bad, rec.Code)
		}
	}
}

// Asking in the background doesn't keep a forgotten panel signed in: the
// session still ends after its idle time.
func TestBackgroundKeepsNoSession(t *testing.T) {
	p, b, _, _ := signedInPanel(t)
	var mu sync.Mutex
	now := time.Now()
	p.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	step := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	bg := map[string]string{backgroundHeader: "1"}
	for i := 0; i < 4; i++ {
		step(10 * time.Minute)
		if rec := b.do("GET", "/admin/api/news", "", bg); i < 2 && rec.Code != 200 {
			t.Fatalf("background ask %d: %d", i, rec.Code)
		}
	}
	if rec := b.do("GET", "/admin/api/news", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("after 40 minutes of background asks only: %d", rec.Code)
	}
}
