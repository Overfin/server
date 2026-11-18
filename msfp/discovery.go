package msfp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/overfin/server/msfp/wire"
)

// The discovery document (MSFP core section 11): where this server's
// federation calls are, served at /.well-known/msfp when the host turns
// it on (off by default: a server that serves it tells anyone it
// federates); and peers' documents, fetched when their address is learnt,
// for the base path every call to them uses.

// discoveryDocument is the document this server serves.
type discoveryDocument struct {
	Core  []int            `json:"core"`
	Parts map[string][]int `json:"parts"`
	Base  string           `json:"base"`
}

// DiscoveryHandler serves this server's discovery document at
// /.well-known/msfp, on GET (and HEAD), while it is on (Config.Discovery,
// SetDiscovery); otherwise, and for anything else, exactly what the
// host answers for a path that doesn't exist (Config.NotFound). Mount it
// at /.well-known/msfp on the address's root, whatever the Prefix.
func (e *Engine) DiscoveryHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !e.discovery.Load() || r.URL.Path != wire.DiscoveryPath || r.Method != http.MethodGet && r.Method != http.MethodHead {
			e.cfg.NotFound.ServeHTTP(w, r)
			return
		}
		v := e.versions()
		body, err := marshal(discoveryDocument{Core: v.Core, Parts: v.Parts, Base: e.cfg.Prefix + defaultBase})
		if err != nil {
			e.cfg.NotFound.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	})
}

// SetDiscovery turns the discovery document on or off. sync.
func (e *Engine) SetDiscovery(on bool) { e.discovery.Store(on) }

// fetchBase fetches the discovery document at address and gives the
// base path it says, "" for the default: for a 404, an answer that
// isn't a valid document, or none at all (MSFP core section 11). The
// fetch is a plain GET with no signature, any certificate accepted, no
// redirects followed, 16 KiB read at most; nothing is fetched at an
// address of the .invalid form.
func (e *Engine) fetchBase(ctx context.Context, address string) string {
	base, _ := e.readBase(ctx, address)
	return base
}

// readBase is fetchBase, and whether address answered at all.
func (e *Engine) readBase(ctx context.Context, address string) (base string, answered bool) {
	if wire.Uncallable(address) || wire.CheckAddress(address) != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address+wire.DiscoveryPath, nil)
	if err != nil {
		return "", false
	}
	res, err := e.conns.client(nil, true).Do(req)
	if err != nil {
		return "", false
	}
	defer res.Body.Close()
	ce := res.Header.Get("Content-Encoding")
	if res.StatusCode != http.StatusOK || res.Uncompressed || ce != "" && !strings.EqualFold(ce, "identity") ||
		wire.MediaType(res.Header.Get("Content-Type")) != "application/json" {
		return "", true
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, wire.MaxDiscovery+1))
	if err != nil || len(body) > wire.MaxDiscovery {
		return "", true
	}
	d, err := wire.ReadDiscovery(body)
	if err != nil || d.Base == defaultBase {
		return "", true
	}
	return d.Base, true
}

// keepBase fetches peer id's discovery document at address and keeps the
// base it says, if address is still the peer's then. No answer at all
// keeps the base stored: a moment's outage doesn't undo it.
func (e *Engine) keepBase(ctx context.Context, id, address string) {
	base, answered := e.readBase(ctx, address)
	if !answered {
		return
	}
	err := e.updatePeer(ctx, id, func(p *PeerRecord) {
		if p.Address == address && p.Inbound != InboundNo {
			p.Base = base
		}
	})
	if err != nil && !errors.Is(err, ErrNotFound) {
		e.log.Warn("msfp: a peer's base path couldn't be kept", "peer", id, "error", err)
	}
}
