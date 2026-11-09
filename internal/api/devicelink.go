package api

import (
	"crypto/rand"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/overfin/server/internal/cookie"
	"github.com/overfin/server/internal/identity"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/qr"
	"github.com/overfin/server/internal/store"
)

// Signing a screen in from another device (rule 69): a TV's sign-in page
// asks for a code and shows it, with a QR code of the address to confirm
// it at; someone signed in elsewhere opens that address (/link), types or
// scans the code, sees what confirming does, and confirms; the screen,
// asking with the secret only it holds, then gets a login of its own.

const (
	linkPagePath = "/link"
	// DeviceCookie holds a screen's secret while its code waits.
	DeviceCookie = "overfin-device"
	// deviceCodeFor is how long a code can be confirmed.
	deviceCodeFor = 10 * time.Minute
	// codeLetters are the codes' letters: no vowels (no words) and nothing
	// that looks like a digit; 8 of them is about 2³⁵ codes.
	codeLetters = "BCDFGHJKLMNPQRSTVWXZ"
	codeLength  = 8
	// wrongCodes caps wrong codes per person, and per address, a quarter
	// hour: with codes this many and this short-lived, guessing one is out
	// of reach.
	wrongCodes = 10
)

// newDeviceCode is codeLength letters, picked without bias.
func newDeviceCode() string {
	b := make([]byte, codeLength)
	for i := range b {
		for {
			var r [1]byte
			rand.Read(r[:])
			// The largest multiple of 20 under 256 is 240.
			if r[0] < 240 {
				b[i] = codeLetters[int(r[0])%len(codeLetters)]
				break
			}
		}
	}
	return string(b)
}

// cleanCode is a typed code without spaces, dashes or case; "" if it isn't
// one.
func cleanCode(s string) string {
	var b strings.Builder
	for _, c := range strings.ToUpper(s) {
		if c == ' ' || c == '-' {
			continue
		}
		if !strings.ContainsRune(codeLetters, c) {
			return ""
		}
		b.WriteRune(c)
	}
	if b.Len() != codeLength {
		return ""
	}
	return b.String()
}

// siteAddress is the site's public address: the configured one, else the
// one this request came to.
func (m *Managed) siteAddress(r *http.Request) string {
	if m.SiteURL != "" {
		return strings.TrimRight(m.SiteURL, "/")
	}
	scheme := "http"
	if cookie.Secure(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// deviceStart gives a screen a new code, its address and its QR code, and
// the screen a secret, in a cookie, to collect the sign-in with.
func (m *Managed) deviceStart(w http.ResponseWriter, r *http.Request) {
	if !m.deviceStarts.allow(clientFrom(identity.ClientIP(r.Context())), time.Now()) {
		m.refuse(w, r, problem.RateLimited, "tooManyTries")
		return
	}
	// A form anyone can send: with its check against scripts, as the others
	// (rule 66), as each code is a row.
	req, ok := m.read(w, r)
	if !ok || !m.checked(w, r, req) {
		return
	}
	code, secret := newDeviceCode(), randomToken()
	if err := m.Store.CreateDeviceCode(r.Context(), code, secret, deviceName(r.UserAgent()), time.Now().Add(deviceCodeFor)); err != nil {
		m.fail(w, r, err)
		return
	}
	// The code in the address's fragment: it never reaches a server's logs.
	address := m.siteAddress(r) + linkPagePath
	pic, err := qr.Encode(address + "#" + code)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	cookie.Set(w, r, &http.Cookie{Name: DeviceCookie, Value: secret, MaxAge: int(deviceCodeFor.Seconds())})
	writeOK(w, map[string]any{
		"code":      code[:4] + "-" + code[4:],
		"address":   strings.TrimPrefix(strings.TrimPrefix(address, "https://"), "http://"),
		"qr":        map[string]any{"size": pic.Size + 8, "path": pic.Path()},
		"expiresIn": int(deviceCodeFor.Seconds()),
	})
}

// deviceCheck is the screen asking whether its code was confirmed: once it
// is, the screen is signed in (a login of its own, not a fresh one: nobody
// typed a password on it).
func (m *Managed) deviceCheck(w http.ResponseWriter, r *http.Request) {
	if !m.deviceChecks.allow(clientFrom(identity.ClientIP(r.Context())), time.Now()) {
		m.refuse(w, r, problem.RateLimited, "tooManyTries")
		return
	}
	var req struct {
		Return string `json:"return"`
	}
	c, err := cookie.Get(r, DeviceCookie)
	if !decode(r, &req) || err != nil || len(c.Value) > 64 {
		m.refuse(w, r, problem.NotFound, "codeExpired")
		return
	}
	ctx := r.Context()
	subject, err := m.Store.CollectDeviceCode(ctx, c.Value)
	switch {
	case errors.Is(err, store.ErrNotFound):
		cookie.Clear(w, r, DeviceCookie)
		m.refuse(w, r, problem.NotFound, "codeExpired")
		return
	case err != nil:
		m.fail(w, r, err)
		return
	case subject == "":
		writeOK(w, map[string]bool{"waiting": true})
		return
	}
	cookie.Clear(w, r, DeviceCookie)
	u, err := m.Store.UserBySubject(ctx, subject)
	// One who must choose a new password can't confirm a code, but may have
	// been told to since: that choice takes no old password, so a login
	// from here could make it.
	if err != nil || u.Disabled || !u.Confirmed || u.MustChange {
		m.refuse(w, r, problem.NotFound, "codeExpired")
		return
	}
	_, token, err := m.Store.CreateLogin(ctx, u.Subject, nil, "", m.LoginLifetime)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	cookie.Set(w, r, &http.Cookie{Name: LoginCookie, Value: token, MaxAge: int(m.LoginLifetime.Seconds())})
	writeOK(w, map[string]string{"next": safeReturn(req.Return)})
}

// linkPage is where a code is confirmed; anyone not signed in signs in
// first, and comes back (the code, in the fragment, comes along).
func (m *Managed) linkPage(w http.ResponseWriter, r *http.Request) {
	_, u := m.current(r)
	if u == nil || !u.Confirmed || u.MustChange {
		q := url.Values{"return": {linkPagePath}}
		http.Redirect(w, r, signinPagePath+"?"+q.Encode(), http.StatusSeeOther)
		return
	}
	renderPage(w, r, m.Store, m.Logger, page{Mode: "link", Email: u.Email})
}

// deviceRequest is a code typed, or scanned, on /link.
type deviceRequest struct {
	Code   string `json:"code"`
	Return string `json:"return"`
}

// deviceSigner reads a code sent by someone signed in, counting wrong ones;
// it answers itself and returns "" when there's nothing to do.
func (m *Managed) deviceSigner(w http.ResponseWriter, r *http.Request) (subject, code string) {
	_, u := m.current(r)
	if u == nil || !u.Confirmed || u.MustChange {
		problem.Send(w, problem.NotAuthenticated, "")
		return "", ""
	}
	var req deviceRequest
	if !decode(r, &req) || len(req.Code) > 32 {
		m.refuse(w, r, problem.InvalidRequest, "")
		return "", ""
	}
	return u.Subject, cleanCode(req.Code)
}

// wrongCode counts a wrong code, and answers it.
func (m *Managed) wrongCode(w http.ResponseWriter, r *http.Request, subject string) {
	now := time.Now()
	client := clientFrom(identity.ClientIP(r.Context()))
	if !m.codesBySubject.allow(subject, now) || !m.codesByClient.allow(client, now) {
		m.refuse(w, r, problem.RateLimited, "tooManyTries")
		return
	}
	m.refuse(w, r, problem.NotFound, "codeUnknown")
}

// mayTryCode says whether someone may try a code now, without counting it.
func (m *Managed) mayTryCode(r *http.Request, subject string) bool {
	now := time.Now()
	return m.codesBySubject.allows(subject, now) && m.codesByClient.allows(clientFrom(identity.ClientIP(r.Context())), now)
}

// deviceLook tells what a code would sign in: the screen's kind, and how
// long ago it asked.
func (m *Managed) deviceLook(w http.ResponseWriter, r *http.Request) {
	subject, code := m.deviceSigner(w, r)
	if subject == "" {
		return
	}
	if !m.mayTryCode(r, subject) {
		m.refuse(w, r, problem.RateLimited, "tooManyTries")
		return
	}
	d, err := m.Store.PendingDeviceCode(r.Context(), code)
	if code == "" || errors.Is(err, store.ErrNotFound) {
		m.wrongCode(w, r, subject)
		return
	}
	if err != nil {
		m.fail(w, r, err)
		return
	}
	writeOK(w, map[string]any{"device": d.Device, "minutes": int(time.Since(d.Created).Minutes())})
}

// deviceConfirm signs the screen in with the account of the person
// confirming.
func (m *Managed) deviceConfirm(w http.ResponseWriter, r *http.Request) {
	subject, code := m.deviceSigner(w, r)
	if subject == "" {
		return
	}
	if !m.mayTryCode(r, subject) {
		m.refuse(w, r, problem.RateLimited, "tooManyTries")
		return
	}
	err := m.Store.ConfirmDeviceCode(r.Context(), code, subject)
	if code == "" || errors.Is(err, store.ErrNotFound) {
		m.wrongCode(w, r, subject)
		return
	}
	if err != nil {
		m.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
