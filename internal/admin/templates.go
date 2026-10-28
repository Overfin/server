package admin

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/textclean"
)

// Account templates: what accounts may do. Accounts follow theirs live, so
// editing a template changes every account on it at once. Default exists
// from the start, so accounts can be opened right away; it's edited like any
// other, and only can't be deleted.

type templateJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MaxStreams  int    `json:"maxStreams"`
	MaxProfiles int    `json:"maxProfiles"`
	// ReserveStreams: each profile owns one of the account's streams.
	ReserveStreams bool `json:"reserveStreams"`
	// Categories, when present, are the only categories its accounts'
	// profiles see.
	Categories *[]string `json:"categories,omitempty"`
	Accounts   int       `json:"accounts"`
	Default    bool      `json:"default"`
}

func toTemplateJSON(t *store.Template) templateJSON {
	out := templateJSON{ID: t.ID, Name: t.Name, MaxStreams: t.MaxStreams, MaxProfiles: t.MaxProfiles,
		ReserveStreams: t.ReserveStreams, Accounts: t.Accounts, Default: t.ID == store.DefaultTemplate}
	if t.Categories != nil {
		cats := t.Categories
		out.Categories = &cats
	}
	return out
}

func (p *Panel) templates(w http.ResponseWriter, r *http.Request, _ *session) {
	all, err := p.Store.Templates(r.Context())
	if err != nil {
		p.fail(w, "listing templates", err)
		return
	}
	out := []templateJSON{}
	for _, t := range all {
		out = append(out, toTemplateJSON(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type templateInput struct {
	Name        string `json:"name"`
	MaxStreams  int    `json:"maxStreams"`
	MaxProfiles int    `json:"maxProfiles"`
	// ReserveStreams is off when absent.
	ReserveStreams bool `json:"reserveStreams"`
	// Categories limits its accounts' profiles to some; null or absent, no
	// limit.
	Categories *[]string `json:"categories"`
}

// readTemplate decodes and checks a template; it writes the problem and
// returns false when it's not acceptable.
func (p *Panel) readTemplate(w http.ResponseWriter, r *http.Request) (templateInput, bool) {
	var in templateInput
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(&in)
	name, ok := textclean.Name(in.Name, 40)
	in.Name = name
	switch {
	case err != nil:
		invalid(w, "not a valid template")
	case name == "" || !ok:
		invalid(w, "a template's name is plain text, 1 to 40 characters")
	case in.MaxStreams < 1 || in.MaxStreams > 100:
		invalid(w, "streams at once: 1 to 100")
	case in.MaxProfiles < 1 || in.MaxProfiles > 20:
		invalid(w, "profiles: 1 to 20, the owner's included")
	case in.ReserveStreams && in.MaxProfiles > in.MaxStreams:
		invalid(w, "with a stream reserved for each profile, no more profiles than streams")
	case in.Categories != nil && len(*in.Categories) > 50:
		invalid(w, "too many categories")
	default:
		if in.Categories != nil {
			for _, id := range *in.Categories {
				if _, err := p.Store.CategoryByID(r.Context(), id); err != nil {
					invalid(w, "a category no longer exists")
					return in, false
				}
			}
		}
		return in, true
	}
	return in, false
}

func (p *Panel) createTemplate(w http.ResponseWriter, r *http.Request, _ *session) {
	in, ok := p.readTemplate(w, r)
	if !ok {
		return
	}
	t, err := p.Store.CreateTemplate(r.Context(), in.Name, in.MaxStreams, in.MaxProfiles, in.ReserveStreams)
	if errors.Is(err, store.ErrTemplateNameTaken) {
		invalid(w, "another template has that name")
		return
	} else if err != nil {
		p.fail(w, "creating a template", err)
		return
	}
	if in.Categories != nil {
		if err := p.Store.SetTemplateCategories(r.Context(), t.ID, *in.Categories); err != nil {
			p.fail(w, "creating a template", err)
			return
		}
		t.Categories = *in.Categories
	}
	writeJSON(w, http.StatusCreated, toTemplateJSON(t))
}

func (p *Panel) updateTemplate(w http.ResponseWriter, r *http.Request, _ *session) {
	in, ok := p.readTemplate(w, r)
	if !ok {
		return
	}
	var cats []string
	if in.Categories != nil {
		cats = *in.Categories
	}
	err := p.Store.UpdateTemplate(r.Context(), r.PathValue("id"), in.Name, in.MaxStreams, in.MaxProfiles, in.ReserveStreams)
	if err == nil {
		err = p.Store.SetTemplateCategories(r.Context(), r.PathValue("id"), cats)
	}
	switch {
	case errors.Is(err, store.ErrTemplateNameTaken):
		invalid(w, "another template has that name")
	case errors.Is(err, store.ErrNotFound):
		notFound(w, "No such template")
	case err != nil:
		p.fail(w, "changing a template", err)
	default:
		p.accessChanged()
		p.Logger.Info("account template changed in the control panel", "template", r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	}
}

func (p *Panel) deleteTemplate(w http.ResponseWriter, r *http.Request, _ *session) {
	switch err := p.Store.DeleteTemplate(r.Context(), r.PathValue("id")); {
	case errors.Is(err, store.ErrTemplateInUse):
		invalid(w, "Default can't be deleted, nor a template accounts still follow: move them first")
	case errors.Is(err, store.ErrNotFound):
		notFound(w, "No such template")
	case err != nil:
		p.fail(w, "deleting a template", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// accessChanged makes images follow at once a change that limits what
// profiles see: what was remembered of their access is forgotten.
func (p *Panel) accessChanged() {
	if p.Images != nil {
		p.Images.ForgetAll()
	}
}
