package admin

import (
	"context"
	"crypto/subtle"
	"html/template"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/overfin/server/internal/password"
)

// The control panel's own login, on the backend: one account (username and
// password), made in the setup wizard, nothing in common with the site's
// accounts. A stolen site account never opens the panel.

// Failed tries: per address, and for everyone together (a LAN can hold many
// addresses), within localWindow.
const (
	localWindow      = 15 * time.Minute
	localPerAddress  = 5
	localEveryone    = 20
	maxUsernameBytes = 64
)

// PanelAccountFunc reads the panel's login: its username and its
// password's hash; ok is false while none was made.
type PanelAccountFunc func(ctx context.Context) (username, hash string, ok bool, err error)

type localTries struct {
	mu    sync.Mutex
	by    map[netip.Addr][]time.Time
	every []time.Time
}

func recent(ts []time.Time, now time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < localWindow {
			out = append(out, t)
		}
	}
	return out
}

// allowed says whether addr may try again now.
func (l *localTries) allowed(addr netip.Addr, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.by == nil {
		l.by = map[netip.Addr][]time.Time{}
	}
	l.by[addr] = recent(l.by[addr], now)
	l.every = recent(l.every, now)
	return len(l.by[addr]) < localPerAddress && len(l.every) < localEveryone
}

func (l *localTries) failed(addr netip.Addr, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.by[addr] = append(l.by[addr], now)
	l.every = append(l.every, now)
}

var localPage = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Control panel</title><link rel="stylesheet" href="/admin/theme.css">
<link rel="stylesheet" href="/admin/login.css"></head><body>
<form method="post" action="/admin/login">
<h1>Control panel</h1>
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
<label>Username<input name="username" autocomplete="username" spellcheck="false" required autofocus value="{{.Username}}"></label>
<label>Password<input name="password" type="password" autocomplete="current-password" required></label>
<input type="hidden" name="return" value="{{.Return}}">
<button type="submit">Sign in</button>
</form></body></html>`))

// localCSS is the sign-in form's look (the panel's policy allows no
// inline style).
const localCSS = `body{margin:0;min-height:100vh;display:grid;place-items:center;font-family:system-ui,sans-serif;background:var(--bg,#f4f5f8);color:var(--text,#1c1d22)}
form{width:min(22rem,calc(100vw - 2rem));display:grid;gap:.9rem;padding:1.5rem;border-radius:12px;background:var(--panel,#fff);border:1px solid var(--border,#e2e5eb);box-shadow:var(--shadow-s,0 1px 3px #0002)}
h1{margin:0 0 .3rem;font-size:1.3rem}label{display:grid;gap:.3rem;font-size:.95rem}
input{font:inherit;color:inherit;background:var(--bg,#fff);padding:.55rem .7rem;border:1px solid var(--border-strong,#8884);border-radius:8px}
button{font:inherit;font-weight:600;padding:.6rem;border:0;border-radius:8px;background:var(--accent,#3b6fd8);color:var(--on-accent,#fff);cursor:pointer}
.error{margin:0;color:var(--danger,#c0392b)}
`

func serveLocalCSS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write([]byte(localCSS))
}

type localForm struct{ Error, Username, Return string }

func showLocal(w http.ResponseWriter, status int, f localForm) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = localPage.Execute(w, f)
}

// localLogin shows the panel's sign-in form.
func (p *Panel) localLogin(w http.ResponseWriter, r *http.Request) {
	showLocal(w, http.StatusOK, localForm{Return: safeReturn(r.URL.Query().Get("return"))})
}

// localSignIn checks the panel's login and starts a panel session.
func (p *Panel) localSignIn(w http.ResponseWriter, r *http.Request) {
	if p.PanelAccount == nil {
		http.NotFound(w, r)
		return
	}
	// Only from the panel's own page: another site's origin is refused
	// (sameOrigin also checks Sec-Fetch-Site). The panel's no-referrer
	// policy has browsers send "null" for its own form.
	if o := r.Header.Get("Origin"); o != "" && o != "null" && !sameHost(o, r.Host) {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "That request isn't valid", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(r.PostForm.Get("username"))
	pw := r.PostForm.Get("password")
	ret := safeReturn(r.PostForm.Get("return"))
	if len(username) > maxUsernameBytes {
		username = username[:maxUsernameBytes]
	}
	addr := clientAddr(r)
	now := p.clock()
	if !p.localTries.allowed(addr, now) {
		p.Logger.Warn("control panel sign-in: too many tries", "from", addr.String())
		showLocal(w, http.StatusTooManyRequests, localForm{Error: "Too many tries. Wait 15 minutes, then try again.", Username: username, Return: ret})
		return
	}
	want, hash, ok, err := p.PanelAccount(r.Context())
	if err != nil {
		p.Logger.Error("control panel sign-in: reading its login", "err", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}
	// The password is checked even for a wrong username, so the time taken
	// doesn't tell which was wrong.
	good := ok && password.Check(hash, pw)
	if !ok || subtle.ConstantTimeCompare([]byte(username), []byte(want)) != 1 || !good {
		p.localTries.failed(addr, now)
		p.Logger.Warn("control panel sign-in: wrong username or password", "from", addr.String())
		showLocal(w, http.StatusUnauthorized, localForm{Error: "Wrong username or password.", Username: username, Return: ret})
		return
	}
	p.startSession(w, r, username, "")
	http.Redirect(w, r, ret, http.StatusSeeOther)
}

// clientAddr is the request's address, unmapped.
func clientAddr(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}

// sameHost says whether origin (scheme://host[:port]) is this request's host.
func sameHost(origin, host string) bool {
	_, rest, ok := strings.Cut(origin, "://")
	return ok && strings.EqualFold(rest, host)
}
