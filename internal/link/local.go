package link

import "net/http"

// The backend's side of the bundled gateway (internal/gateway, Socket): the
// site, served on a unix socket in a volume only the two share. No link
// protocol there: the socket's volume is the trust, as the TLS keys are on
// the link.

// LocalVersion is the header in which the bundled gateway names its
// version, and the backend its own when they differ: both must be the
// same release, as on the link.
const LocalVersion = "Overfin-Version"

// Local is the handler the backend serves the gateway beside it: site,
// with the visitor's address the gateway gives (ClientAddr). A gateway of
// another version gets a 503 naming the backend's.
func Local(version string, site http.Handler) http.Handler {
	site = withClientAddr(site)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(LocalVersion) != version {
			w.Header().Set(LocalVersion, version)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		r.Header.Del(LocalVersion)
		r.Header.Del(CheckOnly)
		site.ServeHTTP(w, r)
	})
}
