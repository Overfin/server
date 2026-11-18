package msfp

import (
	"context"
	"sync"
	"time"
)

// slots bounds how many of something run at once, by key (one peer, or
// all of them), with a bounded queue: a request waits for a slot only
// while fewer than queue others wait, and only for wait; otherwise it is
// refused at once (MSFP core section 8.3, items 3 and 5).
type slots struct {
	size, queue int
	wait        time.Duration

	mu   sync.Mutex
	keys map[string]*slotKey
}

type slotKey struct {
	ch    chan struct{}
	users int // holding a slot or waiting for one
}

// take takes a slot of k, waiting as above, and gives the function that
// frees it (once, however often it is called); false when it wasn't
// taken: too many waiting, the wait over, or ctx done.
func (s *slots) take(ctx context.Context, k string) (func(), bool) {
	s.mu.Lock()
	if s.keys == nil {
		s.keys = map[string]*slotKey{}
	}
	e := s.keys[k]
	if e == nil {
		e = &slotKey{ch: make(chan struct{}, s.size)}
		s.keys[k] = e
	}
	if e.users >= s.size+s.queue {
		s.mu.Unlock()
		return nil, false
	}
	e.users++
	s.mu.Unlock()
	leave := func() {
		s.mu.Lock()
		if e.users--; e.users == 0 {
			delete(s.keys, k)
		}
		s.mu.Unlock()
	}
	free := sync.OnceFunc(func() {
		<-e.ch
		leave()
	})
	select {
	case e.ch <- struct{}{}:
		return free, true
	default:
	}
	t := time.NewTimer(s.wait)
	defer t.Stop()
	select {
	case e.ch <- struct{}{}:
		return free, true
	case <-t.C:
	case <-ctx.Done():
	}
	leave()
	return nil, false
}
