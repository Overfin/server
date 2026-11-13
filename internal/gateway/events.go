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

	mu      sync.Mutex
	revoked map[[sha256.Size]byte]int64 // the login cookie's hash: when it'd have expired
	seq     int64
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
	rv.seq = max(rv.seq, seq)
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
		since := rv.seq
		rv.mu.Unlock()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://backend/link/v1/events?since="+strconv.FormatInt(since, 10), nil)
		if resp, err := client.Do(req); err == nil {
			if resp.StatusCode == http.StatusOK {
				sc := bufio.NewScanner(resp.Body)
				for sc.Scan() {
					var e struct {
						Type    string   `json:"type"`
						Seq     int64    `json:"seq"`
						Hash    string   `json:"hash"`
						Expires int64    `json:"expires"`
						Images  []string `json:"images"`
					}
					if json.Unmarshal(sc.Bytes(), &e) != nil {
						continue
					}
					if e.Type == "new" && rv.New != nil {
						rv.New(e.Images)
						continue
					}
					if e.Type != "revoked" {
						continue
					}
					if h, err := hex.DecodeString(e.Hash); err == nil {
						rv.add(e.Seq, h, e.Expires)
					}
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
