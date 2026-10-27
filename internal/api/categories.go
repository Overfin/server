package api

import (
	"errors"
	"net/http"

	"github.com/overfin/server/internal/ids"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// Categories: the sections of the site the operator made from rules, and
// whether the site's menu shows the libraries too.

type categoryJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Home makes it a row on the home page.
	Home bool `json:"home"`
}

func (l Library) categories(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	st := l.Provisioner.Store
	all, err := st.Categories(ctx)
	if err != nil {
		l.storeError(w, err)
		return
	}
	shown, err := st.LibrariesShown(ctx)
	if err != nil {
		l.storeError(w, err)
		return
	}
	lang := p.TextLanguage
	if lang == "" {
		if _, lang, _, err = st.Languages(ctx); err != nil {
			l.storeError(w, err)
			return
		}
	}
	out := []categoryJSON{}
	for _, c := range all {
		out = append(out, categoryJSON{ID: c.ID, Name: c.Name(lang), Home: c.Home})
	}
	writeOK(w, map[string]any{"items": out, "libraries": shown})
}

// categoryFilter reads the category a listing asks for ("" for none), or
// writes the problem and returns false.
func (l Library) categoryFilter(w http.ResponseWriter, r *http.Request) (*store.CategoryFilter, bool) {
	id := r.URL.Query().Get("category")
	if id == "" {
		return nil, true
	}
	if !ids.Valid("cat", id) {
		problem.Send(w, problem.InvalidRequest, "")
		return nil, false
	}
	f, err := l.Provisioner.Store.CategoryFilterOf(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		problem.Send(w, problem.NotFound, "")
		return nil, false
	}
	if err != nil {
		l.storeError(w, err)
		return nil, false
	}
	return f, true
}
