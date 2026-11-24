package store

import (
	"context"
	"testing"
	"time"
)

// A database from before profiles lost their Jellyfin user columns (none
// set, as every profile had been imported) migrates with its profiles and
// everything pointing at them: sessions, what they did with titles, the
// access triggers.
func TestProfilesLoseJellyfinUsers(t *testing.T) {
	s := openAt(t, 138)
	ctx := context.Background()
	acct, prof, err := s.CreateAccount(ctx, "subject-1", owner("1"))
	if err != nil {
		t.Fatal(err)
	}
	age := 9
	kid, err := s.AddProfile(ctx, acct.ID, NewProfile{Name: "Kid", Kind: "kids", MaxAge: &age, Libraries: []string{"lib"}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE profiles SET theme = 'dark', audio_language = 'original',
		subtitle_language = 'fr', kids_titles = 1 WHERE id = ?`, prof.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO profile_imports (profile_id, imported_at) VALUES (?, 1), (?, 1)`,
		prof.ID, kid.ID); err != nil {
		t.Fatal(err)
	}
	sess, _, err := s.CreateSession(ctx, acct.ID, prof.ID, "phone", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	s.RecordPlay(ctx, prof.ID, "movie/tmdb/949", "item", 60_000, false, time.Now())
	var nulls int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM profiles WHERE jellyfin_user_id IS NULL
		AND jellyfin_username IS NULL AND jellyfin_token IS NULL`).Scan(&nulls)
	if nulls != 2 {
		t.Fatalf("before: %d profiles without a Jellyfin user", nulls)
	}

	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}

	got, err := s.Profiles(ctx, acct.ID)
	if err != nil || len(got) != 2 {
		t.Fatalf("profiles: %v %v", got, err)
	}
	if p := got[0]; p.ID != prof.ID || !p.Owner || p.Theme != "dark" || p.AudioLanguage != "original" ||
		p.SubtitleLanguage != "fr" || p.KidsTitles == nil || !*p.KidsTitles {
		t.Errorf("owner: %+v", p)
	}
	if k := got[1]; k.ID != kid.ID || k.Kind != "kids" || k.MaxAge == nil || *k.MaxAge != 9 || len(k.Libraries) != 1 {
		t.Errorf("kid: %+v", k)
	}
	for _, gone := range []string{"jellyfin_user_id", "jellyfin_username", "jellyfin_token"} {
		var n int
		s.db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('profiles') WHERE name = ?`, gone).Scan(&n)
		if n != 0 {
			t.Errorf("column %s still there", gone)
		}
	}
	var n int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = 'profile_imports'`).Scan(&n)
	if n != 0 {
		t.Error("profile_imports still there")
	}
	var active string
	s.db.QueryRowContext(ctx, `SELECT coalesce(active_profile_id, '') FROM sessions WHERE id = ?`, sess.ID).Scan(&active)
	if active != prof.ID {
		t.Errorf("session's profile: %q", active)
	}
	if states, _ := s.PersonalStates(ctx, prof.ID, []string{"movie/tmdb/949"}); states["movie/tmdb/949"].PositionMs != 60_000 {
		t.Errorf("history: %+v", states)
	}
	// Still one owner per account, names unique within it.
	if _, err := s.db.ExecContext(ctx, `UPDATE profiles SET owner = 1 WHERE id = ?`, kid.ID); err == nil {
		t.Error("a second owner was accepted")
	}
	if _, err := s.AddProfile(ctx, acct.ID, NewProfile{Name: "Kid", Kind: "standard"}, 0); err == nil {
		t.Error("a name used twice was accepted")
	}
	// The access triggers came back.
	var before, after int
	s.db.QueryRowContext(ctx, `SELECT coalesce(max(seq), 0) FROM access_changes`).Scan(&before)
	if _, err := s.db.ExecContext(ctx, `UPDATE profiles SET max_age = 12 WHERE id = ?`, kid.ID); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRowContext(ctx, `SELECT coalesce(max(seq), 0) FROM access_changes`).Scan(&after)
	if after <= before {
		t.Error("an access change went unnoticed")
	}
	var fk int
	s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk)
	if fk != 1 {
		t.Error("foreign keys left off")
	}
	// And they still hold: deleting the account takes its profiles' rows.
	if err := s.DeleteAccount(ctx, acct.ID); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM profile_titles`).Scan(&n)
	if n != 0 {
		t.Errorf("cascade after the rebuild: %d rows left", n)
	}
}

// Continue Watching finds a title wherever its copy is now: the item it was
// last seen as, else another with the same title, else the item as kept.
func TestInProgressFollowsTheTitle(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	s.PutCatalog(ctx, []CatalogItem{{ID: "0000000000000000000000000000000b", Kind: "movie", Name: "Heat", SortName: "Heat", TMDB: "949"}}, 1)
	s.RefreshTitleKeys(ctx)
	now := time.Now()
	// Watched as an item the library no longer has.
	s.RecordPlay(ctx, prof.ID, "movie/tmdb/949", "0000000000000000000000000000000a", 60_000, false, now)
	// And something the index doesn't hold (an audiobook, say).
	s.RecordPlay(ctx, prof.ID, "item/0000000000000000000000000000000c", "0000000000000000000000000000000c", 60_000, false, now.Add(-time.Hour))
	got, err := s.InProgress(ctx, prof.ID, nil, 0, 10)
	if err != nil || len(got) != 2 || got[0] != "0000000000000000000000000000000b" || got[1] != "0000000000000000000000000000000c" {
		t.Errorf("in progress: %v %v", got, err)
	}
	if got, _ := s.InProgress(ctx, prof.ID, []string{"movie"}, 0, 10); len(got) != 1 {
		t.Errorf("films in progress: %v", got)
	}
}
