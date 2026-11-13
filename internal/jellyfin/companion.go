package jellyfin

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// Overfin's plugin for its Jellyfin (github.com/Overfin/jellyfin-plugin):
// routes under /Overfin/ that answer Overfin's key alone.

// SetPluginKey tells Overfin's plugin which key is Overfin's: the SHA-256
// (hex) of admin's token, in the plugin's configuration. Until then, the
// plugin answers no one.
func (c *Client) SetPluginKey(ctx context.Context, admin Auth, pluginID, keyHash string) error {
	return c.do(ctx, "Overfin's plugin's key", http.MethodPost, "/Plugins/"+pluginID+"/Configuration", admin,
		map[string]string{"KeyHash": keyHash}, nil)
}

// PluginInfo is which plugin and which Jellyfin answer, as the plugin says.
func (c *Client) PluginInfo(ctx context.Context, admin Auth) (plugin, server string, err error) {
	var info struct{ Plugin, Jellyfin string }
	err = c.do(ctx, "Overfin's plugin", http.MethodGet, "/Overfin/Info", admin, nil, &info)
	return info.Plugin, info.Jellyfin, err
}

// SubtitleStretch fetches one subtitle track of a version between two
// times (seconds, the file's own) from Overfin's plugin, which reads the
// file from there: text as WebVTT, pictures (PGS) as a .sup stream. item,
// source and index come from Overfin's own stream state; anything larger
// than max bytes is refused.
func (c *Client) SubtitleStretch(ctx context.Context, admin Auth, item, source string, index int, from, to float64, max int) ([]byte, error) {
	q := url.Values{}
	q.Set("from", strconv.FormatFloat(from, 'f', -1, 64))
	q.Set("to", strconv.FormatFloat(to, 'f', -1, 64))
	return c.readSmall(ctx, streamClient, admin, "/Overfin/Subtitles/"+item+"/"+source+"/"+strconv.Itoa(index), q, max)
}
