package api

import (
	"testing"

	"github.com/overfin/server/internal/library"
)

// TestDescribe: what plays, in words, for the control panel's live view.
func TestDescribe(t *testing.T) {
	s, e := 2, 5
	for _, tc := range []struct {
		title *library.TitleSummary
		want  string
	}{
		{nil, ""},
		{&library.TitleSummary{Kind: "movie", Name: "Free Guy", Year: 2021}, "Free Guy (2021)"},
		{&library.TitleSummary{Kind: "movie", Name: "Free Guy"}, "Free Guy"},
		{&library.TitleSummary{Kind: "episode", Name: "Le poison", Series: &library.TitleParent{Name: "19-2"},
			SeasonNumber: &s, EpisodeNumber: &e}, "19-2 · S2 E5 · Le poison"},
		{&library.TitleSummary{Kind: "episode", Name: "Pilot", Series: &library.TitleParent{Name: "19-2"}}, "19-2 · Pilot"},
	} {
		if got := describe(tc.title); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.title, got, tc.want)
		}
	}
}
