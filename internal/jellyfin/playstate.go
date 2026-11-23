package jellyfin

import (
	"context"
	"net/http"
	"net/url"
)

// StopEncoding ends the transcode behind a playback. Jellyfin doesn't stop
// abandoned transcodes by itself, so Overfin always calls this when a
// playback ends (SECURITY.md rule 50).
func (c *Client) StopEncoding(ctx context.Context, a Auth, deviceID, playSessionID string) error {
	return c.doQuery(ctx, "stop encoding", http.MethodDelete, "/Videos/ActiveEncodings",
		url.Values{"deviceId": {deviceID}, "playSessionId": {playSessionID}}, a, nil, nil)
}
