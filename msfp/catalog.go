package msfp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/overfin/server/msfp/wire"
)

// The catalog, on the owning side (MSFP catalog sections 2 and 3): what
// this server offers each peer, the libraries and titles calls, and the
// change log of its own titles, kept by the host (ChangeLog) or by the
// engine from snapshots (OwnLogTx).

// titlesPage is how many keys a titles page holds at most (MSFP catalog
// section 3.1).
const titlesPage = 5000

// changesKept is how long the engine keeps its own change log (catalog
// section 3.2: at least 30 days).
const changesKept = 30 * 24 * time.Hour

// The calls of the catalog and playback parts.
const (
	callLibraries = "libraries"
	callTitles    = "titles"
	callPlay      = "play"
	callProgress  = "progress"
	callStop      = "stop"
)

// ownLog is what the engine knows of its own catalog's log: whether it
// was refreshed since the start, whether the host said it changed since,
// and the highest change number given out (seqSeen): a change of what a
// peer sees is noted at least there, so that a reading before it is
// never answered with changes after it.
type ownLog struct {
	mu        sync.Mutex // one refresh at a time
	refreshed atomic.Bool
	dirty     atomic.Bool
	seqSeen   atomic.Int64
}

// libraryKinds are the kinds of library MSFP lists (catalog section 2).
var libraryKinds = []string{"movies", "tvshows", "music", "musicvideos", "homevideos", "boxsets", "books", "photos", "mixed"}

func cleanKind(kind string) string {
	if slices.Contains(libraryKinds, kind) {
		return kind
	}
	return ""
}

// hostLibraries are the host's libraries, none without a Catalog.
func (e *Engine) hostLibraries(ctx context.Context) ([]Library, error) {
	if e.cfg.Catalog == nil {
		return nil, nil
	}
	return e.cfg.Catalog.Libraries(ctx)
}

// publicIDs gives each host library its public ID, made when it is first
// offered: 96 random bits, never reused (MSFP catalog section 2.1).
func (e *Engine) publicIDs(tx Tx, hostIDs []string) (map[string]string, error) {
	ids, err := tx.LibraryIDs()
	if err != nil {
		return nil, err
	}
	for _, id := range hostIDs {
		if _, ok := ids[id]; ok {
			continue
		}
		b, err := e.random(12)
		if err != nil {
			return nil, err
		}
		pub := wire.Encode64(b)
		if err := tx.PutLibraryID(id, pub); err != nil {
			return nil, err
		}
		ids[id] = pub
	}
	return ids, nil
}

// Offers lists the host's libraries, those offered to the peer marked.
// Error: ErrNotFound.
func (e *Engine) Offers(ctx context.Context, peer string) ([]Library, error) {
	p, err := e.peerRecord(ctx, peer)
	if err != nil {
		return nil, err
	}
	libs, err := e.hostLibraries(ctx)
	if err != nil {
		return nil, err
	}
	for i := range libs {
		libs[i].Offered = slices.Contains(p.Offers, libs[i].ID)
	}
	return libs, nil
}

// SetOffers sets the host libraries offered to a paired peer (IDs that
// aren't the host's are left out). The peer's next reading is a whole
// one, and its playbacks from libraries no longer offered end at once
// (MSFP playback section 6.3). Errors: ErrNotFound, ErrNotPaired.
func (e *Engine) SetOffers(ctx context.Context, peer string, libraries []string) error {
	ours, err := e.hostLibraries(ctx)
	if err != nil {
		return err
	}
	var keep []string
	for _, l := range ours {
		if slices.Contains(libraries, l.ID) && !slices.Contains(keep, l.ID) {
			keep = append(keep, l.ID)
		}
	}
	slices.Sort(keep)
	mark, err := e.seqMark(ctx)
	if err != nil {
		return err
	}
	changed := false
	err = e.cfg.Store.Update(ctx, func(tx Tx) error {
		p, err := tx.Peer(peer)
		switch {
		case err != nil:
			return err
		case p == nil:
			return ErrNotFound
		case p.State != Paired:
			return ErrNotPaired
		}
		had := slices.Sorted(slices.Values(p.Offers))
		if changed = !slices.Equal(had, keep); !changed {
			return nil
		}
		if _, err := e.publicIDs(tx, keep); err != nil {
			return err
		}
		if p.OffersSeq, err = e.offersSeq(tx, mark); err != nil {
			return err
		}
		p.Offers = keep
		return tx.PutPeer(*p)
	})
	if err != nil || !changed {
		return err
	}
	e.endPlaybacks(func(pb *playback) bool { return pb.peer == peer && !slices.Contains(keep, pb.library) }, EndNotOffered)
	return nil
}

// LibraryRemoved is the host saying one of its libraries is gone: it is
// offered to nobody from then on, and peers' playbacks from it end. Its
// public ID is kept, never given to another library.
func (e *Engine) LibraryRemoved(ctx context.Context, id string) error {
	mark, err := e.seqMark(ctx)
	if err != nil {
		return err
	}
	err = e.cfg.Store.Update(ctx, func(tx Tx) error {
		peers, err := tx.Peers()
		if err != nil {
			return err
		}
		for _, p := range peers {
			if i := slices.Index(p.Offers, id); i >= 0 {
				if p.OffersSeq, err = e.offersSeq(tx, mark); err != nil {
					return err
				}
				p.Offers = slices.Delete(p.Offers, i, i+1)
				if err := tx.PutPeer(p); err != nil {
					return err
				}
			}
		}
		return nil
	})
	e.endPlaybacks(func(pb *playback) bool { return pb.library == id }, EndNotOffered)
	return err
}

// SetLimits sets what a peer may use of this server at once (MSFP
// playback section 5), from its next play on. Errors: ErrNotFound,
// ErrInvalidLimits.
func (e *Engine) SetLimits(ctx context.Context, peer string, l Limits) error {
	if err := l.Check(); err != nil {
		return err
	}
	if !l.Audio {
		l.AudioStreams, l.AudioTranscodes = 0, 0
	}
	return e.updatePeer(ctx, peer, func(p *PeerRecord) { p.Limits = l })
}

// CatalogChanged is the host saying its titles changed: a hint. When the
// engine keeps the change log, it reads the titles again before its next
// titles answer, and in the background now.
func (e *Engine) CatalogChanged() {
	if !e.keepsLog() {
		return
	}
	e.own.dirty.Store(true)
	e.background(func(ctx context.Context) {
		if err := e.refreshOwn(ctx); err != nil {
			e.log.Warn("msfp: the catalog's change log couldn't be refreshed", "error", err)
		}
	})
}

// changeLog is the host's own change log, nil when it keeps none.
func (e *Engine) changeLog() ChangeLog {
	cl, _ := e.cfg.Catalog.(ChangeLog)
	return cl
}

// keepsLog says whether the engine keeps the change log: the host has a
// catalog without one, and the store can keep it.
func (e *Engine) keepsLog() bool { return e.logKept }

// catalogSeq is the catalog's change number now, and the oldest it still
// answers changes since (floor); 0, 0 when no log is kept.
func (e *Engine) catalogSeq(ctx context.Context) (seq, floor int64, err error) {
	if cl := e.changeLog(); cl != nil {
		seq, err = cl.Seq(ctx)
		e.sawSeq(seq)
		return seq, 0, err
	}
	if !e.keepsLog() {
		return 0, 0, nil
	}
	if !e.own.refreshed.Load() || e.own.dirty.Load() {
		if err := e.refreshOwn(ctx); err != nil {
			return 0, 0, err
		}
	}
	err = e.cfg.Store.Read(ctx, func(tx Tx) error {
		log := tx.(OwnLogTx)
		var err error
		if seq, err = log.OwnSeq(); err != nil {
			return err
		}
		floor, err = log.OwnFloor()
		return err
	})
	e.sawSeq(seq)
	return seq, floor, err
}

func (e *Engine) sawSeq(seq int64) {
	for {
		old := e.own.seqSeen.Load()
		if seq <= old || e.own.seqSeen.CompareAndSwap(old, seq) {
			return
		}
	}
}

// offersSeq is the change number from which a peer's readings know of a
// change of what it sees (its offers, the parts on), set as its
// OffersSeq inside the change's transaction: changes are answered only
// since it, or later (MSFP catalog section 3.2). With the engine's own
// log, a number of its own; with the host's, one above every number
// given out, and above the host's now (mark, read before the
// transaction): until the host's log moves past it, readings are whole.
func (e *Engine) offersSeq(tx Tx, mark int64) (int64, error) {
	if log, ok := tx.(OwnLogTx); ok && e.keepsLog() {
		return log.BumpOwnSeq()
	}
	return mark + 1, nil
}

// seqMark is the highest change number given out, at least the host's
// catalog's now: read before a transaction that changes what a peer sees.
func (e *Engine) seqMark(ctx context.Context) (int64, error) {
	if e.keepsLog() {
		return 0, nil
	}
	if _, _, err := e.catalogSeq(ctx); err != nil {
		return 0, err
	}
	return e.own.seqSeen.Load(), nil
}

// refreshOwn reads the host's titles whole and notes what changed since
// the snapshot, all under one new change number.
func (e *Engine) refreshOwn(ctx context.Context) error {
	e.own.mu.Lock()
	defer e.own.mu.Unlock()
	e.own.dirty.Store(false)
	libs, err := e.hostLibraries(ctx)
	if err != nil {
		return err
	}
	ids := make([]string, len(libs))
	for i, l := range libs {
		ids[i] = l.ID
	}
	now := map[string]Title{}
	for after := ""; len(ids) > 0; {
		page, err := e.cfg.Catalog.Titles(ctx, ids, after, titlesPage)
		if err != nil {
			return err
		}
		for _, t := range page.Titles {
			c := t.Common()
			if c.Key <= after {
				return ErrInternal.with(errors.New("the host's titles aren't in key order"))
			}
			slices.Sort(c.Libraries)
			now[c.Key], after = t, c.Key
		}
		if !page.More || len(page.Titles) == 0 {
			break
		}
	}
	at := e.now()
	err = e.cfg.Store.Update(ctx, func(tx Tx) error {
		log := tx.(OwnLogTx)
		was, err := log.OwnSnapshot()
		if err != nil {
			return err
		}
		seq, err := log.OwnSeq()
		if err != nil {
			return err
		}
		seq++
		var changes []OwnChange
		for key, t := range now {
			if old, ok := was[key]; !ok || !sameTitle(old, t) {
				changes = append(changes, OwnChange{Seq: seq, Key: key, Title: t, At: at})
			}
		}
		for key := range was {
			if _, ok := now[key]; !ok {
				changes = append(changes, OwnChange{Seq: seq, Key: key, At: at})
			}
		}
		if err := log.TrimOwnChanges(at.Add(-changesKept)); err != nil {
			return err
		}
		if len(changes) == 0 {
			return nil
		}
		if err := log.AppendOwnChanges(changes); err != nil {
			return err
		}
		return log.PutOwnSnapshot(now)
	})
	if err == nil {
		e.own.refreshed.Store(true)
	}
	return err
}

// sameTitle says whether two titles would be sent alike.
func sameTitle(a, b Title) bool {
	x, err1 := MarshalTitle(a)
	y, err2 := MarshalTitle(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

// libraries answers a paired peer with what this server offers it
// (MSFP catalog section 2), and, with playback on, its limits.
func (e *Engine) libraries(req *request) response {
	p := req.peer
	if p == nil || p.State != Paired || wire.DecodeJSON(req.body, &struct{}{}) != nil {
		return refuse(probNotFound)
	}
	if !e.on(p, wire.PartCatalog) {
		return refuse(probNotSupported)
	}
	ctx := req.r.Context()
	ours, err := e.hostLibraries(ctx)
	if err != nil {
		e.log.Warn("msfp: the host's libraries couldn't be read", "error", err)
		return refuse(probInternal)
	}
	var ids map[string]string
	if err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		var err error
		ids, err = tx.LibraryIDs()
		return err
	}); err != nil {
		return refuse(probInternal)
	}
	out := librariesAnswer{Name: e.name(), Libraries: []libraryWire{}}
	for _, l := range ours {
		if pub, ok := ids[l.ID]; ok && slices.Contains(p.Offers, l.ID) {
			out.Libraries = append(out.Libraries, libraryWire{ID: pub, Name: wire.CleanText(l.Name, maxName), Kind: cleanKind(l.Kind)})
		}
	}
	if e.on(p, wire.PartPlayback) {
		out.Limits = limitsOut(p.Limits, e.on(p, wire.PartMusic))
	}
	return answer(out)
}

type librariesAnswer struct {
	Name      string        `json:"name"`
	Libraries []libraryWire `json:"libraries"`
	Limits    *limitsWire   `json:"limits,omitempty"`
}

type libraryWire struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// limitsWire is a limits object as sent (MSFP playback section 5, music
// section 5.1).
type limitsWire struct {
	Streams         int  `json:"streams"`
	Transcodes      int  `json:"transcodes"`
	BitrateKbps     int  `json:"bitrateKbps"`
	AudioStreams    *int `json:"audioStreams,omitempty"`
	AudioTranscodes *int `json:"audioTranscodes,omitempty"`
}

func limitsOut(l Limits, music bool) *limitsWire {
	w := &limitsWire{Streams: l.Streams, Transcodes: l.Transcodes, BitrateKbps: l.BitrateKbps}
	if music && l.Audio {
		w.AudioStreams, w.AudioTranscodes = &l.AudioStreams, &l.AudioTranscodes
	}
	return w
}

// titlesRequest is a titles request (MSFP catalog section 3).
type titlesRequest struct {
	Libraries *[]string `json:"libraries"`
	After     string    `json:"after"`
	Since     int64     `json:"since"`
}

// titlesAnswer is a page of titles. Titles is never null.
type titlesAnswer struct {
	Titles  []any    `json:"titles"`
	Next    string   `json:"next,omitempty"`
	Changes bool     `json:"changes,omitempty"`
	Gone    []string `json:"gone,omitempty"`
	Seq     int64    `json:"seq,omitempty"`
}

// titles answers a paired peer with a page of the titles in the
// libraries it asks for, of those offered to it, of the content parts on
// between the two: the whole list, or what changed since its last
// reading (MSFP catalog section 3).
func (e *Engine) titles(req *request) response {
	p := req.peer
	var in titlesRequest
	if p == nil || p.State != Paired || wire.DecodeJSON(req.body, &in) != nil || in.Libraries == nil || len(in.After) > 512 {
		return refuse(probNotFound)
	}
	if !e.on(p, wire.PartCatalog) {
		return refuse(probNotSupported)
	}
	out := titlesAnswer{Titles: []any{}}
	if e.cfg.Catalog == nil {
		return answer(out)
	}
	ctx := req.r.Context()
	var ids map[string]string
	if err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		var err error
		ids, err = tx.LibraryIDs()
		return err
	}); err != nil {
		return refuse(probInternal)
	}
	// Those asked for that are offered to it, by their public IDs: any
	// other is ignored.
	byPublic := map[string]string{}
	for _, l := range p.Offers {
		if pub, ok := ids[l]; ok {
			byPublic[l] = pub
		}
	}
	var libs []string
	for _, asked := range *in.Libraries {
		for host, pub := range byPublic {
			if pub == asked && !slices.Contains(libs, host) {
				libs = append(libs, host)
			}
		}
	}
	slices.Sort(libs)
	seq, floor, err := e.catalogSeq(ctx)
	if err != nil {
		e.log.Warn("msfp: the catalog's change number couldn't be read", "error", err)
		return refuse(probInternal)
	}
	out.Seq = seq
	if len(libs) == 0 {
		return answer(out)
	}
	var sent []string
	if in.Since > 0 && in.Since >= floor && in.Since <= seq && in.Since >= p.OffersSeq {
		ok, err := e.changesPage(ctx, p, libs, byPublic, in, &out, &sent)
		if err != nil {
			e.log.Warn("msfp: the catalog's changes couldn't be read", "error", err)
			return refuse(probInternal)
		}
		if !ok {
			out = titlesAnswer{Titles: []any{}, Seq: seq}
			sent = nil
		}
	}
	if !out.Changes {
		if err := e.wholePage(ctx, p, libs, byPublic, in.After, &out, &sent); err != nil {
			e.log.Warn("msfp: the catalog's titles couldn't be read", "error", err)
			return refuse(probInternal)
		}
	}
	if len(sent) > 0 {
		if err := e.cfg.Store.Update(ctx, func(tx Tx) error { return tx.MarkSent(p.ID, sent) }); err != nil {
			return refuse(probInternal)
		}
	}
	return answer(out)
}

// wholePage fills a whole-list page: up to 5,000 titles after after,
// with next set only on a full page (MSFP catalog section 3.1). Titles
// the peer can't be sent are skipped, and the host asked for more.
func (e *Engine) wholePage(ctx context.Context, p *PeerRecord, libs []string, pub map[string]string, after string,
	out *titlesAnswer, sent *[]string) error {
	for len(out.Titles) < titlesPage {
		page, err := e.cfg.Catalog.Titles(ctx, libs, after, titlesPage-len(out.Titles))
		if err != nil {
			return err
		}
		for _, t := range page.Titles {
			key := t.Common().Key
			if key <= after {
				return ErrInternal.with(errors.New("the host's titles aren't in key order"))
			}
			after = key
			if w, ok := e.titleOut(p, t, libs, pub); ok {
				out.Titles = append(out.Titles, w)
				*sent = append(*sent, key)
			}
		}
		if !page.More || len(page.Titles) == 0 {
			return nil
		}
	}
	out.Next = after
	return nil
}

// changesPage fills a changes page: what changed after in.Since, titles
// new or changed and keys gone (of those the peer was ever sent), 5,000
// keys looked at at most; false when the log doesn't reach back that far.
func (e *Engine) changesPage(ctx context.Context, p *PeerRecord, libs []string, pub map[string]string, in titlesRequest,
	out *titlesAnswer, sent *[]string) (bool, error) {
	var present []Title
	var gone []string
	var last string
	more := false
	if cl := e.changeLog(); cl != nil {
		page, err := cl.Changes(ctx, libs, in.Since, in.After, titlesPage)
		if errors.Is(err, ErrTooOld) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if len(page.Present)+len(page.Gone) > titlesPage {
			return false, ErrInternal.with(fmt.Errorf("a page of changes of more than %d keys", titlesPage))
		}
		present, gone, last, more = page.Present, page.Gone, page.Last, page.More
	} else {
		var changes []OwnChange
		err := e.cfg.Store.Read(ctx, func(tx Tx) error {
			var err error
			changes, err = tx.(OwnLogTx).OwnChanges(in.Since, in.After, titlesPage)
			return err
		})
		if err != nil {
			return false, err
		}
		for _, c := range changes {
			last = c.Key
			if c.Title != nil && overlaps(c.Title.Common().Libraries, libs) {
				present = append(present, c.Title)
			} else {
				gone = append(gone, c.Key)
			}
		}
		more = len(changes) == titlesPage
	}
	out.Changes = true
	for _, t := range present {
		if w, ok := e.titleOut(p, t, libs, pub); ok {
			out.Titles = append(out.Titles, w)
			*sent = append(*sent, t.Common().Key)
		} else {
			gone = append(gone, t.Common().Key)
		}
	}
	// gone names only titles the peer was told of (MSFP catalog section
	// 3.2), else it would learn of titles it never could see.
	if len(gone) > 0 {
		err := e.cfg.Store.Read(ctx, func(tx Tx) error {
			var err error
			gone, err = tx.Sent(p.ID, gone)
			return err
		})
		if err != nil {
			return false, err
		}
		slices.Sort(gone)
		out.Gone = gone
	}
	if more && last > in.After {
		out.Next = last
	}
	return true, nil
}

func overlaps(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

// titleOut is a title of the host as sent to p: false when its key isn't
// canonical (with a warning: the host's bug), of a content part off with
// p, or in none of the libraries asked. Its library is the one asked
// whose public ID comes first in byte order (MSFP catalog section 3).
func (e *Engine) titleOut(p *PeerRecord, t Title, libs []string, pub map[string]string) (any, bool) {
	c := t.Common()
	k, ok := wire.ParseKey(c.Key)
	if !ok || k.Kind != c.Kind {
		e.log.Warn("msfp: a title of the host's catalog has a key MSFP doesn't take; it isn't sent", "key", c.Key)
		return nil, false
	}
	if !e.on(p, k.Part) {
		return nil, false
	}
	library := ""
	for _, l := range c.Libraries {
		if slices.Contains(libs, l) && (library == "" || pub[l] < library) {
			library = pub[l]
		}
	}
	if library == "" {
		return nil, false
	}
	switch t := t.(type) {
	case *VideoTitle:
		return videoOut(k, t, library), true
	case *MusicTitle:
		if w, ok := musicOut(k, t, library); ok {
			return w, true
		}
		e.log.Warn("msfp: a music title of the host's catalog has no name; it isn't sent", "key", c.Key)
	}
	return nil, false
}

// videoTitleWire is a video title as sent (MSFP video section 3).
type videoTitleWire struct {
	Key       string            `json:"key"`
	Kind      string            `json:"kind"`
	Library   string            `json:"library"`
	Audio     []string          `json:"audio,omitempty"`
	Subtitles []string          `json:"subtitles,omitempty"`
	With      []string          `json:"with,omitempty"`
	IDs       map[string]string `json:"ids,omitempty"`
}

// videoOut is the host's video title as sent: its members cleaned as a
// receiver would keep them, so nothing leaves that MSFP doesn't take.
func videoOut(k wire.TitleKey, t *VideoTitle, library string) videoTitleWire {
	w := videoTitleWire{Key: t.Key, Kind: t.Kind, Library: library}
	if k.Playable {
		w.Audio, w.Subtitles = wire.CleanTags(t.Audio), wire.CleanTags(t.Subtitles)
	}
	if k.Kind == "episode" {
		vk, _ := wire.ParseVideoKey(t.Key)
		w.With = wire.CleanWith(vk, t.With)
		slices.Sort(w.With)
	}
	if k.Kind != "season" {
		for scheme, v := range t.IDs {
			if wire.OtherID(scheme, v) {
				if w.IDs == nil {
					w.IDs = map[string]string{}
				}
				w.IDs[scheme] = v
			}
		}
	}
	return w
}

// musicTitleWire is a music title as sent (MSFP music section 3).
type musicTitleWire struct {
	Key         string            `json:"key"`
	Kind        string            `json:"kind"`
	Library     string            `json:"library"`
	Name        string            `json:"name"`
	Artists     []string          `json:"artists,omitempty"`
	ArtistNames string            `json:"artistNames,omitempty"`
	Album       string            `json:"album,omitempty"`
	Disc        int               `json:"disc,omitempty"`
	Number      int               `json:"number,omitempty"`
	Year        int               `json:"year,omitempty"`
	DurationMs  int64             `json:"durationMs,omitempty"`
	IDs         map[string]string `json:"ids,omitempty"`
}

// musicOut is the host's music title as sent, read back as a receiver
// reads it: what a receiver would skip or drop isn't sent.
func musicOut(_ wire.TitleKey, t *MusicTitle, library string) (musicTitleWire, bool) {
	w := musicTitleWire{Key: t.Key, Kind: t.Kind, Library: library, Name: t.Name, Artists: t.Artists,
		ArtistNames: t.ArtistNames, Album: t.Album, Disc: t.Disc, Number: t.Number, Year: t.Year,
		DurationMs: t.DurationMs, IDs: t.IDs}
	raw, err := marshal(w)
	if err != nil {
		return musicTitleWire{}, false
	}
	m, ok := wire.ReadMusicTitle(raw)
	if !ok {
		return musicTitleWire{}, false
	}
	return musicTitleWire{Key: m.Key, Kind: m.Kind, Library: library, Name: m.Name, Artists: m.Artists,
		ArtistNames: m.ArtistNames, Album: m.Album, Disc: m.Disc, Number: m.Number, Year: m.Year,
		DurationMs: m.DurationMs, IDs: m.IDs}, true
}
