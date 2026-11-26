package store

import (
	"context"
	"testing"
	"time"
)

// The hourly trims, each at its boundary: revocations past their logins'
// time; email links past their expiry (those still good kept, and still
// taken); title changes noted before a time (a peer then reads whole);
// wish arrivals older than a year. What's newer stays.
func TestHousekeepingTrims(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	count := func(table string) int {
		t.Helper()
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Revocations: one ending now (gone), one later (kept, and still said).
	for _, at := range []time.Time{now.Add(-time.Hour), now, now.Add(time.Hour)} {
		s.db.ExecContext(ctx, `INSERT INTO revoked_logins (token_hash, expires_at) VALUES (x'01', ?)`, at.Unix())
	}
	if n := count("revoked_logins"); n != 3 {
		t.Fatalf("revocations before: %d", n)
	}
	if err := s.TrimRevokedLogins(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count("revoked_logins"); n != 1 {
		t.Errorf("revocations kept: %d", n)
	}
	if seq, _ := s.LastRevocation(ctx); seq != 3 {
		t.Errorf("the last revocation's number after the trim: %d, want 3", seq)
	}

	// Email links: one expired, one good.
	u, _ := s.CreateUser(ctx, "a@example.org", false)
	s.AddEmailLink(ctx, EmailLink{Subject: u.Subject, LinkHash: []byte("old"), Purpose: "verify", Expires: now.Add(-time.Minute)})
	s.AddSetupLink(ctx, EmailLink{Subject: u.Subject, LinkHash: []byte("new"), Purpose: "setup", Expires: now.Add(time.Hour)})
	if n := count("email_links"); n != 2 {
		t.Fatalf("links before the trim: %d", n)
	}
	if err := s.DeleteOldEmailLinks(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count("email_links"); n != 1 {
		t.Errorf("links kept: %d", n)
	}
	if subject, err := s.PeekEmailLink(ctx, "setup", []byte("new")); err != nil || subject != u.Subject {
		t.Errorf("the good link: %q %v", subject, err)
	}

	// Title changes before a time.
	s.db.ExecContext(ctx, `DELETE FROM title_changes`)
	for _, at := range []time.Time{now.AddDate(0, 0, -40), now.AddDate(0, 0, -30), now.AddDate(0, 0, -1)} {
		s.db.ExecContext(ctx, `INSERT INTO title_changes (title_key, at) VALUES ('movie/tmdb/1', ?)`, at.Unix())
	}
	head, _, _ := s.TitleChanges(ctx)
	if err := s.TrimTitleChanges(ctx, now.AddDate(0, 0, -30)); err != nil {
		t.Fatal(err)
	}
	if n := count("title_changes"); n != 2 {
		t.Errorf("changes kept: %d", n)
	}
	if h, floor, _ := s.TitleChanges(ctx); h != head || floor != head-2 {
		t.Errorf("after the trim: head %d floor %d, want %d %d", h, floor, head, head-2)
	}

	// Wish arrivals older than a year.
	_, p, _ := s.CreateAccount(ctx, u.Subject, NewOwner{Name: "A"})
	for i, at := range []time.Time{now.AddDate(-2, 0, 0), now.AddDate(0, -6, 0)} {
		s.db.ExecContext(ctx, `INSERT INTO wish_arrivals (profile_id, title, arrived_at) VALUES (?, ?, ?)`, p.ID, "movie/tmdb/"+string(rune('1'+i)), at.Unix())
	}
	if n := count("wish_arrivals"); n != 2 {
		t.Fatalf("arrivals before: %d", n)
	}
	if err := s.ForgetArrivals(ctx, now.AddDate(-1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if n := count("wish_arrivals"); n != 1 {
		t.Errorf("arrivals kept: %d", n)
	}
}

// The transcoding chosen and the previews switch, as saved: none and off
// before; each saved again replaces the last.
func TestTranscodingAndPreviewsSettings(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if f, d, err := s.Transcoding(ctx); err != nil || f != "" || d != "" {
		t.Errorf("before: %q %q %v", f, d, err)
	}
	if on, err := s.Previews(ctx); err != nil || on {
		t.Errorf("previews before: %v %v", on, err)
	}
	s.SetTranscoding(ctx, "vaapi", "/dev/dri/renderD128")
	s.SetTranscoding(ctx, "qsv", "/dev/dri/renderD129")
	if f, d, _ := s.Transcoding(ctx); f != "qsv" || d != "/dev/dri/renderD129" {
		t.Errorf("transcoding: %q %q", f, d)
	}
	s.SetTranscoding(ctx, "none", "")
	if f, d, _ := s.Transcoding(ctx); f != "none" || d != "" {
		t.Errorf("none: %q %q", f, d)
	}
	s.SetPreviews(ctx, true)
	if on, _ := s.Previews(ctx); !on {
		t.Error("previews on: off")
	}
	s.SetPreviews(ctx, false)
	if on, _ := s.Previews(ctx); on {
		t.Error("previews off: on")
	}
}

// Of two pictures, the taller is better; of the same height, the one with
// more HDR formats; the best of several stepped first.
func TestBetterPicture(t *testing.T) {
	for _, c := range []struct {
		p, q Picture
		want bool
	}{
		{Picture{Height: 2160}, Picture{Height: 1080, HDR: []string{"HDR10", "DV"}}, true},
		{Picture{Height: 1080, HDR: []string{"HDR10"}}, Picture{Height: 1080}, true},
		{Picture{Height: 1080}, Picture{Height: 1080}, false},
		{Picture{Height: 720, HDR: []string{"HDR10"}}, Picture{Height: 1080}, false},
	} {
		if got := c.p.Better(c.q); got != c.want {
			t.Errorf("%+v better than %+v: %v", c.p, c.q, got)
		}
	}
}
