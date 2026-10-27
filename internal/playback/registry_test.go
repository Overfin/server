package playback

import (
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
