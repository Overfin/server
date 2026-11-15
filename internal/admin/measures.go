package admin

import (
	"net/http"
	"time"
)

// measureDays is how far back the measures look.
const measureDays = 90

// measures (GET /admin/api/measures) are whether the library follows the
// people using it (store.Measures): totals for the whole server.
func (p *Panel) measures(w http.ResponseWriter, r *http.Request, _ *session) {
	p.writeMeasures(w, r)
}

// writeMeasures answers with the measures as of now.
func (p *Panel) writeMeasures(w http.ResponseWriter, r *http.Request) {
	m, err := p.Store.LibraryMeasures(r.Context(), time.Now(), measureDays)
	if err != nil {
		p.fail(w, "computing the measures", err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// appMeasures (GET /apps/v1/measures, history scope) are the same measures,
// for an app that adds and removes titles: totals for the whole server,
// nothing per title or per person (SECURITY.md rule 85).
func (p *Panel) appMeasures(w http.ResponseWriter, r *http.Request) {
	if p.appKey(w, r, "history") == nil {
		return
	}
	p.writeMeasures(w, r)
}
