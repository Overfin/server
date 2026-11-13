package store

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestRevokedLogins(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	l, token, err := s.CreateLogin(ctx, "subject-1", nil, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := s.RevokedLogins(ctx, 0, 10); len(r) != 0 {
		t.Fatalf("before: %v", r)
	}
	if err := s.DeleteLogin(ctx, l.ID); err != nil {
		t.Fatal(err)
	}
	r, err := s.RevokedLogins(ctx, 0, 10)
	if err != nil || len(r) != 1 || !bytes.Equal(r[0].Hash, hashToken(token)) {
		t.Fatalf("after signing out: %v %v", r, err)
	}
	if more, _ := s.RevokedLogins(ctx, r[0].Seq, 10); len(more) != 0 {
		t.Errorf("since its number: %v", more)
	}
	// Every login of someone (a new password, an account removed).
	s.CreateLogin(ctx, "subject-2", nil, "", time.Hour)
	s.CreateLogin(ctx, "subject-2", nil, "", time.Hour)
	s.DeleteLogins(ctx, "subject-2")
	if all, _ := s.RevokedLogins(ctx, 0, 10); len(all) != 3 {
		t.Errorf("after removing two more: %d", len(all))
	}
}
