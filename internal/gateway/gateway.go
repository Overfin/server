// Package gateway is Overfin's gateway: the only part the internet
// reaches. It passes on, over the link its backend opened (internal/link),
// exactly the routes the API specification lists (the allowlist: api
// package) and the site's pages, with the visitor's address; everything
// else is refused at the gateway. It holds nothing personal: no data, no
// secret of Overfin's, nothing about visitors in its logs (SECURITY.md,
// the gateway).
package gateway

import (
	"crypto/ed25519"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/overfin/server/api"
	"github.com/overfin/server/internal/edge"
	"github.com/overfin/server/internal/link"
)

// Backend is the gateway's side of the link, as the gateway uses it.
type Backend interface {
	http.RoundTripper
	Status() link.Status
	// BackendKey checks the backend's edge signatures (package edge).
	BackendKey() ed25519.PublicKey
}

// Handler passes visitors' requests to the backend: the API's routes the
// specification lists, and GET for the site's pages; every other request
// gets a plain 404 here, without reaching the backend.
func Handler(b Backend, version string, logger *slog.Logger) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = "http", "backend"
			pr.Out.Host = pr.In.Host
			// Only the gateway says who the visitor is: whatever a visitor
			// sent in its name is dropped.
			pr.Out.Header.Del("X-Forwarded-For")
			pr.Out.Header.Del("X-Forwarded-Host")
			pr.Out.Header.Del("X-Forwarded-Proto")
			pr.Out.Header.Del("Forwarded")
			pr.Out.Header.Del("X-Real-Ip")
			pr.Out.Header.Set(link.ClientAddr, ClientAddr(pr.In))
			if pr.In.TLS != nil {
				pr.Out.Header.Set("X-Forwarded-Proto", "https")
			}
		},
		Transport:     b,
		FlushInterval: -1, // streams pass as they come
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, link.ErrNoBackend) {
				down(w, r, b.Status(), version)
				return
			}
			if r.Context().Err() != nil {
				return // the visitor left
			}
			logger.Warn("gateway: the backend didn't answer a request", "err", err)
			down(w, r, b.Status(), version)
		},
	}
	mux := http.NewServeMux()
	for _, op := range api.Operations() {
		if op.Planned {
			continue
		}
		var h http.Handler = proxy
		if !op.Public {
			h = requireSession(b, proxy)
		}
		mux.Handle(op.Method+" "+op.Path, h)
	}
	// The site's pages: GET (and HEAD) only, never the control panel, the
	// apps' API, nor an API route the specification doesn't list.
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if refused(r.URL.Path) {
			notFound(w)
			return
		}
		proxy.ServeHTTP(w, r)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { notFound(w) })
	return withClientAddr(Limit(mux))
}

// requireSession lets through only a request that carries a login cookie
// and the backend's signature of it (package edge): without one, the
// visitor isn't signed in, and the backend isn't asked. A filter only: the
// backend checks the session itself.
func requireSession(b Backend, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		login, token, ok := edge.Pair(r)
		key := b.BackendKey()
		if !ok || key == nil || !edge.Verify(key, login, token) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"type":"not-authenticated","title":"Not logged in","status":401}` + "\n"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// refused are the paths the gateway never passes on as pages.
func refused(p string) bool {
	for _, prefix := range []string{"/api/", "/admin", "/apps", "/link"} {
		if p == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

func notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("Not found\n"))
}
