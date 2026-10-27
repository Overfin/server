package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/overfin/server/internal/ids"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/textclean"
)

// Categories: sections of the site made from rules, in the operator's
// order, and whether the site's menu shows the libraries too.

type categoryJSON struct {
	ID    string              `json:"id"`
	Names map[string]string   `json:"names"`
	Rules store.CategoryRules `json:"rules"`
	Home  bool                `json:"home"`
	// Count is how many films and series it has.
	Count int `json:"count"`
}

type choiceJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (p *Panel) categories(w http.ResponseWriter, r *http.Request, _ *session) {
	ctx := r.Context()
	st := p.Store
	all, err := st.Categories(ctx)
	if err != nil {
		p.fail(w, "listing categories", err)
		return
	}
	out := []categoryJSON{}
	for _, c := range all {
		n, err := p.categoryCount(ctx, c.Rules, c.ID)
		if err != nil {
			p.fail(w, "counting a category's titles", err)
			return
		}
		out = append(out, categoryJSON{ID: c.ID, Names: c.Names, Rules: c.Rules, Home: c.Home, Count: n})
	}
	// What rules can be made of.
	offered, def, _, err := st.Languages(ctx)
	if err != nil {
		p.fail(w, "reading the languages", err)
		return
	}
	genres, err := st.TasteGenres(ctx, def)
	if err != nil {
		p.fail(w, "listing genres", err)
		return
	}
	gs := []map[string]any{}
	for _, g := range genres {
		gs = append(gs, map[string]any{"id": g.ID, "name": g.Name})
	}
	langs, err := st.LibraryLanguages(ctx)
	if err != nil {
		p.fail(w, "listing languages", err)
		return
	}
	libs, err := st.Libraries(ctx)
	if err != nil {
		p.fail(w, "listing libraries", err)
		return
	}
	ls := []choiceJSON{}
	for _, l := range libs {
		var item struct{ Name string }
		json.Unmarshal(l.Item, &item)
		ls = append(ls, choiceJSON{ID: l.ID, Name: textclean.Plain(item.Name, 64)})
	}
	tags, err := st.CatalogTags(ctx)
	if err != nil {
		p.fail(w, "listing tags", err)
		return
	}
	country, err := st.RatingsCountry(ctx)
	if err != nil {
		p.fail(w, "reading the ratings country", err)
		return
	}
	ratings := map[string][]string{}
	for _, kind := range []string{"movie", "series"} {
		scale, err := st.RatingScale(ctx, kind, country)
		if err != nil {
			p.fail(w, "reading the ratings", err)
			return
		}
		ratings[kind] = []string{}
		for _, l := range scale {
			if l.Certification != store.NotRated {
				ratings[kind] = append(ratings[kind], l.Certification)
			}
		}
	}
	shown, err := st.LibrariesShown(ctx)
	if err != nil {
		p.fail(w, "reading the site's menu", err)
		return
	}
	bases := []string{}
	for _, l := range offered {
		if b := base(l); !slices.Contains(bases, b) {
			bases = append(bases, b)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "librariesShown": shown, "languages": bases,
		"genres": gs, "originalLanguages": langs, "libraries": ls, "tags": orEmpty(tags), "ratings": ratings,
		"ratingsCountry": country})
}

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func base(lang string) string { return strings.SplitN(lang, "-", 2)[0] }

func (p *Panel) categoryCount(ctx context.Context, rules store.CategoryRules, self string) (int, error) {
	f, err := p.Store.FilterFor(ctx, rules, self)
	if err != nil {
		return 0, err
	}
	return p.Store.CountCatalog(ctx, store.ListQuery{Category: f})
}

type categoryInput struct {
	Names map[string]string   `json:"names"`
	Rules store.CategoryRules `json:"rules"`
	Home  bool                `json:"home"`
}

var (
	langCode  = regexp.MustCompile(`^[a-z]{2,3}$`)
	libraryID = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// readCategory reads and checks a category from a request (self is its ID,
// "" for a new one); it answers and returns false when it's wrong.
func (p *Panel) readCategory(w http.ResponseWriter, r *http.Request, self string) (categoryInput, bool) {
	var in categoryInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		invalid(w, "not a valid category")
		return in, false
	}
	named := false
	for lang, name := range in.Names {
		if !langCode.MatchString(lang) || textclean.Plain(name, 40) != name {
			invalid(w, "a name is plain text, 40 characters at most")
			return in, false
		}
		if name == "" {
			delete(in.Names, lang)
		} else {
			named = true
		}
	}
	if !named {
		invalid(w, "give it a name")
		return in, false
	}
	ru := &in.Rules
	switch {
	case len(ru.Kinds) > 2 || slices.ContainsFunc(ru.Kinds, func(k string) bool { return k != "movie" && k != "series" }):
		invalid(w, "films, series or both")
	case len(ru.Genres) > 30 || slices.ContainsFunc(ru.Genres, func(g int) bool { return g <= 0 }):
		invalid(w, "not valid genres")
	case len(ru.Languages) > 30 || slices.ContainsFunc(ru.Languages, func(l string) bool { return !langCode.MatchString(l) }):
		invalid(w, "not valid languages")
	case len(ru.Libraries) > 30 || slices.ContainsFunc(ru.Libraries, func(l string) bool { return !libraryID.MatchString(l) }):
		invalid(w, "not valid libraries")
	case len(ru.Tags) > 30 || slices.ContainsFunc(ru.Tags, func(t string) bool { return t == "" || textclean.Plain(t, 100) != t }):
		invalid(w, "a tag is plain text, 100 characters at most")
	case len(ru.Except) > 30 || slices.ContainsFunc(ru.Except, func(id string) bool { return !ids.Valid("cat", id) || id == self }):
		invalid(w, "not valid categories to leave out")
	default:
		for _, x := range ru.Except {
			if _, err := p.Store.CategoryByID(r.Context(), x); err != nil {
				invalid(w, "a category to leave out no longer exists")
				return in, false
			}
		}
		if !p.validCeilings(w, r, ru) {
			return in, false
		}
		return in, true
	}
	return in, false
}

// validCeilings checks that "rated at most" names ratings of the ratings
// country's scales.
func (p *Panel) validCeilings(w http.ResponseWriter, r *http.Request, ru *store.CategoryRules) bool {
	country, err := p.Store.RatingsCountry(r.Context())
	if err != nil {
		p.fail(w, "reading the ratings country", err)
		return false
	}
	for kind, c := range map[string]string{"movie": ru.MovieCeiling, "series": ru.SeriesCeiling} {
		if c == "" {
			continue
		}
		scale, err := p.Store.RatingScale(r.Context(), kind, country)
		if err != nil {
			p.fail(w, "reading the ratings", err)
			return false
		}
		if !slices.ContainsFunc(scale, func(l store.RatingLevel) bool { return l.Certification == c && c != store.NotRated }) {
			invalid(w, "not a rating of "+country+"'s")
			return false
		}
	}
	return true
}

func (p *Panel) createCategory(w http.ResponseWriter, r *http.Request, s *session) {
	in, ok := p.readCategory(w, r, "")
	if !ok {
		return
	}
	c := &store.Category{Names: in.Names, Rules: in.Rules, Home: in.Home}
	if err := p.Store.SaveCategory(r.Context(), c); err != nil {
		p.fail(w, "saving a category", err)
		return
	}
	p.Logger.Info("a category was made in the control panel", "category", c.ID, "by", s.logID)
	writeJSON(w, http.StatusCreated, map[string]string{"id": c.ID})
}

func (p *Panel) changeCategory(w http.ResponseWriter, r *http.Request, s *session) {
	id := r.PathValue("id")
	if !ids.Valid("cat", id) {
		http.Error(w, "No such category", http.StatusNotFound)
		return
	}
	in, ok := p.readCategory(w, r, id)
	if !ok {
		return
	}
	err := p.Store.SaveCategory(r.Context(), &store.Category{ID: id, Names: in.Names, Rules: in.Rules, Home: in.Home})
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "No such category", http.StatusNotFound)
		return
	} else if err != nil {
		p.fail(w, "saving a category", err)
		return
	}
	p.Logger.Info("a category was changed in the control panel", "category", id, "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}

func (p *Panel) deleteCategory(w http.ResponseWriter, r *http.Request, s *session) {
	id := r.PathValue("id")
	err := p.Store.DeleteCategory(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "No such category", http.StatusNotFound)
		return
	} else if err != nil {
		p.fail(w, "deleting a category", err)
		return
	}
	p.Logger.Info("a category was deleted in the control panel", "category", id, "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}

func (p *Panel) orderCategories(w http.ResponseWriter, r *http.Request, _ *session) {
	var req struct {
		IDs []string `json:"ids"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		invalid(w, "not a valid order")
		return
	}
	err := p.Store.OrderCategories(r.Context(), req.IDs)
	if errors.Is(err, store.ErrNotFound) {
		invalid(w, "give every category, once")
		return
	} else if err != nil {
		p.fail(w, "ordering categories", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Panel) setLibrariesShown(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		Shown bool `json:"shown"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		invalid(w, "not a valid setting")
		return
	}
	if err := p.Store.SetLibrariesShown(r.Context(), req.Shown); err != nil {
		p.fail(w, "saving the site's menu", err)
		return
	}
	p.Logger.Info("the site's menu changed in the control panel", "libraries", req.Shown, "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}

// previewCategory says how many titles a category being edited would
// have, and the first few, before it's saved.
func (p *Panel) previewCategory(w http.ResponseWriter, r *http.Request, _ *session) {
	var req struct {
		ID    string              `json:"id"`
		Rules store.CategoryRules `json:"rules"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		invalid(w, "not valid rules")
		return
	}
	ctx := r.Context()
	f, err := p.Store.FilterFor(ctx, req.Rules, req.ID)
	if err != nil {
		p.fail(w, "previewing a category", err)
		return
	}
	n, err := p.Store.CountCatalog(ctx, store.ListQuery{Category: f})
	if err != nil {
		p.fail(w, "previewing a category", err)
		return
	}
	sample, err := p.Store.CategorySample(ctx, f, 8)
	if err != nil {
		p.fail(w, "previewing a category", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": n, "sample": orEmpty(sample)})
}
