package api

import "testing"

// Titles are joined in order; an empty one adds nothing.
func TestMergeSharedNames(t *testing.T) {
	rows := []episodeJSON{{Overview: "a"}, {Overview: "b"}}
	rows[0].ID, rows[0].Name = titleID("series/tmdb/1/s1/e1"), "Rollercoaster"
	rows[1].ID, rows[1].Name = titleID("series/tmdb/1/s1/e2"), "Lawn Gnome"
	got := mergeShared(rows, []string{"series/tmdb/1/s1/e1", "series/tmdb/1/s1/e2"}, map[string]string{"series/tmdb/1/s1/e2": "series/tmdb/1/s1/e1"})
	if len(got) != 1 || got[0].Name != "Rollercoaster / Lawn Gnome" || got[0].EpisodeEnd != 2 || got[0].Parts[1].Overview != "b" {
		t.Errorf("merged: %+v", got)
	}
}
