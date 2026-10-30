package admin

import "net/http"

// appCategoryJSON is one of the operator's categories: its ID, as titles
// give it, and its name in each language.
type appCategoryJSON struct {
	ID    string            `json:"id"`
	Names map[string]string `json:"names"`
}

// appCategories (GET /apps/v1/categories, any key) lists the operator's
// categories, in their order: an app that adds titles (Overswell) lets the
// operator say where each category's titles go.
func (p *Panel) appCategories(w http.ResponseWriter, r *http.Request) {
	if p.appKey(w, r, "") == nil {
		return
	}
	categories, err := p.Store.Categories(r.Context())
	if err != nil {
		p.fail(w, "reading the categories", err)
		return
	}
	out := []appCategoryJSON{}
	for _, c := range categories {
		out = append(out, appCategoryJSON{ID: c.ID, Names: c.Names})
	}
	writeJSON(w, http.StatusOK, map[string]any{"categories": out})
}
