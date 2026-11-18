package lang

import "slices"

// Choice is a language an operator can offer: a regional variant, as TMDB
// names its translations, and its name in itself.
type Choice struct {
	Tag  string `json:"tag"`  // e.g. "fr-CA"
	Name string `json:"name"` // e.g. "Français (Canada)"
}

// Choices are the languages an operator can offer. The site's own texts
// exist in some of them (English, French); the others get English texts,
// and titles in their language.
var Choices = []Choice{
	{"en-US", "English (United States)"},
	{"en-GB", "English (United Kingdom)"},
	{"en-CA", "English (Canada)"},
	{"en-AU", "English (Australia)"},
	{"fr-CA", "Français (Canada)"},
	{"fr-FR", "Français (France)"},
	{"es-ES", "Español (España)"},
	{"es-MX", "Español (México)"},
	{"pt-BR", "Português (Brasil)"},
	{"pt-PT", "Português (Portugal)"},
	{"de-DE", "Deutsch (Deutschland)"},
	{"it-IT", "Italiano (Italia)"},
	{"nl-NL", "Nederlands (Nederland)"},
	{"sv-SE", "Svenska (Sverige)"},
	{"da-DK", "Dansk (Danmark)"},
	{"nb-NO", "Norsk bokmål (Norge)"},
	{"fi-FI", "Suomi (Suomi)"},
	{"pl-PL", "Polski (Polska)"},
	{"cs-CZ", "Čeština (Česko)"},
	{"ru-RU", "Русский (Россия)"},
	{"uk-UA", "Українська (Україна)"},
	{"tr-TR", "Türkçe (Türkiye)"},
	{"ja-JP", "日本語 (日本)"},
	{"ko-KR", "한국어 (대한민국)"},
	{"zh-CN", "中文 (中国)"},
}

// Offered reports whether tag is one of the Choices.
func Offered(tag string) bool {
	return slices.ContainsFunc(Choices, func(c Choice) bool { return c.Tag == tag })
}

// Region picks the choice for a language in a country ("fr", "CA" →
// "fr-CA"), else the first choice in that language; "" when there's none.
func Region(language, country string) string {
	first := ""
	for _, c := range Choices {
		if Base(c.Tag) != Base(language) || Base(language) == "" {
			continue
		}
		if c.Tag[3:] == country {
			return c.Tag
		}
		if first == "" {
			first = c.Tag
		}
	}
	return first
}

// CountryLanguages are the languages an operator can offer in a country
// (its ISO 3166 code, such as "CA"), the most spoken first; none for a
// country Overfin has none for. They're in countries.go, made from
// Unicode CLDR and TMDB's translations by tools/countrylangs.
func CountryLanguages(country string) []string {
	return slices.Clone(countryLanguages[country])
}

// Countries are the countries with languages to offer (CountryLanguages),
// by their codes, in order.
func Countries() []string {
	out := make([]string, 0, len(countryLanguages))
	for c := range countryLanguages {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

// Named gives languages their names (Name), in the order given.
func Named(tags []string) []Choice {
	out := make([]Choice, 0, len(tags))
	for _, t := range tags {
		out = append(out, Choice{t, Name(t)})
	}
	return out
}
