package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A code goes: waiting, confirmed once, collected once, by its secret only;
// what ends a person's logins ends what they confirmed; an old one is over.
func TestDeviceCodes(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	soon := time.Now().Add(time.Minute)
	if err := s.CreateDeviceCode(ctx, "CODE", "secret", "Chrome · Linux", soon); err != nil {
		t.Fatal(err)
	}
	if d, err := s.PendingDeviceCode(ctx, "CODE"); err != nil || d.Device != "Chrome · Linux" {
		t.Fatalf("pending: %+v, %v", d, err)
	}
	if got, err := s.CollectDeviceCode(ctx, "secret"); got != "" || err != nil {
		t.Errorf("collected before confirming: %q, %v", got, err)
	}
	if _, err := s.CollectDeviceCode(ctx, "CODE"); !errors.Is(err, ErrNotFound) {
		t.Errorf("collected by the code, not the secret: %v", err)
	}
	if err := s.ConfirmDeviceCode(ctx, "other", "ann"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another code: %v", err)
	}
	if err := s.ConfirmDeviceCode(ctx, "CODE", "ann"); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmDeviceCode(ctx, "CODE", "bob"); !errors.Is(err, ErrNotFound) {
		t.Errorf("confirmed twice: %v", err)
	}
	if _, err := s.PendingDeviceCode(ctx, "CODE"); !errors.Is(err, ErrNotFound) {
		t.Errorf("still pending: %v", err)
	}
	if got, err := s.CollectDeviceCode(ctx, "secret"); got != "ann" || err != nil {
		t.Errorf("collect: %q, %v", got, err)
	}
	if _, err := s.CollectDeviceCode(ctx, "secret"); !errors.Is(err, ErrNotFound) {
		t.Errorf("collected twice: %v", err)
	}

	// Ending logins.
	for _, end := range []func() error{
		func() error { return s.DeleteLogins(ctx, "ann") },
		func() error { return s.DeleteOtherLogins(ctx, "ann", "kept") },
	} {
		s.CreateDeviceCode(ctx, "C2", "s2", "d", soon)
		s.ConfirmDeviceCode(ctx, "C2", "ann")
		if err := end(); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CollectDeviceCode(ctx, "s2"); !errors.Is(err, ErrNotFound) {
			t.Errorf("survives the logins' end: %v", err)
		}
	}

	// Over.
	s.CreateDeviceCode(ctx, "OLD", "s3", "d", time.Now().Add(-time.Minute))
	if _, err := s.PendingDeviceCode(ctx, "OLD"); !errors.Is(err, ErrNotFound) {
		t.Errorf("old pending: %v", err)
	}
	if err := s.ConfirmDeviceCode(ctx, "OLD", "ann"); !errors.Is(err, ErrNotFound) {
		t.Errorf("old confirmed: %v", err)
	}
	if _, err := s.CollectDeviceCode(ctx, "s3"); !errors.Is(err, ErrNotFound) {
		t.Errorf("old collected: %v", err)
	}
	if err := s.DeleteExpiredDeviceCodes(ctx); err != nil {
		t.Fatal(err)
	}
	var left int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM device_codes`).Scan(&left)
	if left != 0 {
		t.Errorf("%d codes left", left)
	}
}
