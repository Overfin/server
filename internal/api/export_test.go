package api

import (
	"time"

	"github.com/overfin/server/internal/slots"
)

// SetMediaBounds sets the bounds of media fetches at once, for a test:
// on this server as the owning one (per ticket) and as the viewer's (per
// playback). It gives what puts them back.
func SetMediaBounds(perTicket, perPlayback *slots.Slots) (undo func()) {
	t, p := servedPerTicket, relayPerPlayback
	servedPerTicket, relayPerPlayback = perTicket, perPlayback
	return func() { servedPerTicket, relayPerPlayback = t, p }
}

// SetSmallReadTime sets how long a relayed playlist or subtitle part may
// take to read whole, for a test, and gives what puts it back.
func SetSmallReadTime(d time.Duration) (undo func()) {
	old := smallReadTime
	smallReadTime = d
	return func() { smallReadTime = old }
}
