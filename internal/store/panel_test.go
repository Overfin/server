package store

import (
	"context"
	"testing"
)

func TestPanelAccount(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, _, ok, err := s.PanelAccount(ctx); ok || err != nil {
		t.Fatalf("before any: %v %v", ok, err)
	}
	if err := s.SetPanelAccount(ctx, "operator", "hash1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPanelAccount(ctx, "operator", "hash2"); err != nil {
		t.Fatal(err)
	}
	u, h, ok, err := s.PanelAccount(ctx)
	if !ok || err != nil || u != "operator" || h != "hash2" {
		t.Errorf("got %q %q %v %v", u, h, ok, err)
	}
	if err := s.SetPanelAccount(ctx, "", "x"); err == nil {
		t.Error("no username accepted")
	}
}
