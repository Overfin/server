package api_test

import (
	"net/http"
	"testing"
)

// wishes reads the tracks a title's next playback starts with, as its
// page says them.
func wishes(t *testing.T, p *played, title string) (audio, subtitles string) {
	t.Helper()
	var page titlePage
	if code := get(t, p.srv, "/api/v1/titles/"+title, &page); code != 200 || page.Wishes == nil {
		t.Fatalf("%s: %d, wishes %+v", title, code, page.Wishes)
	}
	return page.Wishes.Audio, page.Wishes.Subtitles
}

// Tracks chosen while watching are kept by language for the title: its
// next playbacks start with them, on any device. Heat's copy: a1 English
// E-AC-3, a2 French AAC; s3 French SubRip.
func TestChooseTracks(t *testing.T) {
	p := playedServer(t, options{})
	id := startDelivery(t, p.srv, "tv", `"delivery":"remux","mediaType":"video/mp4"`)
	tracks := "/api/v1/playback/" + id + "/tracks"

	if code, body := put(t, p.srv, "PUT", tracks, `{"audioTrack":"a2"}`); code != http.StatusNoContent {
		t.Fatalf("audio a2: %d %s", code, body)
	}
	if audio, _ := wishes(t, p, "title_movie_tmdb_949"); audio != "fr" {
		t.Errorf("audio after a2: %q, want fr", audio)
	}
	if code, body := put(t, p.srv, "PUT", tracks, `{"subtitleTrack":"s3"}`); code != http.StatusNoContent {
		t.Fatalf("subtitles s3: %d %s", code, body)
	}
	if _, subs := wishes(t, p, "title_movie_tmdb_949"); subs != "fr" {
		t.Errorf("subtitles after s3: %q, want fr", subs)
	}
	// "" is no subtitles, kept as such.
	if code, body := put(t, p.srv, "PUT", tracks, `{"subtitleTrack":""}`); code != http.StatusNoContent {
		t.Fatalf("no subtitles: %d %s", code, body)
	}
	if audio, subs := wishes(t, p, "title_movie_tmdb_949"); subs != "" || audio != "fr" {
		t.Errorf("after none: audio %q subtitles %q, want fr and none", audio, subs)
	}
	// Another title keeps nothing of Heat's.
	if audio, _ := wishes(t, p, "title_movie_tmdb_14160"); audio == "fr" {
		t.Errorf("Up took Heat's audio")
	}

	// A track the copy doesn't have changes nothing; malformed ones and an
	// empty request are refused; an unknown playback is not found.
	if code, _ := put(t, p.srv, "PUT", tracks, `{"audioTrack":"a9"}`); code != http.StatusNoContent {
		t.Errorf("unknown track a9: %d", code)
	}
	if audio, _ := wishes(t, p, "title_movie_tmdb_949"); audio != "fr" {
		t.Errorf("audio after a9: %q, want fr still", audio)
	}
	for _, body := range []string{`{}`, `{"audioTrack":"s3"}`, `{"audioTrack":""}`, `{"subtitleTrack":"a1"}`,
		`{"audioTrack":"a01"}`, `{"audioTrack":"x"}`, `{"audioTrack":"a1","extra":1}`, `not json`} {
		if code, _ := put(t, p.srv, "PUT", tracks, body); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, code)
		}
	}
	if code, _ := put(t, p.srv, "PUT", "/api/v1/playback/"+id+"x/tracks", `{"audioTrack":"a1"}`); code != http.StatusNotFound {
		t.Errorf("unknown playback: %d, want 404", code)
	}
}

// The profile's subtitles: a language its next playbacks start with when
// nothing else was chosen for the title; "" for none; anything but a
// language tag refused.
func TestSetSubtitles(t *testing.T) {
	p := playedServer(t, options{})
	if code, body := put(t, p.srv, "PUT", "/api/v1/me/subtitles", `{"language":"fr"}`); code != http.StatusNoContent {
		t.Fatalf("fr: %d %s", code, body)
	}
	if _, subs := wishes(t, p, "title_movie_tmdb_14160"); subs != "fr" {
		t.Errorf("Up's subtitles: %q, want fr", subs)
	}
	if code, _ := put(t, p.srv, "PUT", "/api/v1/me/subtitles", `{"language":""}`); code != http.StatusNoContent {
		t.Errorf("none: %d", code)
	}
	if _, subs := wishes(t, p, "title_movie_tmdb_14160"); subs != "" {
		t.Errorf("Up's subtitles after none: %q", subs)
	}
	for _, body := range []string{`{"language":"not a tag"}`, `{"language":"<b>"}`, `{"language":1}`, `{}x`} {
		if code, _ := put(t, p.srv, "PUT", "/api/v1/me/subtitles", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, code)
		}
	}
}
