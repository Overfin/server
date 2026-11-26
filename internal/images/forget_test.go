package images

import (
	"testing"
	"time"
)

// What a profile may see of images is remembered five minutes; forgotten
// for that profile alone when its limits change (another profile whose
// ID starts the same keeps its own), for everyone after a change that
// limits many; expired, asked again; a full cache starts over rather than
// grow.
func TestImageAccessForgotten(t *testing.T) {
	p := New(nil, 1<<20)
	now := time.Now()
	tags := map[string]string{"primary/0": "t"}
	for _, key := range []string{"p1/a", "p1/b", "p10/a", "p2/a"} {
		p.access.allow(key, tags, now)
	}
	p.Forget("p1")
	for key, want := range map[string]bool{"p1/a": false, "p1/b": false, "p10/a": true, "p2/a": true} {
		if _, ok := p.access.allowed(key, now); ok != want {
			t.Errorf("%s after forgetting p1: %v", key, ok)
		}
	}
	p.ForgetAll()
	if _, ok := p.access.allowed("p2/a", now); ok {
		t.Error("kept after forgetting everyone")
	}
	p.access.allow("p3/a", tags, now)
	if _, ok := p.access.allowed("p3/a", now.Add(5*time.Minute)); ok {
		t.Error("kept past five minutes")
	}
	small := newAccessCache(time.Minute, 2)
	small.allow("a", tags, now)
	small.allow("b", tags, now)
	small.allow("c", tags, now)
	if len(small.entries) != 1 {
		t.Errorf("a full cache: %d entries", len(small.entries))
	}
	if _, ok := small.allowed("c", now); !ok {
		t.Error("the newest lost")
	}
}
