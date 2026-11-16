package api

import (
	"context"
	"testing"
	"time"
)

// A playback started on a paired server right after one stopped there
// waits for that stop (at most a few seconds): the other server would
// count both against the pairing's limit.
func TestPendingStops(t *testing.T) {
	p := pendingStops{byPeer: map[string]int{}}
	done := p.begin("peer_a")
	go func() { time.Sleep(200 * time.Millisecond); done(); done() }() // twice: counted once
	start := time.Now()
	p.wait(context.Background(), "peer_a", 3*time.Second)
	if waited := time.Since(start); waited < 150*time.Millisecond || waited > time.Second {
		t.Errorf("waited %v", waited)
	}
	start = time.Now()
	p.wait(context.Background(), "peer_b", 3*time.Second) // nothing on its way there
	if time.Since(start) > 50*time.Millisecond {
		t.Error("waited with nothing to wait for")
	}
	stuck := p.begin("peer_c")
	defer stuck()
	start = time.Now()
	p.wait(context.Background(), "peer_c", 300*time.Millisecond)
	if waited := time.Since(start); waited < 250*time.Millisecond || waited > time.Second {
		t.Errorf("a stop that never ends: waited %v", waited)
	}
}
