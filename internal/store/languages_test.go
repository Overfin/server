package store

import (
	"context"
	"strings"
	"testing"
)

func TestLanguages(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	// Never set: English (United States).
	if offered, def, set, err := s.Languages(ctx); err != nil || set || def != "en-US" || strings.Join(offered, ",") != "en-US" {
		t.Errorf("fresh: %v %q %v %v", offered, def, set, err)
	}
	// An install from before: its TMDB language in its ratings country.
	if err := s.SetTMDBLanguage(ctx, "fr"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRatingsCountry(ctx, "CA"); err != nil {
		t.Fatal(err)
	}
	if _, def, set, _ := s.Languages(ctx); set || def != "fr-CA" {
		t.Errorf("from before: %q %v", def, set)
	}

	// Saved: TMDB is asked in the default's tag ("fr" was the old way to
	// ask for it), and changing it says so.
	changed, err := s.SetLanguages(ctx, []string{"fr-CA", "en-US"}, "fr-CA")
	if l, _ := s.TMDBLanguage(ctx); err != nil || !changed || l != "fr-CA" {
		t.Errorf("fr to fr-CA: %v %v, TMDB's %q", changed, err, l)
	}
	if changed, err := s.SetLanguages(ctx, []string{"fr-CA", "en-US", "de-DE"}, "fr-CA"); err != nil || changed {
		t.Errorf("same default: %v %v", changed, err)
	}
	if offered, def, set, _ := s.Languages(ctx); !set || def != "fr-CA" || strings.Join(offered, ",") != "fr-CA,en-US,de-DE" {
		t.Errorf("saved: %v %q %v", offered, def, set)
	}
	if changed, err := s.SetLanguages(ctx, []string{"fr-CA", "en-US"}, "en-US"); err != nil || !changed {
		t.Errorf("another default: %v %v", changed, err)
	}
	if l, _ := s.TMDBLanguage(ctx); l != "en-US" {
		t.Errorf("TMDB's language: %q", l)
	}

	// An install from before, asking TMDB in two letters, is brought in
	// line once.
	if err := s.SetTMDBLanguage(ctx, "en"); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.SyncTMDBLanguage(ctx); err != nil || !changed {
		t.Errorf("sync: %v %v", changed, err)
	}
	if changed, _ := s.SyncTMDBLanguage(ctx); changed {
		t.Error("synced twice")
	}
}

// An install from before the server's country was asked: its default
// language's region, else the first other language's Overfin has
// languages for, else none; once saved, the one saved.
func TestServerCountry(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	country := func() string {
		t.Helper()
		c, err := s.ServerCountry(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// Languages not set yet: none, the panel asks.
	if c := country(); c != "" {
		t.Errorf("fresh: %q", c)
	}
	for _, tc := range []struct {
		offered []string
		def     string
		want    string
	}{
		{[]string{"en-US", "fr-FR"}, "fr-FR", "FR"},
		{[]string{"fr-CA", "en-CA"}, "fr-CA", "CA"},
		// No region, or one Overfin has no languages for: the next.
		{[]string{"fr", "en-GB"}, "fr", "GB"},
		{[]string{"fr-AQ", "de-CH"}, "fr-AQ", "CH"},
		{[]string{"fr", "es-419"}, "fr", ""},
	} {
		if _, err := s.SetLanguages(ctx, tc.offered, tc.def); err != nil {
			t.Fatal(err)
		}
		if c := country(); c != tc.want {
			t.Errorf("%v, %s: %q, want %q", tc.offered, tc.def, c, tc.want)
		}
	}
	// Saved: it stays, whatever the languages.
	if _, err := s.SetCountryLanguages(ctx, "CH", []string{"de-CH", "fr-FR"}, "fr-FR"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetLanguages(ctx, []string{"en-US"}, "en-US"); err != nil {
		t.Fatal(err)
	}
	if c := country(); c != "CH" {
		t.Errorf("saved: %q", c)
	}
}
