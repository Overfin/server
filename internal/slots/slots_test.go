package slots

import (
	"context"
	"testing"
	"time"
)

// Size run at once, Queue more wait, the next is refused at once; a wait
// ends after Wait; freeing lets a waiting one in; keys are apart.
func TestSlots(t *testing.T) {
	s := &Slots{Size: 2, Queue: 1, Wait: 50 * time.Millisecond}
	ctx := context.Background()
	a, ok1 := s.Take(ctx, "p")
	_, ok2 := s.Take(ctx, "p")
	if !ok1 || !ok2 {
		t.Fatal("the first two")
	}
	if _, ok := s.Take(ctx, "q"); !ok {
		t.Fatal("another key")
	}
	start := time.Now()
	if _, ok := s.Take(ctx, "p"); ok || time.Since(start) < 40*time.Millisecond {
		t.Fatal("a third waits, then is refused")
	}
	got := make(chan bool)
	go func() { _, ok := s.Take(ctx, "p"); got <- ok }()
	for s.InUse("p") < 3 {
		time.Sleep(time.Millisecond)
	}
	start = time.Now()
	if _, ok := s.Take(ctx, "p"); ok || time.Since(start) > 20*time.Millisecond {
		t.Fatal("the queue is full: refused at once")
	}
	a()
	a() // freed once only
	if !<-got {
		t.Fatal("the waiting one gets the freed slot")
	}
	if n := s.InUse("p"); n != 2 {
		t.Fatalf("in use: %d", n)
	}
}
