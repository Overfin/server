package store

import (
	"testing"
	"time"
)

// A film is out by its home release, else 90 days after cinemas, else, with
// no date at all, only from a year before.
func TestHomeReleaseOutBy(t *testing.T) {
	day := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		r    HomeRelease
		want bool
	}{
		{HomeRelease{AtHome: "2026-10-27"}, true},
		{HomeRelease{AtHome: "2026-10-28", Released: "2025-01-01"}, false},
		{HomeRelease{Released: "2026-07-29"}, true},
		{HomeRelease{Released: "2026-08-01"}, false},
		{HomeRelease{Year: 2025}, true},
		{HomeRelease{Year: 2026}, false},
		{HomeRelease{}, false},
	} {
		if got := c.r.OutBy(day); got != c.want {
			t.Errorf("%+v: %v", c.r, got)
		}
	}
}
