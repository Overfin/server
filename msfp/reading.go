package msfp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/overfin/server/msfp/wire"
)

// The catalog, on the reading side (MSFP catalog sections 2.2, 3.2 and
// 3.3): the libraries a peer offers this server, those wanted, and the
// readings of their titles, kept in storage for the host's pages.

// The bounds of what is kept of a peer (catalog sections 2.2 and 6).
const (
	maxLibraries  = 200
	maxPeerTitles = 1_000_000
	maxGoneKey    = 128
)

// readingTime is how long a reading may take (catalog section 3.2: 5
// minutes); pageWait the longest a page is waited for when the peer
// asks to wait (a signed 429 or 503 with retryAfter).
const (
	readingTime = 5 * time.Minute
	pageWait    = time.Minute
)

// firstReading is how long after the start the first readings go.
const firstReading = time.Minute

// readings are the readings of peers' catalogs: one at a time per peer,
// each on its schedule (in memory: after a restart, a minute after the
// start).
type readings struct {
	mu      sync.Mutex
	running map[string]*sync.Mutex
	next    map[string]time.Time
	number  int64
}

// peerLock is the lock of a peer's readings.
func (r *readings) peerLock(id string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running == nil {
		r.running = map[string]*sync.Mutex{}
	}
	l := r.running[id]
	if l == nil {
		l = &sync.Mutex{}
		r.running[id] = l
	}
	return l
}

// newReading is a number for a reading, above every one before.
func (e *Engine) newReading() int64 {
	r := &e.reading
	r.mu.Lock()
	defer r.mu.Unlock()
	r.number = max(r.number+1, e.now().UnixNano())
	return r.number
}

// readSoon has a peer's catalog read at the next tick.
func (e *Engine) readSoon(id string) {
	r := &e.reading
	r.mu.Lock()
	if r.next == nil {
		r.next = map[string]time.Time{}
	}
	r.next[id] = time.Time{}
	r.mu.Unlock()
}

// readDue reads, in the background, the catalogs due now: each paired
// peer that can be called, a minute after the start, then every
// SyncEvery, give or take a tenth; wait waits for them (RunOnce).
func (e *Engine) readDue(ctx context.Context, wait bool) {
	var peers []PeerRecord
	if err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		var err error
		peers, err = tx.Peers()
		return err
	}); err != nil {
		return
	}
	now := e.now()
	r := &e.reading
	var due []string
	r.mu.Lock()
	if r.next == nil {
		r.next = map[string]time.Time{}
	}
	for _, p := range peers {
		if p.State != Paired || p.Inbound != InboundYes || !p.Suspended.IsZero() || !e.on(&p, wire.PartCatalog) {
			continue
		}
		next, ok := r.next[p.ID]
		if !ok {
			next = e.started.Add(firstReading)
			r.next[p.ID] = next
		}
		if !now.Before(next) {
			spread := time.Duration(float64(e.cfg.SyncEvery) * (0.9 + 0.2*rand.Float64()))
			r.next[p.ID] = now.Add(spread)
			due = append(due, p.ID)
		}
	}
	r.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range due {
		run := func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readingTime)
			defer cancel()
			if _, err := e.readCatalog(ctx, id, true); err != nil && !errors.Is(err, ErrBackingOff) {
				e.log.Info("msfp: a peer's catalog couldn't be read", "peer", id, "error", err)
			}
		}
		if wait {
			wg.Go(run)
		} else {
			e.bg.Go(run)
		}
	}
	wg.Wait()
}

// usablePeer checks that a peer can be asked for its catalog: paired,
// not suspended, callable, with catalog on.
func (e *Engine) usablePeer(p *PeerRecord, part string) error {
	switch {
	case p.State != Paired:
		return ErrNotPaired
	case !p.Suspended.IsZero():
		return ErrSuspended
	case p.Inbound != InboundYes:
		return ErrUncallable
	case !e.on(p, part):
		return ErrNotSupported
	}
	return nil
}

// TheirLibraries asks a peer which libraries it offers this server now,
// and keeps them, those wanted marked (gone ones too, marked gone).
// Errors: ErrNotFound, ErrNotPaired, ErrUncallable, ErrSuspended,
// ErrNotSupported, ErrUnreachable, ErrBackingOff.
func (e *Engine) TheirLibraries(ctx context.Context, peer string) ([]WantedLibrary, error) {
	p, err := e.peerRecord(ctx, peer)
	if err != nil {
		return nil, err
	}
	return e.theirLibraries(ctx, p, false)
}

// theirLibraries calls libraries on p and keeps its answer: the
// libraries, its name, its limits (MSFP catalog section 2.2).
func (e *Engine) theirLibraries(ctx context.Context, p *PeerRecord, background bool) ([]WantedLibrary, error) {
	if err := e.usablePeer(p, wire.PartCatalog); err != nil {
		return nil, err
	}
	var ans struct {
		Name      *string           `json:"name"`
		Libraries []json.RawMessage `json:"libraries"`
		Limits    json.RawMessage   `json:"limits"`
	}
	r, err := e.call(ctx, p, callLibraries, struct{}{}, &ans, background)
	if err != nil {
		return nil, err
	}
	if r.status != http.StatusOK {
		return nil, e.refused(p, r)
	}
	var libs []WantedLibrary
	for _, raw := range ans.Libraries {
		if len(libs) == maxLibraries {
			break
		}
		var l struct {
			ID   string          `json:"id"`
			Name json.RawMessage `json:"name"`
			Kind json.RawMessage `json:"kind"`
		}
		if wire.DecodeEntry(raw, &l) != nil || !wire.ID(l.ID) ||
			slices.ContainsFunc(libs, func(o WantedLibrary) bool { return o.ID == l.ID }) {
			continue
		}
		var name, kind string
		_ = json.Unmarshal(l.Name, &name) // not a string: none
		_ = json.Unmarshal(l.Kind, &kind)
		name = cleanName(name, "")
		if name == "" {
			name = "?"
		}
		libs = append(libs, WantedLibrary{ID: l.ID, Name: name, Kind: cleanKind(kind)})
	}
	limits := readLimits(ans.Limits, e.on(p, wire.PartMusic))
	var events []Event
	var kept []WantedLibrary
	err = e.updatePeer(ctx, p.ID, func(q *PeerRecord) {
		events, kept = nil, nil
		if ans.Name != nil {
			if n := cleanName(*ans.Name, ""); n != "" {
				q.Name = n
			}
		}
		if limits != nil && e.on(q, wire.PartPlayback) {
			q.TheirLimits = limits
		}
		var gone []string
		for _, l := range libs {
			l.Wanted = slices.ContainsFunc(q.Wants, func(w WantedLibrary) bool { return w.ID == l.ID && w.Wanted })
			kept = append(kept, l)
		}
		for _, w := range q.Wants {
			if w.Wanted && !slices.ContainsFunc(libs, func(l WantedLibrary) bool { return l.ID == w.ID }) {
				if !w.Gone {
					gone = append(gone, w.ID)
				}
				w.Gone = true
				kept = append(kept, w)
			}
		}
		q.Wants = kept
		if len(gone) > 0 {
			events = append(events, WantsGone{Peer: q.ID, Name: q.Name, Libraries: gone})
		}
	})
	if err != nil {
		return nil, err
	}
	e.notify(events...)
	return slices.Clone(kept), nil
}

// readLimits reads a peer's limits (MSFP playback section 5, music
// section 5.1): nil when they aren't an object of integers in range.
func readLimits(raw json.RawMessage, music bool) *Limits {
	if raw == nil {
		return nil
	}
	var w struct {
		Streams         *int `json:"streams"`
		Transcodes      *int `json:"transcodes"`
		BitrateKbps     int  `json:"bitrateKbps"`
		AudioStreams    *int `json:"audioStreams"`
		AudioTranscodes *int `json:"audioTranscodes"`
	}
	if wire.DecodeEntry(raw, &w) != nil || w.Streams == nil || w.Transcodes == nil {
		return nil
	}
	l := Limits{Streams: *w.Streams, Transcodes: *w.Transcodes, BitrateKbps: w.BitrateKbps}
	if music && w.AudioStreams != nil && w.AudioTranscodes != nil {
		l.Audio, l.AudioStreams, l.AudioTranscodes = true, *w.AudioStreams, *w.AudioTranscodes
	}
	if l.Check() != nil {
		return nil
	}
	return &l
}

// refused is a peer's signed refusal as an error; a signed 404 that
// isn't not-supported may mean it doesn't count us as paired yet: a hello
// goes at once.
func (e *Engine) refused(p *PeerRecord, r reply) error {
	err := refusalError(r.status, r.problem)
	if errors.Is(err, ErrNotFound) {
		e.helloNow(p.ID)
	}
	return err
}

// Wants lists the peer's libraries as last read, those wanted marked,
// those gone from its last answer marked gone. Error: ErrNotFound.
func (e *Engine) Wants(ctx context.Context, peer string) ([]WantedLibrary, error) {
	p, err := e.peerRecord(ctx, peer)
	if err != nil {
		return nil, err
	}
	return slices.Clone(p.Wants), nil
}

// SetWants sets which of a peer's libraries this server reads (their
// public IDs); IDs it doesn't offer are left out. A library not seen yet
// has the peer asked first (TheirLibraries). A reading follows at once.
// Errors: ErrNotFound, ErrNotPaired, and TheirLibraries'.
func (e *Engine) SetWants(ctx context.Context, peer string, libraries []string) error {
	p, err := e.peerRecord(ctx, peer)
	if err != nil {
		return err
	}
	if p.State != Paired {
		return ErrNotPaired
	}
	for _, id := range libraries {
		if !slices.ContainsFunc(p.Wants, func(w WantedLibrary) bool { return w.ID == id }) {
			if _, err := e.theirLibraries(ctx, p, false); err != nil {
				return err
			}
			break
		}
	}
	err = e.updatePeer(ctx, peer, func(q *PeerRecord) {
		kept := q.Wants[:0]
		for _, w := range q.Wants {
			w.Wanted = slices.Contains(libraries, w.ID)
			if w.Wanted || !w.Gone {
				kept = append(kept, w)
			}
		}
		q.Wants = kept
	})
	if err == nil {
		e.readSoon(peer)
	}
	return err
}

// ReadNow reads a peer's catalog now, and waits for it: how many titles
// the reading kept. Errors: ErrNotFound, ErrNotPaired, ErrUncallable,
// ErrSuspended, ErrNotSupported, ErrUnreachable, ErrBackingOff, and a
// peer's refusals.
func (e *Engine) ReadNow(ctx context.Context, peer string) (int, error) {
	return e.readCatalog(ctx, peer, false)
}

// titlesReading is a titles answer as read: each title and each gone key
// on its own, so that a bad one is skipped alone; the other members of
// the wrong type fail the answer (MSFP catalog section 3.3).
type titlesReading struct {
	Titles  []json.RawMessage `json:"titles"`
	Next    *string           `json:"next"`
	Changes *bool             `json:"changes"`
	Gone    []json.RawMessage `json:"gone"`
	Seq     *int64            `json:"seq"`
}

// readCatalog reads a peer's titles in the libraries wanted of it, and
// keeps them (MSFP catalog section 3.2): only what changed since the
// last reading, when the peer gives changes and the libraries are the
// same; the whole list otherwise, after which titles not seen are
// dropped. A reading that fails keeps what was kept before it.
func (e *Engine) readCatalog(ctx context.Context, id string, background bool) (kept int, err error) {
	l := e.reading.peerLock(id)
	l.Lock()
	defer l.Unlock()
	p, err := e.peerRecord(ctx, id)
	if err != nil {
		return 0, err
	}
	if err := e.usablePeer(p, wire.PartCatalog); err != nil {
		return 0, err
	}
	changes := false
	defer func() {
		if errors.Is(err, ErrBackingOff) {
			return
		}
		e.notify(CatalogReadEnded{Peer: p.ID, Name: p.Name, Titles: kept, Changes: changes, Err: err})
	}()
	var wanted []string
	for _, w := range p.Wants {
		if w.Wanted {
			wanted = append(wanted, w.ID)
		}
	}
	var libs []string
	if len(wanted) > 0 {
		theirs, err := e.theirLibraries(ctx, p, background)
		if err != nil {
			return 0, err
		}
		for _, w := range theirs {
			if w.Wanted && !w.Gone {
				libs = append(libs, w.ID)
			}
		}
	}
	slices.Sort(libs)
	reading := e.newReading()
	if len(libs) == 0 {
		// Nothing wanted that it offers: nothing of it is kept.
		err := e.cfg.Store.Update(ctx, func(tx Tx) error {
			q, err := tx.Peer(id)
			if err != nil || q == nil {
				return err
			}
			q.CatalogRead = CatalogRead{}
			if err := tx.PutPeer(*q); err != nil {
				return err
			}
			return tx.EndReading(id, reading)
		})
		return 0, err
	}
	since := int64(0)
	if slices.Equal(p.CatalogRead.Wants, libs) {
		since = p.CatalogRead.Seq
	}
	var seq int64
	first := true
	for after := ""; ; {
		var ans titlesReading
		in := titlesRequest{Libraries: &libs, After: after, Since: since}
		r, err := e.pageCall(ctx, p, in, &ans, background)
		if err != nil {
			return kept, err
		}
		if r.status != http.StatusOK {
			return kept, e.refused(p, r)
		}
		pageChanges := ans.Changes != nil && *ans.Changes
		if first {
			changes, first = pageChanges, false
			if ans.Seq != nil && *ans.Seq > 0 {
				seq = *ans.Seq
			}
		} else if pageChanges != changes {
			return kept, ErrUnreachable.with(errors.New("a reading mixing changes and the whole list"))
		}
		if len(ans.Titles)+len(ans.Gone) > titlesPage {
			return kept, ErrUnreachable.with(fmt.Errorf("a page of more than %d keys", titlesPage))
		}
		var page []Title
		for _, raw := range ans.Titles {
			if t, ok := e.readTitle(p, raw, libs); ok {
				page = append(page, t)
			}
		}
		var gone []string
		if changes {
			for _, raw := range ans.Gone {
				var k string
				if json.Unmarshal(raw, &k) == nil && len(k) <= maxGoneKey {
					gone = append(gone, k)
				}
			}
		}
		if kept += len(page); kept > maxPeerTitles {
			return kept, ErrUnreachable.with(fmt.Errorf("more than %d titles", maxPeerTitles))
		}
		err = e.cfg.Store.Update(ctx, func(tx Tx) error {
			if len(page) > 0 {
				if err := tx.PutPeerTitles(id, reading, page); err != nil {
					return err
				}
			}
			if len(gone) > 0 {
				return tx.DropPeerTitles(id, gone)
			}
			return nil
		})
		if err != nil {
			return kept, err
		}
		if ans.Next == nil || *ans.Next == "" || *ans.Next <= after {
			break
		}
		after = *ans.Next
	}
	err = e.cfg.Store.Update(ctx, func(tx Tx) error {
		q, err := tx.Peer(id)
		if err != nil || q == nil {
			return cmp.Or[error](err, ErrNotFound)
		}
		if !changes {
			if err := tx.EndReading(id, reading); err != nil {
				return err
			}
		}
		q.CatalogRead = CatalogRead{Seq: seq, Wants: libs}
		return tx.PutPeer(*q)
	})
	return kept, err
}

// pageCall asks for a page; asked to wait a moment (a signed 429 or 503
// with retryAfter of a minute at most), the same page once it's time.
func (e *Engine) pageCall(ctx context.Context, p *PeerRecord, in titlesRequest, out *titlesReading, background bool) (reply, error) {
	for try := 0; ; try++ {
		*out = titlesReading{}
		r, err := e.call(ctx, p, callTitles, in, out, background)
		if err != nil || r.status == http.StatusOK || try == 2 || r.retryAfter <= 0 ||
			time.Duration(r.retryAfter)*time.Second > pageWait {
			return r, err
		}
		t := time.NewTimer(time.Duration(r.retryAfter) * time.Second)
		select {
		case <-ctx.Done():
			t.Stop()
			return r, ctx.Err()
		case <-t.C:
		}
	}
}

// readTitle reads a title of a peer's answer as MSFP catalog section 3.3
// and its content part say; false to skip it.
func (e *Engine) readTitle(p *PeerRecord, raw json.RawMessage, libs []string) (Title, bool) {
	var head struct {
		Key string `json:"key"`
	}
	if wire.DecodeEntry(raw, &head) != nil {
		return nil, false
	}
	k, ok := e.keyOn(p, head.Key)
	if !ok {
		return nil, false
	}
	switch k.Part {
	case wire.PartVideo:
		v, ok := wire.ReadVideoTitle(raw)
		if !ok || !slices.Contains(libs, v.Library) {
			return nil, false
		}
		return &VideoTitle{TitleCommon: TitleCommon{Key: v.Key, Kind: v.Kind, Libraries: []string{v.Library}},
			Audio: v.Audio, Subtitles: v.Subtitles, With: v.With, IDs: v.IDs}, true
	case wire.PartMusic:
		m, ok := wire.ReadMusicTitle(raw)
		if !ok || !slices.Contains(libs, m.Library) {
			return nil, false
		}
		return &MusicTitle{TitleCommon: TitleCommon{Key: m.Key, Kind: m.Kind, Libraries: []string{m.Library}},
			Name: m.Name, Artists: m.Artists, ArtistNames: m.ArtistNames, Album: m.Album, Disc: m.Disc,
			Number: m.Number, Year: m.Year, DurationMs: m.DurationMs, IDs: m.IDs}, true
	}
	return nil, false
}

// dropCatalog drops what is kept of a peer's titles: it can't be called,
// or isn't paired, any more.
func (e *Engine) dropCatalog(ctx context.Context, id string) {
	reading := e.newReading()
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		q, err := tx.Peer(id)
		if err != nil || q == nil {
			return err
		}
		q.CatalogRead = CatalogRead{}
		if err := tx.PutPeer(*q); err != nil {
			return err
		}
		return tx.EndReading(id, reading)
	})
	if err != nil {
		e.log.Warn("msfp: a peer's titles couldn't be dropped", "peer", id, "error", err)
	}
}

// Playable says whether this server's people may be offered a peer's
// titles of a kind ("movie", "episode", "track"…) now: the peer is
// paired, not suspended, can be called, and playback, media and the
// title's content part are on with it (engine.md section 3.3).
func (e *Engine) Playable(peer, kind string) bool {
	p, err := e.peerRecord(context.Background(), peer)
	if err != nil {
		return false
	}
	part := wire.PartVideo
	if kind == "artist" || kind == "album" || kind == "track" {
		part = wire.PartMusic
	}
	return e.usablePeer(p, wire.PartPlayback) == nil && e.on(p, wire.PartMedia) && e.on(p, part)
}
