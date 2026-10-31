package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// The metadata editor: a film's or a series' texts and artwork as an
// admin changes them, over TMDB's, in each language offered (images in
// every language at once too). Overfin keeps them (store.SetTitleEdit);
// TMDB's data stays as it is beside them.

// metadataUploads is where images are uploaded for titles:
// /admin/api/metadata/uploads/{type}.
const metadataUploads = "/admin/api/metadata/uploads/"

// maxTitleUpload is the largest image taken for a title.
const maxTitleUpload = 10 << 20

// metadataTitle reads the kind and the TMDB ID in the path; false (and
// answered) when they aren't a title's.
func metadataTitle(w http.ResponseWriter, r *http.Request) (kind, id string, ok bool) {
	kind, id = r.PathValue("kind"), r.PathValue("tmdb")
	n, err := strconv.Atoi(id)
	if (kind != "movie" && kind != "series") || err != nil || !tmdb.ValidID(n) || strconv.Itoa(n) != id {
		notFound(w, "No such title")
		return "", "", false
	}
	return kind, id, true
}

type metadataTextJSON struct {
	Language       string `json:"language"`
	Default        bool   `json:"default"`
	Name           string `json:"name"`
	Overview       string `json:"overview"`
	TMDBName       string `json:"tmdbName"`
	TMDBOverview   string `json:"tmdbOverview"`
	NameEdited     bool   `json:"nameEdited"`
	OverviewEdited bool   `json:"overviewEdited"`
	// Untranslated: TMDB's texts in this language aren't kept (yet);
	// those shown are the default language's.
	Untranslated bool `json:"untranslated"`
}

// metadataImageJSON is an image as a language shows it: from TMDB, or as
// edited for it alone or for every language.
type metadataImageJSON struct {
	URL    string `json:"url,omitempty"` // a panel image URL; "" for none
	Edited string `json:"edited"`        // "", "language" or "all"
}

type metadataArtworkJSON struct {
	Language string                       `json:"language"` // a regional tag, or "*"
	Images   map[string]metadataImageJSON `json:"images"`   // poster, backdrop, logo
}

// metadata shows a film's or a series' texts and artwork in each language
// offered, as shown and as TMDB says.
func (p *Panel) metadata(w http.ResponseWriter, r *http.Request, _ *session) {
	kind, id, ok := metadataTitle(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	offered, def, _, err := p.Store.Languages(ctx)
	if err != nil {
		p.fail(w, "reading the languages", err)
		return
	}
	edits, err := p.Store.TitleEdits(ctx, kind, id)
	if err != nil {
		p.fail(w, "reading a title's edits", err)
		return
	}
	edited := map[string]bool{} // field + "/" + language
	all := map[string]string{}  // image type → the value for every language
	for _, e := range edits {
		edited[e.Field+"/"+e.Language] = true
		if e.Language == store.AllLanguages {
			all[e.Field] = e.Value
		}
	}
	key := kind + "/tmdb/" + id
	today := p.clock().Format(time.DateOnly)
	texts := []metadataTextJSON{}
	artwork := []metadataArtworkJSON{{Language: store.AllLanguages, Images: map[string]metadataImageJSON{}}}
	for _, typ := range store.ImageFields {
		im := metadataImageJSON{}
		if v := all[typ]; v != "" {
			im = metadataImageJSON{URL: panelImage(typ, v), Edited: "all"}
		}
		artwork[0].Images[typ] = im
	}
	var name string
	var year int
	for _, lang := range offered {
		shown, err := p.Store.TitleInfos(ctx, []string{key}, today, lang)
		if err != nil {
			p.fail(w, "reading a title", err)
			return
		}
		raw, err := p.Store.TMDBTitleInfos(ctx, []string{key}, today, lang)
		if err != nil {
			p.fail(w, "reading a title", err)
			return
		}
		t, tm := shown[key], raw[key]
		if t == nil || tm == nil || (!tm.Known && tm.Name == "") {
			notFound(w, "Overfin doesn't know this title yet")
			return
		}
		if lang == def || name == "" {
			name, year = t.Name, t.Year
		}
		texts = append(texts, metadataTextJSON{Language: lang, Default: lang == def, Name: t.Name, Overview: t.Overview,
			TMDBName: tm.Name, TMDBOverview: tm.Overview, NameEdited: edited["name/"+lang], OverviewEdited: edited["overview/"+lang],
			Untranslated: tm.Untranslated})
		a := metadataArtworkJSON{Language: lang, Images: map[string]metadataImageJSON{}}
		for _, typ := range store.ImageFields {
			im := metadataImageJSON{URL: panelImage(typ, map[string]string{"poster": t.Poster, "backdrop": t.Backdrop, "logo": t.Logo}[typ])}
			switch {
			case edited[typ+"/"+lang]:
				im.Edited = "language"
			case all[typ] != "":
				im.Edited = "all"
			}
			a.Images[typ] = im
		}
		artwork = append(artwork, a)
	}
	writeJSON(w, http.StatusOK, map[string]any{"kind": kind, "tmdb": id, "name": name, "year": year,
		"texts": texts, "artwork": artwork, "images": p.TitleImages != nil})
}

// panelImage is the panel's URL of a title's image: TMDB's or an upload;
// "" for none.
func panelImage(typ, value string) string {
	if id, ok := store.UploadID(value); ok {
		return "/admin/api/artwork/" + uploadImageID + id
	}
	if id := images.ArtworkID(typ, value); id != "" {
		return "/admin/api/artwork/" + id
	}
	return ""
}

// uploadImageID starts an upload's image ID in the panel.
const uploadImageID = "titleup_"

type metadataImageChoiceJSON struct {
	Value    string `json:"value"` // the TMDB path, to choose it
	URL      string `json:"url"`
	Language string `json:"language,omitempty"` // two letters; "" without text
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

// metadataImages lists the images TMDB has of a film or a series, in
// every language, to choose from.
func (p *Panel) metadataImages(w http.ResponseWriter, r *http.Request, _ *session) {
	kind, id, ok := metadataTitle(w, r)
	if !ok {
		return
	}
	found, ok := p.tmdbImages(w, r.Context(), kind, id)
	if !ok {
		return
	}
	out := map[string][]metadataImageChoiceJSON{}
	for typ, list := range found {
		out[typ] = []metadataImageChoiceJSON{}
		for _, im := range list {
			out[typ] = append(out[typ], metadataImageChoiceJSON{Value: im.Path, URL: panelImage(typ, im.Path),
				Language: im.Language, Width: im.Width, Height: im.Height})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ErrNoTMDB: TitleImages has no TMDB to ask (Overfin uses Seerr's, or
// none).
var ErrNoTMDB = errors.New("admin: no TMDB to ask")

// tmdbImages asks TMDB for a title's images; false (and answered) when it
// can't.
func (p *Panel) tmdbImages(w http.ResponseWriter, ctx context.Context, kind, id string) (map[string][]tmdb.Image, bool) {
	var found map[string][]tmdb.Image
	err := ErrNoTMDB
	if p.TitleImages != nil {
		n, _ := strconv.Atoi(id)
		found, err = p.TitleImages(ctx, kind, n)
	}
	if errors.Is(err, ErrNoTMDB) {
		writeJSON(w, http.StatusConflict, map[string]string{"type": "no-tmdb", "detail": "Overfin has no TMDB to ask for images"})
		return nil, false
	}
	if errors.Is(err, tmdb.ErrNotFound) {
		notFound(w, "TMDB doesn't know this title")
		return nil, false
	}
	if err != nil {
		p.Logger.Warn("asking TMDB for a title's images from the control panel failed", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"type": "upstream", "detail": "TMDB couldn't be asked for its images"})
		return nil, false
	}
	return found, true
}

// setMetadata changes a field of a film or a series in a language: a
// text, or an image (one of TMDB's for the title, or an upload).
func (p *Panel) setMetadata(w http.ResponseWriter, r *http.Request, s *session) {
	kind, id, ok := metadataTitle(w, r)
	if !ok {
		return
	}
	var req struct {
		Field    string `json:"field"`
		Language string `json:"language"`
		Value    string `json:"value"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "a field, a language and a value"})
		return
	}
	ctx := r.Context()
	if !p.offeredLanguage(w, ctx, req.Language, slices.Contains(store.ImageFields, req.Field)) {
		return
	}
	if slices.Contains(store.TextFields, req.Field) {
		req.Value = strings.TrimSpace(req.Value)
	}
	// A TMDB image must be one TMDB has of this title.
	if slices.Contains(store.ImageFields, req.Field) {
		if _, upload := store.UploadID(req.Value); !upload {
			found, ok := p.tmdbImages(w, ctx, kind, id)
			if !ok {
				return
			}
			if !slices.ContainsFunc(found[req.Field], func(im tmdb.Image) bool { return im.Path == req.Value }) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "That image isn't one of TMDB's for this title."})
				return
			}
		}
	}
	err := p.Store.SetTitleEdit(ctx, kind, id, req.Field, req.Language, req.Value, p.clock())
	if errors.Is(err, store.ErrInvalidEdit) {
		detail := "That can't be kept."
		switch req.Field {
		case "name":
			detail = fmt.Sprintf("A name needs 1 to %d characters.", store.MaxEditedName)
		case "overview":
			detail = fmt.Sprintf("A synopsis needs 1 to %d characters.", store.MaxEditedOverview)
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": detail})
		return
	}
	if err != nil {
		p.fail(w, "editing a title", err)
		return
	}
	p.Logger.Info("a title was edited in the control panel", "kind", kind, "tmdb", id, "field", req.Field, "language", req.Language, "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}

// unsetMetadata gives a field of a film or a series back to TMDB in a
// language.
func (p *Panel) unsetMetadata(w http.ResponseWriter, r *http.Request, s *session) {
	kind, id, ok := metadataTitle(w, r)
	if !ok {
		return
	}
	field, lang := r.PathValue("field"), r.PathValue("language")
	switch err := p.Store.DeleteTitleEdit(r.Context(), kind, id, field, lang); {
	case errors.Is(err, store.ErrNotFound):
		notFound(w, "That wasn't changed")
		return
	case err != nil:
		p.fail(w, "editing a title", err)
		return
	}
	p.Logger.Info("a title's field went back to TMDB's in the control panel", "kind", kind, "tmdb", id, "field", field, "language", lang, "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}

// offeredLanguage says whether lang is one the server offers (or, for an
// image, every language); false (and answered) when it isn't.
func (p *Panel) offeredLanguage(w http.ResponseWriter, ctx context.Context, lang string, image bool) bool {
	if image && lang == store.AllLanguages {
		return true
	}
	offered, _, _, err := p.Store.Languages(ctx)
	if err != nil {
		p.fail(w, "reading the languages", err)
		return false
	}
	if !slices.Contains(offered, lang) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "That language isn't offered."})
		return false
	}
	return true
}

// uploadMetadataImage keeps an image for a title's poster, backdrop or
// logo, re-encoded (posters and backdrops as JPEG, logos as PNG, for
// their transparency), to be chosen next (setMetadata).
func (p *Panel) uploadMetadataImage(w http.ResponseWriter, r *http.Request, s *session) {
	typ := r.PathValue("type")
	if !slices.Contains(store.ImageFields, typ) {
		notFound(w, "No such image type")
		return
	}
	raw, err := io.ReadAll(r.Body)
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"type": "invalid-request",
			"detail": fmt.Sprintf("An image is %d MB at most.", maxTitleUpload>>20)})
		return
	case err != nil:
		return
	}
	contentType := "image/jpeg"
	if typ == "logo" {
		contentType = "image/png"
	}
	data, err := images.Reencode(raw, contentType)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request",
			"detail": fmt.Sprintf("That isn't a JPEG or a PNG image, or it's larger than %d pixels a side.", images.MaxDimension)})
		return
	}
	b := make([]byte, 16)
	rand.Read(b)
	id := hex.EncodeToString(b)
	if err := p.Store.PutTitleUpload(r.Context(), id, store.TitleUpload{Type: typ, ContentType: contentType, Data: data}, p.clock()); err != nil {
		p.fail(w, "keeping an uploaded image", err)
		return
	}
	p.Logger.Info("an image was uploaded for a title in the control panel", "type", typ, "bytes", len(data), "by", s.logID)
	value := store.UploadValue(id)
	writeJSON(w, http.StatusCreated, map[string]string{"value": value, "url": panelImage(typ, value)})
}

// artwork serves a title's image to the panel: one of TMDB's (a poster,
// backdrop or logo), or an upload, through Overfin, as everywhere
// (SECURITY.md rule 43).
func (p *Panel) artwork(w http.ResponseWriter, r *http.Request, _ *session) {
	id := r.PathValue("id")
	var data []byte
	var contentType string
	if upload, ok := strings.CutPrefix(id, uploadImageID); ok {
		u, found, err := p.Store.TitleUploadOf(r.Context(), upload)
		if err != nil {
			p.fail(w, "reading an uploaded image", err)
			return
		}
		if !found {
			notFound(w, "No such image")
			return
		}
		data, contentType = u.Data, u.ContentType
	} else {
		ref, err := images.ParseArtwork(id)
		if err != nil || !slices.Contains(store.ImageFields, ref.Type) || p.Images == nil {
			notFound(w, "No such image")
			return
		}
		size := "small"
		if ref.Type == "backdrop" {
			size = "medium"
		}
		if data, contentType, err = p.Images.TMDB(r.Context(), ref, size, func(context.Context) error { return nil }); err != nil {
			notFound(w, "No such image")
			return
		}
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	// Shown as an image only.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	_, _ = w.Write(data)
}
