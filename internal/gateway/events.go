package gateway

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Revocations are the logins the backend ended before their time, as its
// events tell the gateway (overfin/events.go): the gateway's first check
// refuses them at once, rather than letting them reach the backend's own
// refusal. In memory only, each until its login would have expired.
type Revocations struct {
	// New, when set, gets the routes of new titles' artwork the backend
	// announces (the cache fetches them ahead).
	New func(paths []string)
	// Access, when set, runs when what a profile may see changed, and each
	// time the stream starts (a change during a break isn't missed): the
	// cache forgets its access answers.
	Access func()

	mu      sync.Mutex
	revoked map[[sha256.Size]byte]int64 // the login cookie's hash: when it'd have expired
	seq     int64
	last    []byte // the hash revocation seq carried: told back, so a restored backend is noticed
}

// Revoked says whether the login cookie login was ended.
func (rv *Revocations) Revoked(login string) bool {
	if rv == nil {
		return false
	}
	h := sha256.Sum256([]byte(login))
	rv.mu.Lock()
	defer rv.mu.Unlock()
	exp, ok := rv.revoked[h]
	return ok && exp > time.Now().Unix()
}

func (rv *Revocations) add(seq int64, hash []byte, expires int64) {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	if rv.revoked == nil {
		rv.revoked = map[[sha256.Size]byte]int64{}
	}
	if len(hash) == sha256.Size {
		rv.revoked[[sha256.Size]byte(hash)] = expires
	}
	if seq > rv.seq {
		rv.seq, rv.last = seq, hash
	}
	now := time.Now().Unix()
	if len(rv.revoked)%1024 == 0 {
		for k, e := range rv.revoked {
			if e <= now {
				delete(rv.revoked, k)
			}
		}
	}
}

// Follow reads the backend's events until ctx ends, connecting again
// whenever the stream ends (the backend gone, the link down), from the
// last revocation it heard of.
func (rv *Revocations) Follow(ctx context.Context, backend http.RoundTripper, logger *slog.Logger) {
	client := &http.Client{Transport: backend}
	for ctx.Err() == nil {
		rv.mu.Lock()
		since, at := rv.seq, rv.last
		rv.mu.Unlock()
		// The start of the last revocation's hash too: a backend restored
		// from a backup, then given new ones up to the same number, has
		// another there, and starts the count again (reset).
		q := "since=" + strconv.FormatInt(since, 10)
		if len(at) >= 8 {
			q += "&at=" + hex.EncodeToString(at[:8])
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://backend/link/v1/events?"+q, nil)
		if resp, err := client.Do(req); err == nil {
			if resp.StatusCode == http.StatusOK {
				if rv.Access != nil {
					rv.Access()
				}
				sc := bufio.NewScanner(resp.Body)
				for sc.Scan() {
					rv.event(sc.Bytes())
				}
			}
			resp.Body.Close()
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

// event takes in one line of the backend's events (section 8 of
// docs/link-protocol.md); a line it doesn't understand changes nothing.
func (rv *Revocations) event(line []byte) {
	var e struct {
		Type    string   `json:"type"`
		Seq     int64    `json:"seq"`
		Hash    string   `json:"hash"`
		Expires int64    `json:"expires"`
		Images  []string `json:"images"`
	}
	if json.Unmarshal(line, &e) != nil {
		return
	}
	switch e.Type {
	case "new":
		if rv.New != nil {
			rv.New(e.Images)
		}
	case "access":
		if rv.Access != nil {
			rv.Access()
		}
	case "reset":
		// The backend's numbers started again (its database restored):
		// count from its first, the revocations heard kept.
		rv.mu.Lock()
		rv.seq, rv.last = 0, nil
		rv.mu.Unlock()
	case "revoked":
		if h, err := hex.DecodeString(e.Hash); err == nil {
			rv.add(e.Seq, h, e.Expires)
		}
	}
}
