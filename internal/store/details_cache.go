package store

import (
	"encoding/json"
	"hash/maphash"
	"slices"
	"sync"
)

// maxCachedDetails is how many titles' details detailsCache keeps; full,
// it starts over.
const maxCachedDetails = 20000

// detailsCache keeps titles' details decoded: a page of cards read each
// one's whole page (cast, seasons, other names; 6 KB of JSON on average)
// for its runtime, request after request. Kept by the JSON's content, so
// details fetched again are decoded again.
type detailsCache struct {
	once sync.Once
	seed maphash.Seed
	mu   sync.RWMutex
	m    map[detailsKey]TitleDetails
}

type detailsKey struct {
	title string // kind/tmdb
	size  int
	sum   uint64
}

// decode fills d from raw, as json.Unmarshal would. The cast and seasons
// are d's own (the translation writes into them).
func (c *detailsCache) decode(title string, raw []byte, d *TitleDetails) error {
	// The hash outside the lock: requests side by side don't wait on it.
	c.once.Do(func() { c.seed = maphash.MakeSeed() })
	k := detailsKey{title, len(raw), maphash.Bytes(c.seed, raw)}
	c.mu.RLock()
	got, ok := c.m[k]
	c.mu.RUnlock()
	if !ok {
		if err := json.Unmarshal(raw, &got); err != nil {
			return err
		}
		c.mu.Lock()
		if c.m == nil || len(c.m) >= maxCachedDetails {
			c.m = map[detailsKey]TitleDetails{}
		}
		c.m[k] = got
		c.mu.Unlock()
	}
	*d = got
	d.Cast, d.Seasons = slices.Clone(got.Cast), slices.Clone(got.Seasons)
	return nil
}
