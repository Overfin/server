package overfin

import (
	"context"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go"
)

// The federation page waits for the engine's next event: past the count
// it knows, at once; else until the next bump, which wakes every waiter;
// or until it gives up, with the count then.
func TestFedChangesWait(t *testing.T) {
	var c fedChanges
	ctx := context.Background()
	c.bump()
	if got := c.Wait(ctx, 0); got != 1 {
		t.Fatalf("past the count: %d", got)
	}
	woke := make(chan uint64, 3)
	for range 3 {
		go func() { woke <- c.Wait(ctx, 1) }()
	}
	time.Sleep(50 * time.Millisecond)
	select {
	case got := <-woke:
		t.Fatalf("woke with nothing new: %d", got)
	default:
	}
	c.bump()
	for range 3 {
		select {
		case got := <-woke:
			if got != 2 {
				t.Errorf("woke at %d", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a waiter not woken")
		}
	}
	short, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if got := c.Wait(short, 2); got != 2 {
		t.Errorf("given up: %d", got)
	}
}

func TestEventName(t *testing.T) {
	if got := eventName(msfp.PeerPaired{}); got != "PeerPaired" {
		t.Errorf("%q", got)
	}
	if got := eventName(nil); got != "<nil>" {
		t.Errorf("nil: %q", got)
	}
}
