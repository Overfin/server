package store

import (
	"context"
	"testing"
)

// What a profile may see changing moves the number the backend sends its
// gateway (migration 107); a change that narrows nothing doesn't.
func TestAccessChanges(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	last := func() int64 {
		n, err := s.AccessChange(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	acct, prof, err := s.CreateAccount(ctx, "subject-1", owner("1"))
	if err != nil {
		t.Fatal(err)
	}
	start := last()
	if err := s.SetProfileTheme(ctx, acct.ID, prof.ID, "dark"); err != nil {
		t.Fatal(err)
	}
	if last() != start {
		t.Error("a theme counted as an access change")
	}
	steps := []struct {
		name string
		do   func() error
	}{
		{"libraries", func() error { return s.SetProfileAccess(ctx, acct.ID, prof.ID, []string{"lib1"}, nil) }},
		{"categories", func() error { return s.SetProfileCategories(ctx, acct.ID, prof.ID, []string{"cat_1"}) }},
		{"kids titles", func() error { on := true; return s.SetProfileKidsTitles(ctx, acct.ID, prof.ID, &on) }},
	}
	for _, st := range steps {
		before := last()
		if err := st.do(); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if last() <= before {
			t.Errorf("%s: not counted", st.name)
		}
	}
	other, err := s.AddProfile(ctx, acct.ID, NewProfile{Name: "Kid", Kind: "standard", JellyfinUserID: "jf-2", JellyfinUsername: "of-2", JellyfinToken: "t-2"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	before := last()
	if err := s.DeleteProfile(ctx, acct.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	if last() <= before {
		t.Error("a deleted profile: not counted")
	}
	// Only the last number is kept.
	var rows int
	s.read.QueryRowContext(ctx, `SELECT count(*) FROM access_changes`).Scan(&rows)
	if rows != 1 {
		t.Errorf("%d rows kept", rows)
	}
}
