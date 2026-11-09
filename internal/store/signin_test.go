package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Users: one per email, whatever its case; disabling or deleting someone
// ends their logins.
func TestUsers(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	p, err := s.CreateUser(ctx, "Alice@Example.org", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "alice@example.org", false); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("same email: %v", err)
	}
	got, err := s.UserByEmail(ctx, "ALICE@example.org")
	if err != nil || got.Subject != p.Subject || !got.Admin || got.PasswordHash != "" {
		t.Fatalf("by email: %+v, %v", got, err)
	}
	if err := s.SetPassword(ctx, p.Subject, "hash"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.UserBySubject(ctx, p.Subject); got.PasswordHash != "hash" {
		t.Errorf("password: %+v", got)
	}
	if ok, _ := s.HasAdmin(ctx); !ok {
		t.Error("no admin")
	}
	_, token, _ := s.CreateLogin(ctx, p.Subject, nil, "", time.Hour)
	if err := s.SetUserDisabled(ctx, p.Subject, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoginByToken(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Errorf("login after disabling: %v", err)
	}
	if got, _ := s.UserBySubject(ctx, p.Subject); !got.Disabled {
		t.Error("not disabled")
	}
	if err := s.DeleteUser(ctx, p.Subject); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserBySubject(ctx, p.Subject); !errors.Is(err, ErrNotFound) {
		t.Errorf("after deleting: %v", err)
	}
}

// Registering, confirming with a link, and the invitation picked up then.
func TestRegistration(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u, err := s.RegisterUser(ctx, "new@example.org", "hash", "invite-code")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterUser(ctx, "NEW@example.org", "hash", ""); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("twice: %v", err)
	}
	if got, _ := s.UserByEmail(ctx, "new@example.org"); got.Confirmed || got.PasswordHash != "hash" || !got.Registered {
		t.Errorf("registered: %+v", got)
	}
	s.AddEmailLink(ctx, EmailLink{Subject: u.Subject, LinkHash: []byte("L1"), Purpose: "verify", Expires: time.Now().Add(time.Hour)})
	// A newer link replaces the first.
	s.AddEmailLink(ctx, EmailLink{Subject: u.Subject, LinkHash: []byte("L2"), Purpose: "verify", Expires: time.Now().Add(time.Hour)})
	if _, err := s.TakeEmailLink(ctx, "verify", []byte("L1")); !errors.Is(err, ErrNotFound) {
		t.Errorf("an older link: %v", err)
	}
	if _, err := s.TakeEmailLink(ctx, "reset", []byte("L2")); !errors.Is(err, ErrNotFound) {
		t.Errorf("another purpose: %v", err)
	}
	if sub, err := s.TakeEmailLink(ctx, "verify", []byte("L2")); err != nil || sub != u.Subject {
		t.Errorf("the link: %q %v", sub, err)
	}
	if _, err := s.TakeEmailLink(ctx, "verify", []byte("L2")); !errors.Is(err, ErrNotFound) {
		t.Errorf("used twice: %v", err)
	}
	if err := s.ConfirmUser(ctx, u.Subject); err != nil {
		t.Errorf("confirming: %v", err)
	}
	// The invitation stays with the person, for their account.
	if code, err := s.UserInvitation(ctx, u.Subject); err != nil || code != "invite-code" {
		t.Errorf("the invitation: %q %v", code, err)
	}
	admin, _ := s.CreateUser(ctx, "added@example.org", false)
	if code, err := s.UserInvitation(ctx, admin.Subject); err != nil || code != "" {
		t.Errorf("someone added: %q %v", code, err)
	}
	if got, _ := s.UserByEmail(ctx, "new@example.org"); !got.Confirmed {
		t.Error("not confirmed")
	}
	// Those who never confirm go.
	s.RegisterUser(ctx, "late@example.org", "hash", "")
	if n, err := s.DeleteUnconfirmed(ctx, time.Now().Add(time.Hour)); err != nil || n != 1 {
		t.Errorf("unconfirmed: %d %v", n, err)
	}
	s.SetMustChange(ctx, u.Subject, true)
	if got, _ := s.UserByEmail(ctx, "new@example.org"); !got.MustChange {
		t.Error("must change")
	}
	if r, _ := s.PasswordRules(ctx); r.MinLength != 10 || !r.Digit || !r.Special {
		t.Errorf("default rules: %+v", r)
	}
}

// Setup links: one per person whichever way it went, looked at without
// using it up, cancelled; setting up records how; someone an admin made
// and held isn't removed with those who never confirmed.
func TestSetupLinks(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u, _ := s.CreateUser(ctx, "made@example.org", false)
	if err := s.SetSetupPending(ctx, u.Subject, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.UserBySubject(ctx, u.Subject); !got.SetupPending || got.Unverified || !got.Confirmed {
		t.Errorf("made: %+v", got)
	}
	week := time.Now().Add(7 * 24 * time.Hour)
	if err := s.AddEmailLink(ctx, EmailLink{Subject: u.Subject, LinkHash: []byte("R"), Purpose: "reset", Expires: week}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSetupLink(ctx, EmailLink{Subject: u.Subject, LinkHash: []byte("S1"), Purpose: "setup-sent", Expires: week}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSetupLink(ctx, EmailLink{Subject: u.Subject, LinkHash: []byte("S2"), Purpose: "setup", Expires: week}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSetupLink(ctx, EmailLink{Subject: u.Subject, LinkHash: []byte("X"), Purpose: "reset", Expires: week}); err == nil {
		t.Error("a reset link taken for a setup link")
	}
	if _, err := s.PeekEmailLink(ctx, "setup-sent", []byte("S1")); !errors.Is(err, ErrNotFound) {
		t.Errorf("the emailed link after a copied one: %v", err)
	}
	if at, err := s.SetupLinkExpires(ctx, u.Subject); err != nil || at.Unix() != week.Unix() {
		t.Errorf("expires: %v %v", at, err)
	}
	for i := 0; i < 2; i++ {
		if sub, err := s.PeekEmailLink(ctx, "setup", []byte("S2")); err != nil || sub != u.Subject {
			t.Errorf("look %d: %q %v", i, sub, err)
		}
	}
	if err := s.CancelSetupLink(ctx, u.Subject); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TakeEmailLink(ctx, "setup", []byte("S2")); !errors.Is(err, ErrNotFound) {
		t.Errorf("a cancelled link: %v", err)
	}
	if at, _ := s.SetupLinkExpires(ctx, u.Subject); !at.IsZero() {
		t.Errorf("expires after cancelling: %v", at)
	}
	if _, err := s.TakeEmailLink(ctx, "reset", []byte("R")); err != nil {
		t.Errorf("the reset link went with the setup link: %v", err)
	}
	s.AddSetupLink(ctx, EmailLink{Subject: u.Subject, LinkHash: []byte("OLD"), Purpose: "setup", Expires: time.Now().Add(-time.Second)})
	if _, err := s.PeekEmailLink(ctx, "setup", []byte("OLD")); !errors.Is(err, ErrNotFound) {
		t.Errorf("an expired link: %v", err)
	}

	// Reminder: signs in, address not proven; confirming proves it.
	if err := s.SetupDone(ctx, u.Subject, false, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.UserBySubject(ctx, u.Subject); got.SetupPending || !got.Unverified || !got.Confirmed || got.MustChange {
		t.Errorf("reminder: %+v", got)
	}
	s.ConfirmUser(ctx, u.Subject)
	if got, _ := s.UserBySubject(ctx, u.Subject); got.Unverified || !got.Confirmed {
		t.Errorf("confirmed: %+v", got)
	}
	// Hold: not confirmed, and not removed as a registration never confirmed.
	held, _ := s.CreateUser(ctx, "held@example.org", false)
	s.SetupDone(ctx, held.Subject, true, true)
	if got, _ := s.UserBySubject(ctx, held.Subject); got.Confirmed || got.Unverified {
		t.Errorf("held: %+v", got)
	}
	if n, err := s.DeleteUnconfirmed(ctx, time.Now().Add(time.Hour)); err != nil || n != 0 {
		t.Errorf("someone made, held: %d removed, %v", n, err)
	}

	if v, _ := s.CopiedSetup(ctx); v != "reminder" {
		t.Errorf("default: %q", v)
	}
	if err := s.SetCopiedSetup(ctx, "hold"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.CopiedSetup(ctx); v != "hold" {
		t.Errorf("hold: %q", v)
	}
	if err := s.SetCopiedSetup(ctx, "other"); err == nil {
		t.Error("an unknown choice was kept")
	}
}
