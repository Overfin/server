package admin

import (
	"slices"
	"testing"

	"github.com/overfin/server/internal/store"
)

// The category editor's genres are in English, the panel's language, even
// when the titles came in French; a series genre says it's one.
func TestPanelGenres(t *testing.T) {
	got := panelGenres([]store.TasteGenre{{ID: 878, Name: "Science-Fiction"}, {ID: 10765, Name: "Sci-Fi & Fantasy"},
		{ID: 27, Name: "Horreur"}, {ID: 10762, Name: "Kids"}, {ID: 99999, Name: "Nouveau"}})
	var names []string
	for _, g := range got {
		names = append(names, g.Name)
	}
	want := []string{"Horror", "Kids (series)", "Nouveau", "Sci-Fi & Fantasy (series)", "Science Fiction"}
	if !slices.Equal(names, want) {
		t.Errorf("got %q, want %q", names, want)
	}
}
