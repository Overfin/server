// Package playback keeps the playbacks Overfin serves: the registry of
// stream tickets (random values that map, inside Overfin only, to what a
// playback streams: SECURITY.md rules 9, 10 and 45), the limits a new
// playback starts under, and the checks on the media playlists a delivery
// is served by. What plays is chosen by the player from a title's copies
// and carried out by package delivery; nothing here decides it.
package playback

import "net/url"

// Upstream is how Overfin fetches a part of a stream from Jellyfin (a
// delivery's segment). It never leaves the server: its query may carry
// Jellyfin's token.
type Upstream struct {
	Path  string
	Query url.Values
}
