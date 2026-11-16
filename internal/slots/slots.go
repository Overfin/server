// Package slots bounds how many of something run at once, by key (one
// playback, one paired server…), with a bounded queue: a request waits
// for a slot only while fewer than Queue others wait, and only for Wait;
// otherwise it is refused at once (MSFP core section 8.3, media section
// 2.4). In memory only.
package slots

import (
	"context"
	"sync"
	"time"
)

// Slots is a bound per key. The zero value isn't usable: set Size.
type Slots struct {
	// Size is how many run at once for one key.
	Size int
	// Queue is how many more may wait for a slot of the same key.
	Queue int
	// Wait is how long one waits at most.
	Wait time.Duration

	mu   sync.Mutex
	keys map[string]*key
}

type key struct {
	ch    chan struct{}
	users int // holding a slot or waiting for one
}

// Take takes a slot of k, waiting as above, and gives the function that
// frees it (once, whatever the number of calls); false when it wasn't
// taken: too many waiting, the wait over, or ctx done.
func (s *Slots) Take(ctx context.Context, k string) (func(), bool) {
	s.mu.Lock()
	if s.keys == nil {
		s.keys = map[string]*key{}
	}
	e := s.keys[k]
	if e == nil {
		e = &key{ch: make(chan struct{}, s.Size)}
		s.keys[k] = e
	}
	if e.users >= s.Size+s.Queue {
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
	t := time.NewTimer(s.Wait)
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

// InUse is how many slots of k are taken or waited for.
func (s *Slots) InUse(k string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.keys[k]; e != nil {
		return e.users
	}
	return 0
}
