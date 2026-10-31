package store

import (
	"context"
	"testing"
)

// What's chosen for a title is kept per profile and title: audio and
// subtitles apart, subtitles turned off told from never chosen, gone with
// the profile.
func TestTitleTracks(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	acct, prof, err := s.CreateAccount(ctx, "subject-1", owner("1"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.TitleTracks(ctx, prof.ID, "series/tmdb/1399"); err != nil || got.Audio != "" || got.Subtitles != nil {
		t.Fatalf("nothing chosen: %+v %v", got, err)
	}
	s.SetTitleAudio(ctx, prof.ID, "series/tmdb/1399", "fr-CA")
	s.SetTitleSubtitles(ctx, prof.ID, "series/tmdb/1399", "")
	s.SetTitleSubtitles(ctx, prof.ID, "movie/tmdb/949", "en")
	if got, _ := s.TitleTracks(ctx, prof.ID, "series/tmdb/1399"); got.Audio != "fr-CA" || got.Subtitles == nil || *got.Subtitles != "" {
		t.Errorf("series: %+v", got)
	}
	if got, _ := s.TitleTracks(ctx, prof.ID, "movie/tmdb/949"); got.Audio != "" || got.Subtitles == nil || *got.Subtitles != "en" {
		t.Errorf("film: %+v", got)
	}
	if err := s.SetAudioLanguage(ctx, acct.ID, prof.ID, "original"); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Profile(ctx, acct.ID, prof.ID); p.AudioLanguage != "original" {
		t.Errorf("profile audio: %q", p.AudioLanguage)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM profiles WHERE id = ?`, prof.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM title_tracks`).Scan(&n)
	if n != 0 {
		t.Errorf("%d choices left after the profile", n)
	}
}
