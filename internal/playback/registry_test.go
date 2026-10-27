package playback

import (
	"strconv"
	"sync"
	"testing"
	"time"

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
// (rules 30 and 33).
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
		return &Stream{AccountID: "acct_" + strconv.Itoa(i), ProfileID: "prof", Decision: Decision{Method: "transcode"}}
	}
	if got, _, tk := at(Limits{MaxTranscodes: 1}, transcode); got != 1 || tk.Transcodes() != 1 {
		t.Errorf("transcodes: %d admitted, %d running", got, tk.Transcodes())
	}
}

// What Admit refuses, and what it replaces.
func TestAdmit(t *testing.T) {
	tk := NewTickets(time.Hour)
	now := time.Now()
	old := &Stream{AccountID: "acct_a", ProfileID: "prof_a", Decision: Decision{Method: "transcode"}}
	if _, err := tk.Admit(old, 0, now, Limits{MaxStreams: 1, MaxTranscodes: 1}, ""); err != nil {
		t.Fatal(err)
	}
	// Another account's transcode: every one taken.
	other := &Stream{AccountID: "acct_b", ProfileID: "prof_b", Decision: Decision{Method: "transcode"}}
	if _, err := tk.Admit(other, 0, now, Limits{MaxStreams: 1, MaxTranscodes: 1}, ""); err != ErrServerBusy {
		t.Errorf("another account's transcode: %v", err)
	}
	// The same account's replaces its oldest, freeing its transcode.
	newer := &Stream{AccountID: "acct_a", ProfileID: "prof_a", Decision: Decision{Method: "transcode"}}
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
