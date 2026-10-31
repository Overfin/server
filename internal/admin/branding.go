package admin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // branding files kept before as JPEGs
	_ "image/png"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/store"
)

// The operator's branding: a switch, their site's name, and their logo and
// banners: PNG or JPEG pictures (not SVG, which emails can't show), made
// Overfin's like every image (images.Prepare): checked, no wider than
// they're ever shown, kept as PNG for their transparency. The control
// panel itself always stays Overfin's.

// brandingFiles is where branding files are uploaded, shown and removed:
// /admin/api/branding/files/{kind}.
const brandingFiles = "/admin/api/branding/files/"

// maxBrandingFile is the largest branding file taken.
const maxBrandingFile = 4 << 20

// brandingWidths are the widest each branding file is kept at: as wide as
// the site's header and the emails ever show it, on sharp screens.
var brandingWidths = map[string]int{"logo": 512, "banner-dark": 1200, "banner-light": 1200}

// maxBrandName is the longest name, in characters.
const maxBrandName = 40

type brandingState struct {
	On   bool   `json:"on"`
	Name string `json:"name"`
	// Files are the uploaded files' addresses here, by kind.
	Files map[string]string `json:"files"`
}

func (p *Panel) brandingState(r *http.Request) (brandingState, error) {
	br, err := p.Store.Branding(r.Context())
	if err != nil {
		return brandingState{}, err
	}
	out := brandingState{On: br.On, Name: br.Name, Files: map[string]string{}}
	for kind, at := range br.Files {
		out.Files[kind] = fmt.Sprintf("%s%s?v=%d", brandingFiles, kind, at.Unix())
	}
	return out, nil
}

func (p *Panel) branding(w http.ResponseWriter, r *http.Request, s *session) {
	out, err := p.brandingState(r)
	if err != nil {
		p.fail(w, "reading the branding", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// brandName checks a site's name: plain text, one line, not too long.
func brandName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > maxBrandName {
		return "", Problem(fmt.Sprintf("The name is %d characters at most.", maxBrandName))
	}
	if strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || r == '<' || r == '>' }) {
		return "", Problem("The name is plain text, on one line.")
	}
	return name, nil
}

func (p *Panel) setBranding(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		On   bool   `json:"on"`
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	name, err := brandName(req.Name)
	if err == nil && req.On && name == "" {
		err = Problem("Give your site's name to use your own branding.")
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": err.Error()})
		return
	}
	if err := p.Store.SetBranding(r.Context(), req.On, name); err != nil {
		p.fail(w, "saving the branding", err)
		return
	}
	p.Logger.Info("branding changed in the control panel", "on", req.On, "by", s.logID)
	p.branding(w, r, s)
}

// brandingKind is the file kind a request names, or "" after answering
// 404.
func brandingKind(w http.ResponseWriter, r *http.Request) string {
	kind := r.PathValue("kind")
	if !slices.Contains(store.BrandingKinds, kind) {
		http.NotFound(w, r)
		return ""
	}
	return kind
}

// uploadBrandingFile keeps a picture, made Overfin's (images.Prepare).
func (p *Panel) uploadBrandingFile(w http.ResponseWriter, r *http.Request, s *session) {
	kind := brandingKind(w, r)
	if kind == "" {
		return
	}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	data, err := io.ReadAll(r.Body)
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"type": "invalid-request",
			"detail": fmt.Sprintf("A file is %d MB at most.", maxBrandingFile>>20)})
		return
	case err != nil:
		return
	case len(data) == 0:
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "That file is empty."})
		return
	case mt == "image/svg+xml" || isSVG(data):
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request",
			"detail": "SVG isn't taken: emails can't show it. Use a PNG."})
		return
	}
	data, err = images.Prepare(data, brandingWidths[kind], "image/png")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request",
			"detail": fmt.Sprintf("That isn't a PNG or a JPEG picture, or it's larger than %d pixels a side.", images.MaxDimension)})
		return
	}
	if err := p.Store.SetBrandingFile(r.Context(), kind, "image/png", data, time.Now()); err != nil {
		p.fail(w, "saving a branding file", err)
		return
	}
	p.Logger.Info("a branding file was uploaded in the control panel", "kind", kind, "type", mt, "bytes", len(data), "by", s.logID)
	p.branding(w, r, s)
}

// isSVG says whether a file is an SVG, whatever type it was sent as.
func isSVG(data []byte) bool {
	head := strings.ToLower(string(data[:min(len(data), 512)]))
	return strings.Contains(head, "<svg")
}

// brandingFile shows a file here, for the page's previews.
func (p *Panel) brandingFile(w http.ResponseWriter, r *http.Request, s *session) {
	kind := brandingKind(w, r)
	if kind == "" {
		return
	}
	f, ok, err := p.Store.BrandingFileOf(r.Context(), kind)
	if err != nil {
		p.fail(w, "reading a branding file", err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", f.ContentType)
	h.Set("Cache-Control", "no-cache")
	// Shown as an image only: nothing in it ever runs.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	_, _ = w.Write(f.Data)
}

func (p *Panel) removeBrandingFile(w http.ResponseWriter, r *http.Request, s *session) {
	kind := brandingKind(w, r)
	if kind == "" {
		return
	}
	if err := p.Store.DeleteBrandingFile(r.Context(), kind); err != nil {
		p.fail(w, "removing a branding file", err)
		return
	}
	p.Logger.Info("a branding file was removed in the control panel", "kind", kind, "by", s.logID)
	p.branding(w, r, s)
}

// PrepareBranding makes the branding files kept before Overfin prepared
// them (as they were uploaded) like those uploaded since: PNG, no wider
// than brandingWidths. One it can't read (WebP, GIF…) is removed, and
// said in the log: the operator uploads it again as a PNG.
func PrepareBranding(ctx context.Context, st *store.Store, logger *slog.Logger) error {
	for _, kind := range store.BrandingKinds {
		f, ok, err := st.BrandingFileOf(ctx, kind)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(f.Data))
		if err == nil && format == "png" && f.ContentType == "image/png" && cfg.Width <= brandingWidths[kind] {
			continue // prepared already
		}
		data, err := images.Prepare(f.Data, brandingWidths[kind], "image/png")
		if err != nil {
			logger.Warn("a branding file Overfin can't read was removed: upload it again as a PNG", "kind", kind, "type", f.ContentType)
			if err := st.DeleteBrandingFile(ctx, kind); err != nil {
				return err
			}
			continue
		}
		if err := st.SetBrandingFile(ctx, kind, "image/png", data, time.Now()); err != nil {
			return err
		}
		logger.Info("a branding file was made a PNG of its shown width", "kind", kind)
	}
	return nil
}
