package playback

import (
	"errors"
	"sync"
	"time"

	"github.com/overfin/server/internal/jellyfin"
)

// Previews tickets (MSFP video section 5.6, Overfin's players' previews
// call): a copy's seek-bar previews before it plays, under a ticket of
// their own. One works for the sheets of that one copy only, in the
// session and for the profile it was made for, for ten minutes; a session
// gets a few a minute (rule 45).

// PreviewsFor is how long a previews ticket works.
const PreviewsFor = 10 * time.Minute

// Previews tickets a session may get in a minute.
const previewsPerMinute = 30

// PreviewTicket is a previews ticket: whose it is, and the sheets it
// opens (Overfin's own, by the copy's fingerprint).
type PreviewTicket struct {
	SessionID, ProfileID string
	// Previews is the fingerprint the sheets are kept by; Trickplay, their
	// layout.
	Previews  string
	Trickplay jellyfin.Trickplay
	// Peer, Key, Version and Etag, for a paired server's copy: the
	// pairing, the title's key and the copy's version ID there, and its
	// etag (its sheets are fetched from it, kept by etag and layout).
	Peer, Key, Version, Etag string
	expires                  time.Time
}

// ErrTooManyPreviews: the session asked for too many previews tickets in
// the last minute.
var ErrTooManyPreviews = errors.New("playback: too many previews tickets")

type previewTickets struct {
	mu     sync.Mutex
	byHash map[[32]byte]*PreviewTicket
	// issued are when each session got its tickets, the last minute's.
	issued map[string][]time.Time
}

// IssuePreviews makes a previews ticket for p: 192 random bits, looked
// up by its hash like a stream ticket.
func (t *Tickets) IssuePreviews(p PreviewTicket, now time.Time) (string, error) {
	pt := &t.previews
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if pt.byHash == nil {
		pt.byHash, pt.issued = map[[32]byte]*PreviewTicket{}, map[string][]time.Time{}
	}
	for h, e := range pt.byHash {
		if !now.Before(e.expires) {
			delete(pt.byHash, h)
		}
	}
	for s, times := range pt.issued {
		kept := times[:0]
		for _, at := range times {
			if now.Sub(at) < time.Minute {
				kept = append(kept, at)
			}
		}
		if len(kept) == 0 {
			delete(pt.issued, s)
		} else {
			pt.issued[s] = kept
		}
	}
	if len(pt.issued[p.SessionID]) >= previewsPerMinute {
		return "", ErrTooManyPreviews
	}
	pt.issued[p.SessionID] = append(pt.issued[p.SessionID], now)
	ticket := random(24)
	e := p
	e.expires = now.Add(PreviewsFor)
	pt.byHash[ticketHash(ticket)] = &e
	return ticket, nil
}

// IsPreviews says whether ticket is a previews ticket still working,
// whoever's it is.
func (t *Tickets) IsPreviews(ticket string, now time.Time) bool {
	pt := &t.previews
	pt.mu.Lock()
	defer pt.mu.Unlock()
	e, ok := pt.byHash[ticketHash(ticket)]
	return ok && now.Before(e.expires)
}

// PreviewsByTicket finds a previews ticket for the session and profile it
// was made for only.
func (t *Tickets) PreviewsByTicket(ticket, sessionID, profileID string, now time.Time) (PreviewTicket, bool) {
	pt := &t.previews
	pt.mu.Lock()
	defer pt.mu.Unlock()
	e, ok := pt.byHash[ticketHash(ticket)]
	if !ok || !now.Before(e.expires) || e.SessionID != sessionID || e.ProfileID != profileID {
		return PreviewTicket{}, false
	}
	return *e, true
}
