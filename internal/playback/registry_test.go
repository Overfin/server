package playback

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/delivery"
	"github.com/overfin/server/internal/jellyfin"
)

// Each playback gets a Jellyfin device of its own, freed when it ends.
func TestDevices(t *testing.T) {
	tk := NewTickets(time.Hour)
	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := tk.ReserveDevice("overfin-play")
			mu.Lock()
			defer mu.Unlock()
			if seen[d] {
				t.Errorf("device %s handed out twice", d)
			}
			seen[d] = true
		}()
	}
	wg.Wait()
	if len(seen) != 20 {
		t.Fatalf("devices: %v", seen)
	}

	// A playback that didn't start gives its device back; one that ended
	// frees it too.
	tk.ReleaseDevice("overfin-play-3")
	if d := tk.ReserveDevice("overfin-play"); d != "overfin-play-3" {
		t.Errorf("after a release: %s", d)
	}
	s := &Stream{ID: "play_1", Auth: jellyfin.Auth{DeviceID: "overfin-play-5"}}
	tk.Add(s, 0, time.Now())
	tk.Remove(s, "stopped", "", time.Now())
	if d := tk.ReserveDevice("overfin-play"); d != "overfin-play-5" {
		t.Errorf("after an end: %s", d)
	}
	if d := tk.ReserveDevice("overfin-play"); d != "overfin-play-21" {
		t.Errorf("next: %s", d)
	}
}

// A ticket used from another session or by another profile finds nothing
// and keeps nothing alive: a leaked stream URL can't hold a transcode
// (rules 45 and 50).
func TestTicketElsewhere(t *testing.T) {
	tk := NewTickets(24 * time.Hour)
	now := time.Now()
	s := &Stream{AccountID: "acct_a", ProfileID: "prof_a", SessionID: "sess_a"}
	tk.Add(s, 0, now)
	for i := 1; i <= 10; i++ {
		at := now.Add(time.Duration(i) * time.Minute)
		if _, ok := tk.ByTicket(s.Ticket, "sess_b", "prof_a", at); ok {
			t.Fatal("found from another session")
		}
		if _, ok := tk.ByTicket(s.Ticket, "sess_a", "prof_b", at); ok {
			t.Fatal("found by another profile")
		}
	}
	if idle := tk.Idle(now.Add(10*time.Minute), 2*time.Minute); len(idle) != 1 || idle[0] != s {
		t.Errorf("uses elsewhere kept the playback alive: idle %v", idle)
	}
	// Its own session's use counts.
	if got, ok := tk.ByTicket(s.Ticket, "sess_a", "prof_a", now.Add(10*time.Minute)); !ok || got != s {
		t.Fatal("not found from its own session")
	}
	if idle := tk.Idle(now.Add(11*time.Minute), 2*time.Minute); len(idle) != 0 {
		t.Errorf("its own use didn't count: idle %v", idle)
	}
}

// A paired server's viewer's playback is never idle here, however long
// unused: the MSFP engine ends it as MSFP says (a download never idles),
// and stops it here. This server's own beside it idles.
func TestPeerPlaybackNeverIdle(t *testing.T) {
	tk := NewTickets(24 * time.Hour)
	now := time.Now()
	peer := &Stream{SessionID: PeerPrefix + "peer_a/play_x"}
	own := &Stream{AccountID: "acct_a", ProfileID: "prof_a", SessionID: "sess_a"}
	tk.Add(peer, 0, now)
	tk.Add(own, 0, now)
	if idle := tk.Idle(now.Add(30*time.Hour), 2*time.Minute); len(idle) != 1 || idle[0] != own {
		t.Errorf("idle: %v", idle)
	}
}

// Playbacks starting at once never get past the limits together: the
// check and the registration are one step (run with -race).
func TestAdmitAtOnce(t *testing.T) {
	const n = 20
	at := func(lim Limits, stream func(i int) *Stream) (admitted, replaced int, tk *Tickets) {
		tk = NewTickets(time.Hour)
		var mu sync.Mutex
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				gone, err := tk.Admit(stream(i), 0, time.Now(), lim, "TV")
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					admitted++
					replaced += len(gone)
				}
			}()
		}
		wg.Wait()
		return admitted, replaced, tk
	}
	own := func(i int) *Stream {
		return &Stream{AccountID: "acct_a", ProfileID: "prof_a", Auth: jellyfin.Auth{DeviceID: "d-" + strconv.Itoa(i)}}
	}

	// Refused over the limit: exactly the limit gets in.
	if got, _, tk := at(Limits{MaxStreams: 2, RefuseOverLimit: true}, own); got != 2 || len(tk.ForAccount("acct_a")) != 2 {
		t.Errorf("refusing: %d admitted, %d playing", got, len(tk.ForAccount("acct_a")))
	}
	// Replacing the oldest: all get in, the limit still holds, and each
	// is replaced once.
	if got, gone, tk := at(Limits{MaxStreams: 2}, own); got != n || gone != n-2 || len(tk.ForAccount("acct_a")) != 2 {
		t.Errorf("replacing: %d admitted, %d replaced, %d playing", got, gone, len(tk.ForAccount("acct_a")))
	}
	// A stream reserved for each of 2 profiles, 3 in all, refusing.
	two := func(i int) *Stream {
		s := own(i)
		s.ProfileID = "prof_" + strconv.Itoa(i%2)
		return s
	}
	if got, _, tk := at(Limits{MaxStreams: 3, Reserve: true, Profiles: 2, RefuseOverLimit: true}, two); got != 3 || len(tk.ForAccount("acct_a")) != 3 {
		t.Errorf("reserved: %d admitted, %d playing", got, len(tk.ForAccount("acct_a")))
	}
	// One transcode for the whole server, across accounts.
	transcode := func(i int) *Stream {
		return &Stream{AccountID: "acct_" + strconv.Itoa(i), ProfileID: "prof", Delivery: transcoding()}
	}
	if got, _, tk := at(Limits{MaxTranscodes: 1}, transcode); got != 1 || tk.Transcodes() != 1 {
		t.Errorf("transcodes: %d admitted, %d running", got, tk.Transcodes())
	}
}

// What Admit refuses, and what it replaces.
func TestAdmit(t *testing.T) {
	tk := NewTickets(time.Hour)
	now := time.Now()
	old := &Stream{AccountID: "acct_a", ProfileID: "prof_a", Delivery: transcoding()}
	if _, err := tk.Admit(old, 0, now, Limits{MaxStreams: 1, MaxTranscodes: 1}, ""); err != nil {
		t.Fatal(err)
	}
	// Another account's transcode: every one taken.
	other := &Stream{AccountID: "acct_b", ProfileID: "prof_b", Delivery: transcoding()}
	if _, err := tk.Admit(other, 0, now, Limits{MaxStreams: 1, MaxTranscodes: 1}, ""); err != ErrServerBusy {
		t.Errorf("another account's transcode: %v", err)
	}
	// The same account's replaces its oldest, freeing its transcode.
	newer := &Stream{AccountID: "acct_a", ProfileID: "prof_a", Delivery: transcoding()}
	gone, err := tk.Admit(newer, 0, now.Add(time.Second), Limits{MaxStreams: 1, MaxTranscodes: 1}, "TV")
	if err != nil || len(gone) != 1 || gone[0] != old {
		t.Fatalf("replacing: %v %v", gone, err)
	}
	if _, e, ok := tk.ByID(old.ID, "prof_a", now); !ok || e == nil || e.Reason != "taken-over" || e.By != "TV" {
		t.Errorf("the replaced one: %+v", e)
	}
	if _, err := tk.Admit(&Stream{AccountID: "acct_a", ProfileID: "prof_a"}, 0, now, Limits{MaxStreams: 1, RefuseOverLimit: true}, ""); err != ErrStreamLimit {
		t.Errorf("refusing: %v", err)
	}
}

// A delivery takes a transcode slot only when it converts the video: a
// remux, and one converting the audio alone, are streams (D8, revised).
func TestDeliveryCounting(t *testing.T) {
	tk := NewTickets(time.Hour)
	now := time.Now()
	lim := Limits{MaxTranscodes: 1}
	with := func(kind string, video, audio bool) *Stream {
		return &Stream{AccountID: "acct_" + kind, ProfileID: "prof", SessionID: "sess",
			Delivery: &delivery.Started{Plan: &delivery.Plan{Kind: kind, VideoConverted: video, AudioConverted: audio}}}
	}
	video := with(delivery.KindTranscode, true, true)
	if _, err := tk.Admit(video, 0, now, lim, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tk.Admit(with(delivery.KindTranscode, true, false), 0, now, lim, ""); err != ErrServerBusy {
		t.Errorf("a second converted picture: %v", err)
	}
	audioOnly := with(delivery.KindTranscode, false, true)
	for _, s := range []*Stream{with(delivery.KindRemux, false, false), audioOnly, with(delivery.KindFile, false, false)} {
		if _, err := tk.Admit(s, 0, now, lim, ""); err != nil {
			t.Errorf("%s: %v", s.Delivery.Plan.Kind, err)
		}
	}
	if n := tk.Transcodes(); n != 1 {
		t.Errorf("transcodes: %d", n)
	}
	if m := tk.Method(audioOnly); m != "transcode" {
		t.Errorf("audio only: method %s", m)
	}
	for _, l := range tk.Live(now) {
		if l.Transcodes != (l.Stream == video) {
			t.Errorf("live: %s transcodes %v", l.Stream.Delivery.Plan.Kind, l.Transcodes)
		}
	}
}

func TestRegistry(t *testing.T) {
	tk := NewTickets(24 * time.Hour)
	now := time.Now()
	old := &Stream{AccountID: "acct_a", ProfileID: "prof_a", SessionID: "sess_a", ItemID: item}
	tk.Add(old, 0, now)
	newer := &Stream{AccountID: "acct_a", ProfileID: "prof_b", ItemID: item}
	tk.Add(newer, 0, now.Add(time.Second))

	if len(old.Ticket) < 32 || !strings.HasPrefix(old.ID, "play_") {
		t.Errorf("ticket %q, id %q", old.Ticket, old.ID)
	}
	if got, ok := tk.ByTicket(old.Ticket, "sess_a", "prof_a", now); !ok || got != old {
		t.Error("ticket not found")
	}
	if _, _, ok := tk.ByID(old.ID, "prof_b", now); ok {
		t.Error("another profile found the playback")
	}
	if list := tk.ForAccount("acct_a"); len(list) != 2 || list[0] != old {
		t.Errorf("ForAccount: oldest first expected, got %v", list)
	}

	// Taken over: the ticket stops working, and the reason is remembered.
	if !tk.Remove(old, "taken-over", "Living room TV", now) {
		t.Fatal("Remove failed")
	}
	if tk.Remove(old, "stopped", "", now) {
		t.Error("a playback was removed twice")
	}
	if _, ok := tk.ByTicket(old.Ticket, "sess_a", "prof_a", now); ok {
		t.Error("an ended playback's ticket still works")
	}
	if _, e, ok := tk.ByID(old.ID, "prof_a", now); !ok || e == nil || e.Reason != "taken-over" || e.By != "Living room TV" {
		t.Errorf("ended: %+v", e)
	}

	// Idle detection: activity through the ticket or progress counts.
	later := now.Add(3 * time.Minute)
	if idle := tk.Idle(later, 2*time.Minute); len(idle) != 1 || idle[0] != newer {
		t.Errorf("idle: %v", idle)
	}
	tk.Progress(newer, 600_000_000, true, later)
	if idle := tk.Idle(later, 2*time.Minute); len(idle) != 0 {
		t.Errorf("a reporting playback was idle")
	}
	if snap := tk.Snapshot(newer); snap.PositionTicks != 600_000_000 || !snap.Paused {
		t.Errorf("snapshot: %+v", snap)
	}
}

// transcoding is a delivery that converts the picture: it takes a
// transcode slot.
func transcoding() *delivery.Started {
	return &delivery.Started{Plan: &delivery.Plan{Kind: delivery.KindTranscode, VideoConverted: true}}
}

const item = "0123456789abcdef0123456789abcdef"
