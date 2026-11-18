package admin

import (
	"net/http"
	"slices"

	"github.com/overfin/server/internal/lang"
)

// languages are the server's country, the languages the operator offers,
// the default among them, and each country's languages that can be
// offered. choices names those offered.
func (p *Panel) languages(w http.ResponseWriter, r *http.Request, s *session) {
	offered, def, _, err := p.Store.Languages(r.Context())
	if err != nil {
		p.fail(w, "reading the languages", err)
		return
	}
	country, err := p.Store.ServerCountry(r.Context())
	if err != nil {
		p.fail(w, "reading the languages", err)
		return
	}
	countries := map[string][]lang.Choice{}
	for _, c := range lang.Countries() {
		countries[c] = lang.Named(lang.CountryLanguages(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"country": country, "offered": offered, "default": def,
		"choices": lang.Named(offered), "countries": countries})
}

// setLanguages changes the server's country, the languages offered and
// the default. Changing the default's language has titles' texts fetched
// again in it.
func (p *Panel) setLanguages(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		Country string   `json:"country"`
		Offered []string `json:"offered"`
		Default string   `json:"default"`
	}
	if !decode(w, r, &req) {
		return
	}
	before, _, _, err := p.Store.Languages(r.Context())
	if err != nil {
		p.fail(w, "changing the languages", err)
		return
	}
	// No country: the one there is (on an install from before, none yet).
	country := req.Country
	if country == "" {
		if country, err = p.Store.ServerCountry(r.Context()); err != nil {
			p.fail(w, "changing the languages", err)
			return
		}
	}
	if detail := checkLanguages(country, req.Offered, req.Default, before); detail != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": detail})
		return
	}
	// The ratings country may follow the server's country.
	ratings, err := p.Store.RatingsCountry(r.Context())
	if err != nil {
		p.fail(w, "changing the languages", err)
		return
	}
	changed, err := p.Store.SetCountryLanguages(r.Context(), req.Country, req.Offered, req.Default)
	if err != nil {
		p.fail(w, "changing the languages", err)
		return
	}
	if now, err := p.Store.RatingsCountry(r.Context()); err == nil && now != ratings {
		p.accessChanged()
		p.Logger.Info("the ratings country follows the server's country", "country", now)
	}
	p.Logger.Info("the languages changed in the control panel", "country", country, "offered", req.Offered, "default", req.Default, "by", s.logID)
	if changed && p.OnLanguage != nil {
		p.OnLanguage()
	}
	p.languages(w, r, s)
}

// checkLanguages says what's wrong with a choice of languages, in plain
// words; "" when nothing is. Each one offered must be the country's, or
// offered before (before): nothing is taken away by a change of country,
// and nothing from elsewhere added.
func checkLanguages(country string, offered []string, def string, before []string) string {
	if country != "" && len(lang.CountryLanguages(country)) == 0 {
		return "Overfin has no languages for that country."
	}
	if len(offered) == 0 {
		return "Offer at least one language."
	}
	own := lang.CountryLanguages(country)
	for i, tag := range offered {
		if !(slices.Contains(own, tag) || slices.Contains(before, tag)) || slices.Contains(offered[:i], tag) {
			return "Those aren't languages Overfin offers, each once."
		}
	}
	if !slices.Contains(offered, def) {
		return "The default language must be one of those offered."
	}
	return ""
}
