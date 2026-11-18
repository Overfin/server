package lang

import (
	"slices"
	"strings"
	"testing"
)

// A country's languages: its official ones TMDB has translations in, the
// most spoken first.
func TestCountryLanguages(t *testing.T) {
	for country, want := range map[string]string{
		"CA": "en-CA,fr-CA",
		// TMDB has only de-CH, but answers fr-CH and it-CH in its French
		// and Italian: the language is what counts.
		"CH": "de-CH,fr-CH,it-CH",
		// Spanish is official in some states (CLDR: official_regional).
		"US": "en-US,es-US",
		"FR": "fr-FR",
		"XX": "",
	} {
		if got := strings.Join(CountryLanguages(country), ","); got != want {
			t.Errorf("%s: %q, want %q", country, got, want)
		}
	}
	if Name("de-CH") != "Deutsch (Schweiz)" || Name("fr-CA") != "Français (Canada)" {
		t.Errorf("names: %q %q", Name("de-CH"), Name("fr-CA"))
	}
	// A copy: changing it changes nothing here.
	CountryLanguages("CA")[0] = "xx"
	if CountryLanguages("CA")[0] != "en-CA" {
		t.Error("the list was changed")
	}

	// Every country's language is known, named, of that country, and each
	// of the Choices is some country's with the same name.
	countries := Countries()
	if !slices.IsSorted(countries) || !slices.Contains(countries, "CA") || slices.Contains(countries, "XX") {
		t.Errorf("countries: %v", countries)
	}
	all := map[string]bool{}
	for _, c := range countries {
		for _, tag := range CountryLanguages(c) {
			if Base(tag) == "" || !strings.HasSuffix(tag, "-"+c) || countryNames[tag] == "" {
				t.Errorf("%s: %s", c, tag)
			}
			all[tag] = true
		}
	}
	for _, c := range Choices {
		if !all[c.Tag] || countryNames[c.Tag] != c.Name {
			t.Errorf("%s: %q, a country's %q", c.Tag, c.Name, countryNames[c.Tag])
		}
	}
	if got := Named([]string{"fr-CA", "de-CH"}); len(got) != 2 || got[1] != (Choice{"de-CH", "Deutsch (Schweiz)"}) {
		t.Errorf("Named: %v", got)
	}
}
