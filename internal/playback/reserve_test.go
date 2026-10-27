package playback

import (
	"fmt"
	"testing"
)

// playing lists an account's playbacks, oldest first, by the profiles
// that started them.
func playing(profiles ...string) []*Stream {
	var out []*Stream
	for i, p := range profiles {
		out = append(out, &Stream{ID: fmt.Sprintf("play_%d", i), ProfileID: p})
	}
	return out
}

// With a limit of 3: one profile may watch in 3 places; two profiles, one
// of them in 2 places while the other keeps its own; three profiles, one
// place each. Nobody else's playback ever stops to make room.
func TestReservedStreams(t *testing.T) {
	for _, c := range []struct {
		name     string
		profiles int
		active   []*Stream
		who      string
		replace  int // how many of who's own stop first
		ok       bool
	}{
		{"one profile, a first", 1, playing(), "a", 0, true},
		{"one profile, a third", 1, playing("a", "a"), "a", 0, true},
		{"one profile, a fourth replaces its oldest", 1, playing("a", "a", "a"), "a", 1, true},

		{"two profiles, the second place of one", 2, playing("a"), "a", 0, true},
		{"two profiles, the other's own is kept", 2, playing("a", "a"), "b", 0, true},
		{"two profiles, a third place replaces its own", 2, playing("a", "a"), "a", 1, true},
		{"two profiles, the shared one is taken", 2, playing("a", "a", "b"), "b", 1, true},

		{"three profiles, one place each", 3, playing("a", "b"), "c", 0, true},
		{"three profiles, no second place", 3, playing("a"), "a", 1, true},
		{"three profiles, others' never stop", 3, playing("b", "c"), "a", 0, true},
	} {
		replace, ok := Reserved(c.active, c.who, c.profiles, 3)
		if ok != c.ok || len(replace) != c.replace {
			t.Errorf("%s: replace %d, ok %v", c.name, len(replace), ok)
			continue
		}
		for i, s := range replace {
			if s.ProfileID != c.who {
				t.Errorf("%s: another profile's playback (%d) would stop", c.name, i)
			}
		}
	}
	// The oldest of the profile's own is the one replaced.
	active := playing("b", "a", "c", "a")
	if replace, _ := Reserved(active, "a", 3, 4); len(replace) != 1 || replace[0] != active[1] {
		t.Errorf("not the profile's oldest: %v", replace)
	}
}

// More profiles than streams (the template changed under the account):
// never more than the limit in all, and a profile whose own playbacks
// can't make room is refused.
func TestReservedStreamsOverLimit(t *testing.T) {
	if _, ok := Reserved(playing("a", "b"), "c", 3, 2); ok {
		t.Error("a third stream over a limit of 2")
	}
	if replace, ok := Reserved(playing("a", "b"), "a", 3, 2); !ok || len(replace) != 1 {
		t.Errorf("replacing its own: %v %v", replace, ok)
	}
	// Streams started before the setting was on: a profile using more
	// than the shared ones stops as many of its own as it takes.
	if replace, ok := Reserved(playing("a", "a", "a"), "a", 3, 3); !ok || len(replace) != 3 {
		t.Errorf("its own three: %v %v", replace, ok)
	}
	if SharedStreams(2, 3) != 0 || SharedStreams(3, 1) != 2 {
		t.Error("shared streams")
	}
}
