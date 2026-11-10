package api

import (
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/overfin/server/internal/cookie"
	"github.com/overfin/server/internal/password"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/proof"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/theme"
)

// Overfin's own pages: signing in (/signin), an invitation
// (/invite/{code}), a forgotten password (/reset). One template, its
// stylesheet and its script, in the site's brand.

const (
	pagesJS        = "/api/v1/pages/page.js"
	pagesCSS       = "/api/v1/pages/page.css"
	pagesLogo      = "/api/v1/pages/logo.svg"
	challengePath  = "/api/v1/pages/challenge"
	invitePage     = "/invite/"
	signinPagePath = "/signin"
	resetPagePath  = "/reset"
	setupPagePath  = "/setup"
)

//go:embed pages
var pageFiles embed.FS

var pageTemplate = template.Must(template.ParseFS(pageFiles, "pages/page.html"))

// Pages serves the pages' files and their check against scripts, whatever
// the sign-in.
type Pages struct {
	Proof *proof.Checker
}

func (Pages) Name() string { return "pages" }

func (p Pages) Register(r *plugin.Registrar) error {
	for pattern, h := range map[string]http.HandlerFunc{
		"GET " + pagesJS:       pageFile("pages/page.js", "text/javascript; charset=utf-8"),
		"GET " + pagesCSS:      pageFile("pages/page.css", "text/css; charset=utf-8"),
		"GET " + pagesLogo:     pageFile("pages/logo.svg", "image/svg+xml"),
		"GET " + challengePath: p.challenge,
	} {
		if err := r.HandlePublic(pattern, h); err != nil {
			return err
		}
	}
	return nil
}

func pageFile(name, contentType string) http.HandlerFunc {
	b, err := pageFiles.ReadFile(name)
	if err != nil {
		panic(err) // embedded: present at build time
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	}
}

// challenge is a new check for the page's next form.
func (p Pages) challenge(w http.ResponseWriter, r *http.Request) {
	writeOK(w, map[string]any{"challenge": p.Proof.Challenge(), "bits": proof.Bits})
}

// page is one of the pages to show: which (Mode), and its own values.
type page struct {
	// Mode is "signin", "fresh", "change", "verify", "reset", "reset-new",
	// "invite" (registering), "invited" (with an identity provider), "link"
	// (signing a screen in), "setup" (an account's first password),
	// "confirm" (an email's link confirming an address) or
	// "again" (opening the same page again, to carry the login cookie: its
	// Return is where).
	Mode string
	// Return is where the person goes once signed in.
	Return string
	// Email is the signed-in person's (to confirm it's them).
	Email string
	// Mail says whether the server sends emails (a forgotten password).
	Mail bool
	// Valid says whether the invitation works; Continue is where it leads
	// with an identity provider.
	Valid    bool
	Continue string
	// Rules are what a new password must have.
	Rules password.Rules
}

// againParam marks a page asked again by itself, to carry the login
// cookie (againWithCookie).
const againParam = "again"

// againWithCookie answers a page opened from elsewhere without the login
// cookie with a page that opens it again, from here. The cookie is
// SameSite=Strict: a link from another site (a QR code scanned, a chat, an
// app) doesn't carry it, so someone signed in would look signed out. Asked
// again from the page itself, the same address (the fragment kept, with
// its code) carries it. Only once: the second time is marked, so it never
// loops; and a request from this site itself, with no cookie, has none to
// carry.
func againWithCookie(w http.ResponseWriter, r *http.Request, st *store.Store, logger *slog.Logger) bool {
	if _, err := cookie.Get(r, LoginCookie); err == nil {
		return false
	}
	// Only when the browser says it came from elsewhere ("cross-site"), or
	// from outside any page ("none": an app, a scanned code; some browsers
	// treat those as cross-site for cookies). A browser that says nothing
	// is left as it was.
	from := r.Header.Get("Sec-Fetch-Site")
	if r.URL.Query().Get(againParam) != "" || (from != "cross-site" && from != "none") {
		return false
	}
	q := r.URL.Query()
	q.Set(againParam, "1")
	renderPage(w, r, st, logger, page{Mode: "again", Return: r.URL.Path + "?" + q.Encode()})
	return true
}

// renderPage shows one of the pages, in the visitor's language and the
// site's brand.
func renderPage(w http.ResponseWriter, r *http.Request, st *store.Store, logger *slog.Logger, p page) {
	lang := pageLang(r, st)
	t := signinTexts[lang]
	data := map[string]any{
		"Lang": lang, "T": t, "P": p, "JS": pagesJS, "CSS": pagesCSS, "Icon": pagesLogo,
		"Banner": template.HTML(theme.Banner), "Name": "Overfin",
		"Rules": rulesText(t, p.Rules), "Min": p.Rules.MinLength,
	}
	if br, err := st.Branding(r.Context()); err == nil && br.On {
		data["Custom"], data["Name"] = true, BrandName(br)
		file := func(kind string) string {
			if at, ok := br.Files[kind]; ok {
				return fmt.Sprintf("/api/v1/branding/%s?v=%d", kind, at.Unix())
			}
			return ""
		}
		data["BannerDark"], data["BannerLight"] = file("banner-dark"), file("banner-light")
		if logo := file("logo"); logo != "" {
			data["Icon"] = logo
		}
	}
	data["InviteTitle"] = strings.ReplaceAll(t["inviteTitle"], "{site}", data["Name"].(string))
	data["FreshIntro"] = strings.ReplaceAll(t["freshIntro"], "{email}", p.Email)
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; "+
		"form-action 'none'; frame-ancestors 'none'; base-uri 'none'")
	if err := pageTemplate.Execute(w, data); err != nil {
		logger.Error("a sign-in page failed", "err", err)
	}
}

// rulesText says what a new password needs, in the page's language.
func rulesText(t map[string]string, r password.Rules) string {
	s := strings.ReplaceAll(t["rules"], "{n}", strconv.Itoa(r.MinLength))
	var with []string
	if r.Digit {
		with = append(with, t["rulesDigit"])
	}
	if r.Special {
		with = append(with, t["rulesSpecial"])
	}
	if len(with) > 0 {
		s += t["rulesWith"] + strings.Join(with, t["rulesAnd"])
	}
	return s + "."
}
