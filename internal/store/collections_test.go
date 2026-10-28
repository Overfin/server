package store

import "testing"

// Collections sort by their own name, the word naming them one aside.
func TestCollectionSortName(t *testing.T) {
	for name, want := range map[string]string{
		"Collection Avatar":              "Avatar",
		"Avatar Collection":              "Avatar",
		"Star Wars - La Collection":      "Star Wars",
		"La saga Harry Potter":           "Harry Potter",
		"Collection":                     "Collection",
		"The Batman Collection":          "The Batman",
		"Collectionneurs":                "Collectionneurs",
		"İstanbul Collection":            "İstanbul",
		"Collection İ":                   "İ",
		"Le Seigneur des anneaux":        "Le Seigneur des anneaux",
		"  Collection Vendredi 13  ":     "Vendredi 13",
		"Collection Demon Slayer – Saga": "Demon Slayer",
	} {
		if got := collectionSortName(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
}
