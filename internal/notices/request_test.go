package notices

import (
	"context"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// A server asking to pair or to join a group is emailed to the admins (on
// by default, the one notice that is), only to admins, never a disabled
// one; with "quiet requests" on, to nobody.
func TestRequestNotice(t *testing.T) {
	ctx := context.Background()
	n, db, m, _, _ := setup(t)
	db.CreateUser(ctx, "admin@example.org", true)
	db.CreateUser(ctx, "person@example.org", false)
	gone, _ := db.CreateUser(ctx, "gone@example.org", true)
	db.SetUserDisabled(ctx, gone.Subject, true)
	wait := func(want int) []sent {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for len(m.all()) < want && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond) // nothing more comes
		return m.all()
	}
	n.Request("pair", "Server X", "")
	if got := wait(1); len(got) != 1 || got[0].to != "admin@example.org" || got[0].subject == "" {
		t.Fatalf("a pairing request: %+v", got)
	}
	n.Request("join", "Server Y", "Family")
	if got := wait(2); len(got) != 2 || got[1].to != "admin@example.org" {
		t.Errorf("a group request: %+v", got)
	}
	db.SetNoticeSettings(ctx, store.NoticeSettings{QuietRequests: true})
	n.Request("pair", "Server Z", "")
	time.Sleep(300 * time.Millisecond)
	if got := m.all(); len(got) != 2 {
		t.Errorf("quiet requests: %+v", got)
	}
}
