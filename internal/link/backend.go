package link

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/overfin/server/internal/edge"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Backend is the backend's side of the link: it connects out to its
// gateway, keeps the connection open (connecting again whenever it ends),
// and answers the gateway's requests with handler.
type Backend struct {
	// OnPaired, when set, runs once a pairing succeeds (the one-machine
	// setup deletes the code it shared).
	OnPaired func()

	dir     string
	version string
	handler http.Handler
	logger  *slog.Logger
	id      *Identity

	mux      *http.ServeMux // the gateway's own questions (linkRoutes)
	egressMu sync.Mutex
	egress   map[string]time.Time // addresses the backend asked to reach, when
}

// pairing is the backend's side of the pairing, in its folder.
type pairing struct {
	Address    string `json:"address"`              // the gateway's, host:port
	ServerName string `json:"serverName,omitempty"` // the name sent in TLS (a proxy in front routes on it)
	Gateway    string `json:"gateway,omitempty"`    // its key, hex, once paired
	Code       string `json:"code,omitempty"`       // a code waiting to be used
}

// NewBackend loads (or makes) the backend's key from dir.
func NewBackend(dir, version string, handler http.Handler, logger *slog.Logger) (*Backend, error) {
	id, err := LoadIdentity(filepath.Join(dir, "backend.key"))
	if err != nil {
		return nil, err
	}
	b := &Backend{dir: dir, version: version, logger: logger, id: id}
	b.handler = b.linkRoutes(withClientAddr(handler))
	return b, nil
}

// Public is the backend's own key.
func (b *Backend) Public() ed25519.PublicKey { return b.id.Public() }

// SignEdge signs a visitor's login cookie for the gateway's first check
// (package edge), with the backend's link key, which the gateway pinned.
func (b *Backend) SignEdge(login string) string { return edge.Sign(b.id.key, login) }

func (b *Backend) pairingPath() string { return filepath.Join(b.dir, "pairing.json") }

func (b *Backend) load() (pairing, error) {
	var p pairing
	data, err := os.ReadFile(b.pairingPath())
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(data, &p)
}

func (b *Backend) save(p pairing) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(b.pairingPath(), data)
}

// Pair starts pairing with the gateway at address (host:port; serverName,
// when empty, is its host): it makes a one-time code, for the operator to
// give the gateway (OVERFIN_PAIRING). The running backend uses it at its
// next attempt; the former gateway stays paired until the new pairing
// succeeds.
func (b *Backend) Pair(address, serverName string) (string, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("the gateway's address, as host:port: %w", err)
	}
	if serverName == "" {
		serverName = host
	}
	code, err := NewCode()
	if err != nil {
		return "", err
	}
	p, err := b.load()
	if err != nil {
		return "", err
	}
	p.Address, p.ServerName, p.Code = address, serverName, code
	return code, b.save(p)
}

// State says whether the backend is paired with a gateway, and whether a
// pairing code waits to be used.
func (b *Backend) State() (paired, pending bool, err error) {
	p, err := b.load()
	return p.Gateway != "", p.Code != "", err
}

// errRefused is the gateway's refusal: no point trying again soon.
type errRefused struct{ reason string }

func (e errRefused) Error() string { return e.reason }

// Run keeps the backend connected to its gateway until ctx ends.
func (b *Backend) Run(ctx context.Context) {
	wait := time.Second
	said := ""
	for ctx.Err() == nil {
		p, err := b.load()
		switch {
		case err != nil:
			b.logger.Error("link: the pairing file can't be read", "err", err)
		case p.Address == "":
			if said != "unpaired" {
				b.logger.Info("link: not paired with a gateway yet (overfin-backend -pair <gateway address>)")
				said = "unpaired"
			}
		default:
			started := time.Now()
			err := b.connect(ctx, p)
			msg := "link: the connection ended"
			var refused errRefused
			switch {
			case ctx.Err() != nil:
				return
			case errors.As(err, &refused):
				msg, wait = "link: the gateway refused the connection", 30*time.Second
			case time.Since(started) > time.Minute:
				wait = time.Second
			}
			if err != nil && err.Error() != said {
				b.logger.Warn(msg, "err", err)
				said = err.Error()
			} else if err == nil {
				said = ""
			}
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
		wait = min(2*wait, 30*time.Second)
	}
}

// connect makes one connection to the gateway and serves it until it ends.
func (b *Backend) connect(ctx context.Context, p pairing) error {
	var pinned ed25519.PublicKey
	if p.Gateway != "" {
		k, err := hex.DecodeString(p.Gateway)
		if err != nil || len(k) != ed25519.PublicKeySize {
			return errors.New("the gateway's saved key isn't a key")
		}
		pinned = k
	}
	if pinned == nil && p.Code == "" {
		return errors.New("no gateway paired and no pairing code")
	}
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", p.Address)
	if err != nil {
		return err
	}
	c := tls.Client(raw, &tls.Config{
		Certificates: []tls.Certificate{b.id.cert},
		NextProtos:   []string{ALPN},
		ServerName:   p.ServerName,
		MinVersion:   tls.VersionTLS13,
		// The gateway's key is checked below, against the pinned one or
		// the pairing's proof: no certificate authority.
		InsecureSkipVerify: true,
	})
	defer c.Close()
	c.SetDeadline(time.Now().Add(handshakeWait))
	if err := c.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("TLS with the gateway: %w", err)
	}
	cs := c.ConnectionState()
	if cs.NegotiatedProtocol != ALPN {
		return errors.New("that address doesn't answer as an Overfin gateway")
	}
	gw, err := peerKey(cs)
	if err != nil {
		return err
	}
	pairingNow := p.Code != "" && (pinned == nil || !bytes.Equal(gw, pinned))
	if !pairingNow && !bytes.Equal(gw, pinned) {
		return errRefused{"the gateway's key isn't the paired one: pair again (overfin-backend -pair)"}
	}
	var code []byte
	if pairingNow {
		if code, err = codeBytes(p.Code); err != nil {
			return err
		}
		_, err = fmt.Fprintf(c, "%s pair %s %s\n", preambleTag, b.version, hex.EncodeToString(proof(code, "backend", gw, b.id.Public())))
	} else {
		_, err = fmt.Fprintf(c, "%s link %s\n", preambleTag, b.version)
	}
	if err != nil {
		return err
	}
	f, err := readLine(c)
	if err != nil {
		return fmt.Errorf("the gateway's answer: %w", err)
	}
	if len(f) == 0 || f[0] != "OK" {
		return refusal(f, b.version)
	}
	if pairingNow {
		if len(f) < 2 {
			return errors.New("the gateway's answer has no proof")
		}
		theirs, err := hexProof(f[1])
		if err != nil || subtle.ConstantTimeCompare(theirs, proof(code, "gateway", gw, b.id.Public())) != 1 {
			return errRefused{"the gateway didn't prove it has the pairing code: not paired"}
		}
		p.Gateway, p.Code = hex.EncodeToString(gw), ""
		if err := b.save(p); err != nil {
			return err
		}
		b.logger.Info("link: paired with the gateway", "address", p.Address, "key", Fingerprint(gw))
		if b.OnPaired != nil {
			b.OnPaired()
		}
	}
	c.SetDeadline(time.Time{})
	b.logger.Info("link: connected to the gateway", "address", p.Address)
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	serveConn(ctx, c, b.handler)
	return io.EOF
}

// refusal explains the gateway's NO.
func refusal(f []string, version string) error {
	if len(f) < 2 {
		return errRefused{"the gateway refused"}
	}
	switch f[1] {
	case "version":
		other := "?"
		if len(f) > 2 {
			other = f[2]
		}
		return errRefused{fmt.Sprintf("the gateway runs version %s, this backend %s: both must be the same version", other, version)}
	case "not-paired":
		return errRefused{"the gateway isn't paired with this backend: pair again (overfin-backend -pair)"}
	case "no-code":
		return errRefused{"the gateway has no pairing code: set OVERFIN_PAIRING there"}
	case "code-used":
		return errRefused{"the gateway has already used that code: make a new one (overfin-backend -pair)"}
	case "wrong-code":
		return errRefused{"the gateway has another pairing code"}
	default:
		return errRefused{"the gateway refused: " + f[1]}
	}
}

// ClientAddr is the header in which the gateway gives the backend each
// request's visitor address (host:port). The backend trusts it because it
// comes over the link, which only the paired gateway can use; it's taken
// off before the backend's own code sees the request.
const ClientAddr = "Overfin-Client-Addr"

// withClientAddr makes the visitor's address the request's own, for the
// backend's limits and logs.
func withClientAddr(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.Header.Get(ClientAddr); v != "" {
			if ap, err := netip.ParseAddrPort(v); err == nil {
				r.RemoteAddr = ap.String()
			}
		}
		r.Header.Del(ClientAddr)
		next.ServeHTTP(w, r)
	})
}
