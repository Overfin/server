package gateway

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strings"

	"github.com/overfin/server/internal/link"
)

// The gateway's own pages, when the backend can't answer: being updated
// (the backend runs another version, or says it's restarting), or out of
// reach. In English and French, as the visitor's browser asks; they never
// name what's behind the server. API requests get JSON.

type downText struct{ Title, Body, Retry string }

var downTexts = map[string]map[string]downText{
	"updating": {
		"en": {"Being updated", "This server is being updated. It will be back in a moment.", "This page tries again by itself."},
		"fr": {"Mise à jour en cours", "Ce serveur est en cours de mise à jour. Il revient dans un instant.", "Cette page réessaie d’elle-même."},
	},
	"unreachable": {
		"en": {"Out of reach", "This server can't be reached right now. Try again in a moment.", "This page tries again by itself."},
		"fr": {"Hors d’atteinte", "Ce serveur ne répond pas pour le moment. Réessayez dans un instant.", "Cette page réessaie d’elle-même."},
	},
}

var downPage = template.Must(template.New("down").Parse(`<!doctype html>
<html lang="{{.Lang}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="20"><title>{{.Title}}</title>
<style>
body{margin:0;min-height:100vh;display:grid;place-items:center;font-family:system-ui,sans-serif;background:#f4f5f8;color:#1c1d22}
@media (prefers-color-scheme:dark){body{background:#16171b;color:#e8e9ee}main{background:#202228}}
main{max-width:min(28rem,calc(100vw - 2rem));padding:1.6rem;border-radius:12px;background:#fff;box-shadow:0 1px 3px #0002}
h1{margin:0 0 .6rem;font-size:1.3rem}p{margin:.4rem 0;line-height:1.5}small{opacity:.7}
</style></head><body><main><h1>{{.Title}}</h1><p>{{.Body}}</p><p><small>{{.Retry}}</small></p></main></body></html>`))

// down answers a request the backend couldn't: the page (or JSON) saying
// why, 503, try again soon.
func down(w http.ResponseWriter, r *http.Request, st link.Status, version string) {
	kind := "unreachable"
	if st.Mismatch != "" {
		kind = "updating"
	}
	w.Header().Set("Retry-After", "20")
	w.Header().Set("Cache-Control", "no-store")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "server-" + kind, "title": downTexts[kind]["en"].Title, "status": 503})
		return
	}
	lang := "en"
	if accept := r.Header.Get("Accept-Language"); strings.HasPrefix(strings.ToLower(strings.TrimSpace(accept)), "fr") {
		lang = "fr"
	}
	t := downTexts[kind][lang]
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = downPage.Execute(w, struct {
		Lang string
		downText
	}{lang, t})
}
