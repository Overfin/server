package playback

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/overfin/server/internal/ids"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/tracks"
)

// Stream is one active playback, as Overfin keeps it. Playback state is
// short-lived and only kept in memory (rule 40).
type Stream struct {
	ID            string // play_…, the playback's public ID
	Ticket        string // secret, in stream URLs only
	AccountID     string
	ProfileID     string
	SessionID     string // the browser session that started it (rule 30)
	Device        string // a readable name, shown when another device takes over
	ItemID        string // Jellyfin IDs
	SourceID      string
	PlaySessionID string
	Decision      Decision
	Auth          jellyfin.Auth // to report to Jellyfin after the request is gone
	AudioIndex    *int
	SubtitleIndex *int
	View          any // the session as last sent to the client, for restoring
	// Trickplay is the set of seek-bar previews offered with it, if any.
	Trickplay *jellyfin.Trickplay
	// Remote, for a title a paired server holds: the playback is there,
	// its stream fetched from there; this one keeps the profile's side
	// (where it is, its limits).
	Remote *Remote
	// Origin is the site allowed to fetch it from another address: a
	// paired server's (its viewers play straight from here); "" for this
	// server's own.
	Origin string

	// over: see Over; made when the playback starts.
	over chan struct{}

	// Guarded by the registry's lock.
	// reencodes: a remux found to re-encode its video (Reencodes).
	reencodes     bool
	started       time.Time
	lastSeen      time.Time
	positionTicks int64
	paused        bool
	// title is what plays, in words, for the control panel (Describe).
	title string

	mu sync.Mutex
	// files maps the neutral names in rewritten playlists to the real
	// upstream URLs. It grows as playlists are fetched.
	files map[string]Upstream

	// Checked runs once: whether a remux really copies the video.
	Checked sync.Once
}

// Over is closed when the playback ends, however it ends (stopped,
// replaced, idle), so a response still flowing ends with it (rule 30).
func (s *Stream) Over() <-chan struct{} { return s.over }

// AddFiles records the real URLs behind a rewritten playlist's names.
func (s *Stream) AddFiles(files map[string]Upstream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.files == nil {
		s.files = map[string]Upstream{}
	}
	for k, v := range files {
		s.files[k] = v
	}
}

// File returns the real URL behind a neutral name.
func (s *Stream) File(name string) (Upstream, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.files[name]
	return u, ok
}

// TextSubtitle reports whether index is a text subtitle track of the
// stream's version, the only kind served as WebVTT.
func (s *Stream) TextSubtitle(index int) bool {
	for _, ms := range s.Decision.Source.MediaStreams {
		if ms.Type == "Subtitle" && ms.Index == index && ms.IsTextSubtitleStream {
			return true
		}
	}
	return false
}

// PictureSubtitle reports whether index is a subtitle track of pictures
// (PGS) of the stream's version, served as the track itself, checked
// (CheckPGS).
func (s *Stream) PictureSubtitle(index int) bool {
	for _, ms := range s.Decision.Source.MediaStreams {
		if ms.Type == "Subtitle" && ms.Index == index && !ms.IsTextSubtitleStream && tracks.IsPGS(ms.Codec) {
			return true
		}
	}
	return false
}

// PlayMethod is the stream's method in Jellyfin's terms.
func (s *Stream) PlayMethod() string {
	switch s.Decision.Method {
	case "direct":
		return "DirectPlay"
	case "remux":
		return "DirectStream"
	}
	return "Transcode"
}

// Ended remembers why a playback stopped, so its device can be told.
type Ended struct {
	Reason string // "taken-over", "expired", "stopped" or "ended-by-admin"
	By     string // for "taken-over": the device that took over
	at     time.Time
}

// Snapshot is a stream's activity at one moment.
type Snapshot struct {
	PositionTicks int64
	Paused        bool
}

// Tickets is the registry of playbacks.
type Tickets struct {
	mu       sync.Mutex
	byTicket map[string]*Stream
	byID     map[string]*Stream
	ended    map[string]Ended
	// devices are the Jellyfin device IDs in use, by live playbacks or by
	// ones starting.
	devices map[string]bool
	// MaxAge is a hard limit on any playback, however active.
	MaxAge time.Duration
}

// endedFor is how long the reason a playback ended is remembered.
const endedFor = time.Hour

// NewTickets returns an empty registry.
func NewTickets(maxAge time.Duration) *Tickets {
	return &Tickets{byTicket: map[string]*Stream{}, byID: map[string]*Stream{}, ended: map[string]Ended{},
		devices: map[string]bool{}, MaxAge: maxAge}
}

// Add registers a stream and gives it its public ID and secret ticket.
func (t *Tickets) Add(s *Stream, startTicks int64, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.add(s, startTicks, now)
}

func (t *Tickets) add(s *Stream, startTicks int64, now time.Time) {
	s.ID = ids.New("play")
	s.Ticket = random(24)
	s.started, s.lastSeen, s.positionTicks = now, now, startTicks
	s.over = make(chan struct{})
	t.byTicket[s.Ticket] = s
	t.byID[s.ID] = s
}

// Limits are what a new playback starts under (rules 31 and 32).
type Limits struct {
	MaxStreams int  // the account's stream limit, 0 for none
	Reserve    bool // a stream reserved for each profile
	Profiles   int  // how many profiles the account has, with Reserve
	// RefuseOverLimit refuses the new playback instead of replacing the
	// account's oldest.
	RefuseOverLimit bool
	MaxTranscodes   int // in total, 0 for none
	// AccountTranscodes caps the account's own transcodes (a paired
	// server's); nil: no cap of its own.
	AccountTranscodes *int
}

var (
	// ErrStreamLimit: the account's stream limit is reached and nothing
	// may be replaced.
	ErrStreamLimit = errors.New("playback: stream limit reached")
	// ErrServerBusy: the playback needs a transcode and every one is
	// taken.
	ErrServerBusy = errors.New("playback: every transcode taken")
)

// Admit checks a new playback against the limits and, when it may start,
// ends the playbacks it replaces (the account's oldest, or with Reserve
// the profile's own oldest) and registers it, as Add does. All of it
// happens under one lock, so two playbacks starting at once can't both
// take the last stream or transcode. It returns the playbacks it ended,
// for the caller to stop at Jellyfin; by is the device taking over.
func (t *Tickets) Admit(s *Stream, startTicks int64, now time.Time, lim Limits, by string) ([]*Stream, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var replaced []*Stream
	if lim.MaxStreams > 0 {
		active := t.forAccount(s.AccountID)
		if lim.Reserve {
			var ok bool
			replaced, ok = Reserved(active, s.ProfileID, lim.Profiles, lim.MaxStreams)
			if !ok || (len(replaced) > 0 && lim.RefuseOverLimit) {
				return nil, ErrStreamLimit
			}
		} else if len(active) >= lim.MaxStreams {
			if lim.RefuseOverLimit {
				return nil, ErrStreamLimit
			}
			replaced = active[:len(active)-lim.MaxStreams+1]
		}
	}
	// Every transcode taken: refused before anything stops, counting those
	// the playbacks it replaces would free.
	if s.Decision.Method == "transcode" && lim.MaxTranscodes > 0 {
		busy := t.transcodes()
		for _, old := range replaced {
			if old.reencodes || old.Decision.Method == "transcode" {
				busy--
			}
		}
		if busy >= lim.MaxTranscodes {
			return nil, ErrServerBusy
		}
	}
	if s.Decision.Method == "transcode" && lim.AccountTranscodes != nil {
		mine := 0
		for _, old := range t.forAccount(s.AccountID) {
			if old.reencodes || old.Decision.Method == "transcode" {
				mine++
			}
		}
		if mine >= *lim.AccountTranscodes {
			return nil, ErrServerBusy
		}
	}
	for _, old := range replaced {
		t.remove(old, "taken-over", by, now)
	}
	t.add(s, startTicks, now)
	return replaced, nil
}

func (t *Tickets) live(s *Stream, now time.Time) bool {
	return now.Sub(s.started) < t.MaxAge
}

// ByTicket finds a live stream by its ticket, for the session and profile
// that started it only (rule 30). Only such a request counts as activity:
// a ticket used anywhere else keeps nothing alive (rule 33).
func (t *Tickets) ByTicket(ticket, sessionID, profileID string, now time.Time) (*Stream, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.byTicket[ticket]
	if !ok || !t.live(s, now) || s.SessionID != sessionID || s.ProfileID != profileID {
		return nil, false
	}
	s.lastSeen = now
	return s, true
}

// ByPeerTicket finds a live stream a paired server's viewer plays, by its
// ticket alone: such a playback has no session here (its viewer's is on
// the other server). Its use counts as activity, as ByTicket's.
func (t *Tickets) ByPeerTicket(ticket string, now time.Time) (*Stream, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.byTicket[ticket]
	if !ok || !t.live(s, now) || s.Origin == "" || !strings.HasPrefix(s.SessionID, PeerPrefix) {
		return nil, false
	}
	s.lastSeen = now
	return s, true
}

// Remote is a playback on a paired server.
type Remote struct {
	PeerID string // our pairing's
	ID     string // the playback's there
	Key    string // the title's key
	// Told is when the paired server last heard it's still watched (Unix
	// nanoseconds): it's told now and then, paused or not.
	Told atomic.Int64
	// Paused is what the paired server last heard: a pause or a resume
	// is told at once.
	Paused atomic.Bool
}

// Due reports whether the paired server should be told now: every
// interval, and at once on a pause or a resume. A true answer counts as
// told, so of calls at the same time only one gets it.
func (r *Remote) Due(now time.Time, paused bool, every time.Duration) bool {
	last := r.Told.Load()
	if now.UnixNano()-last < int64(every) && r.Paused.Load() == paused {
		return false
	}
	if !r.Told.CompareAndSwap(last, now.UnixNano()) {
		return false
	}
	r.Paused.Store(paused)
	return true
}

// PeerPrefix starts the account, profile and session IDs of a paired
// server's playbacks: "peer:" and the pairing's ID.
const PeerPrefix = "peer:"

// ByID finds a playback by its public ID, only for the profile that owns it
// (rule 23). For a playback that ended, it returns why instead.
func (t *Tickets) ByID(id, profileID string, now time.Time) (*Stream, *Ended, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.byID[id]; ok && s.ProfileID == profileID && t.live(s, now) {
		return s, nil, true
	}
	if e, ok := t.ended[id+"/"+profileID]; ok && now.Sub(e.at) < endedFor {
		return nil, &e, true
	}
	return nil, nil, false
}

// Touch notes a playback's stream is in use now: a long download (a
// paired server's viewer playing a file straight, which sends no progress
// here) keeps it alive while it flows.
func (t *Tickets) Touch(s *Stream, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s.lastSeen = now
}

// Progress records a progress report.
func (t *Tickets) Progress(s *Stream, positionTicks int64, paused bool, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s.positionTicks, s.paused, s.lastSeen = positionTicks, paused, now
}

// Snapshot returns a stream's latest activity.
func (t *Tickets) Snapshot(s *Stream) Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Snapshot{PositionTicks: s.positionTicks, Paused: s.paused}
}

// Remove ends a playback and remembers why. It returns false if the
// playback had already ended, so cleanup runs only once.
func (t *Tickets) Remove(s *Stream, reason, by string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.remove(s, reason, by, now)
}

func (t *Tickets) remove(s *Stream, reason, by string, now time.Time) bool {
	if t.byID[s.ID] != s {
		return false
	}
	delete(t.byID, s.ID)
	delete(t.byTicket, s.Ticket)
	delete(t.devices, s.Auth.DeviceID)
	close(s.over)
	t.ended[s.ID+"/"+s.ProfileID] = Ended{Reason: reason, By: by, at: now}
	for k, e := range t.ended {
		if now.Sub(e.at) >= endedFor {
			delete(t.ended, k)
		}
	}
	return true
}

// Where lists the playbacks match keeps.
func (t *Tickets) Where(match func(*Stream) bool) []*Stream {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []*Stream
	for _, s := range t.byID {
		if match(s) {
			out = append(out, s)
		}
	}
	return out
}

// ForAccount lists an account's playbacks, oldest first.
func (t *Tickets) ForAccount(accountID string) []*Stream {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.forAccount(accountID)
}

func (t *Tickets) forAccount(accountID string) []*Stream {
	var out []*Stream
	for _, s := range t.byID {
		if s.AccountID == accountID {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].started.Before(out[j].started) })
	return out
}

// Live is a playback as it stands now, for the control panel's live view:
// read from memory when the page asks, never kept (rule 40).
type Live struct {
	Stream     *Stream
	Title      string // as Describe set it; "" while starting
	Method     string // as Method says
	Started    time.Time
	PositionMs int64
	Paused     bool
}

// Live lists the live playbacks, oldest first.
func (t *Tickets) Live(now time.Time) []Live {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Live
	for _, s := range t.byID {
		if t.live(s, now) {
			out = append(out, Live{Stream: s, Title: s.title, Method: t.method(s), Started: s.started,
				PositionMs: s.positionTicks / 10_000, Paused: s.paused})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// Describe sets what a playback plays, in words, for the live view.
func (t *Tickets) Describe(s *Stream, title string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s.title = title
}

// Find returns a live playback by its public ID, whoever's it is: for the
// control panel only.
func (t *Tickets) Find(id string, now time.Time) (*Stream, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.byID[id]
	return s, ok && t.live(s, now)
}

// Method is how the playback is served: "direct", "remux" or "transcode",
// a remux found to re-encode its video being a transcode.
func (t *Tickets) Method(s *Stream) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.method(s)
}

func (t *Tickets) method(s *Stream) string {
	if s.reencodes {
		return "transcode"
	}
	return s.Decision.Method
}

// Reencodes records that a remux turned out to re-encode its video: it
// counts as a transcode from then on. The decision itself stays as made.
func (t *Tickets) Reencodes(s *Stream) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s.reencodes = true
}

// Transcodes counts playbacks that make Jellyfin re-encode the media;
// repackaging (remux) costs little and isn't counted.
func (t *Tickets) Transcodes() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.transcodes()
}

func (t *Tickets) transcodes() int {
	n := 0
	for _, s := range t.byID {
		if s.Decision.Method == "transcode" || s.reencodes {
			n++
		}
	}
	return n
}

// Latest returns the most recently started playback of a session by one
// of its profiles.
func (t *Tickets) Latest(sessionID, profileID string, now time.Time) (*Stream, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var best *Stream
	for _, s := range t.byID {
		if s.SessionID == sessionID && s.ProfileID == profileID && t.live(s, now) &&
			(best == nil || s.started.After(best.started)) {
			best = s
		}
	}
	return best, best != nil
}

// Idle lists playbacks with no activity for longer than idle, and those past
// their maximum age.
func (t *Tickets) Idle(now time.Time, idle time.Duration) []*Stream {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []*Stream
	for _, s := range t.byID {
		if now.Sub(s.lastSeen) > idle || !t.live(s, now) {
			out = append(out, s)
		}
	}
	return out
}

// random returns n random bytes as URL-safe text. Tickets get 24 bytes, well
// above the 128-bit minimum (rule 23).
func random(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ReserveDevice returns a Jellyfin device ID no live playback uses:
// prefix-1, prefix-2… Overfin's one Jellyfin account plays everything,
// and Jellyfin keeps one session per device, so each playback gets a
// device of its own. It's freed with its playback (Remove), or by
// ReleaseDevice when the playback doesn't start.
func (t *Tickets) ReserveDevice(prefix string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	for n := 1; ; n++ {
		id := prefix + "-" + strconv.Itoa(n)
		if !t.devices[id] {
			t.devices[id] = true
			return id
		}
	}
}

// ReleaseDevice frees a device reserved for a playback that didn't start.
func (t *Tickets) ReleaseDevice(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.devices, id)
}
