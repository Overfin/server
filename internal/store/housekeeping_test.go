package store

import (
	"context"
	"testing"
	"time"
)

// Housekeeping that keeps old credentials from lingering: expired logins
// and sessions deleted (the live ones kept); a session in use pushed
// back; with a lost secret key (ForgetSealed), every PIN and login gone,
// what's left still working.
func TestExpiryAndForgetting(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }

	_, short, err := st.CreateLogin(ctx, "s1", nil, "refresh-a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, long, err := st.CreateLogin(ctx, "s2", nil, "refresh-b", 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	acc, prof, err := st.CreateAccount(ctx, "s1", owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	sess, tok, err := st.CreateSession(ctx, acc.ID, prof.ID, "tv", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, gone, err := st.CreateSession(ctx, acc.ID, prof.ID, "phone", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// Two hours on: the short login and the untouched session have expired;
	// the session in use was pushed back first.
	now = now.Add(50 * time.Minute)
	if err := st.TouchSession(ctx, sess.ID, 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	now = now.Add(70 * time.Minute)
	if n, err := st.DeleteExpiredLogins(ctx); err != nil || n != 1 {
		t.Errorf("expired logins deleted: %d %v", n, err)
	}
	if n, err := st.DeleteExpiredSessions(ctx); err != nil || n != 1 {
		t.Errorf("expired sessions deleted: %d %v", n, err)
	}
	if l, err := st.LoginByToken(ctx, short); err == nil && l != nil {
		t.Error("an expired login still found")
	}
	if l, err := st.LoginByToken(ctx, long); err != nil || l == nil {
		t.Errorf("a live login lost: %v", err)
	}
	if s, err := st.SessionByToken(ctx, tok); err != nil || s == nil {
		t.Errorf("the session in use lost: %v", err)
	}
	if s, err := st.SessionByToken(ctx, gone); err == nil && s != nil {
		t.Error("an expired session still found")
	}

	// The secret key lost: PINs and logins (sealed with it) forgotten.
	if err := st.SetPIN(ctx, acc.ID, prof.ID, []byte("pin-hash")); err != nil {
		t.Fatal(err)
	}
	pins, logins, err := st.ForgetSealed(ctx)
	if err != nil || pins != 1 || logins != 1 {
		t.Errorf("forgotten: %d PINs, %d logins, %v", pins, logins, err)
	}
	if p, err := st.Profile(ctx, acc.ID, prof.ID); err != nil || p.HasPIN {
		t.Errorf("the profile after: %+v %v", p, err)
	}
	if l, _ := st.LoginByToken(ctx, long); l != nil {
		t.Error("a login kept")
	}
	if s, err := st.SessionByToken(ctx, tok); err != nil || s == nil {
		t.Errorf("the session (not sealed) lost: %v", err)
	}
}
