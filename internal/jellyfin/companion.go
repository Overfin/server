package jellyfin

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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

// MaxFrame is the most a frame from Overfin's plugin may weigh.
const MaxFrame = 10 << 20

// Frame fetches one frame of an item's video at a moment (seconds) from
// Overfin's plugin: a JPEG at most 1920 pixels wide, HDR mapped to standard
// range. Its main version: the one whose ID is the item's.
func (c *Client) Frame(ctx context.Context, admin Auth, item string, at float64) ([]byte, error) {
	id, err := NormalizeID(item)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("at", strconv.FormatFloat(at, 'f', 3, 64))
	return c.readSmall(ctx, streamClient, admin, "/Overfin/Frame/"+id+"/"+id, q, MaxFrame)
}

// IndexItem is an item as Overfin's plugin gives it for the index: Jellyfin's
// own field names for what the index keeps, the library it's in, and never a
// path, only a film's file name and its folder's name, or a series' folder
// name.
type IndexItem struct {
	RawItem
	LibraryId  string
	FolderName string
	FileName   string
}

// IndexPage is a page of the index's items, and where the next one starts
// ("" after the last).
type IndexPage struct {
	Items []IndexItem
	Next  string
}

// IndexChange is one change in the plugin's feed.
type IndexChange struct {
	Seq     int64
	Id      string
	Removed bool
}

// IndexChanges is the feed's answer: the changes after a number, or Reset
// when everything must be read again first, from Generation and Last.
type IndexChanges struct {
	Generation string
	Last       int64
	Reset      bool
	Changes    []IndexChange
}

// IndexPage reads the library's films, series, seasons and episodes, by ID,
// after `after` ("" for the first page), at most limit.
func (c *Client) IndexPage(ctx context.Context, admin Auth, after string, limit int) (*IndexPage, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if after != "" {
		id, err := NormalizeID(after)
		if err != nil {
			return nil, err
		}
		q.Set("after", id)
	}
	var out IndexPage
	return &out, c.doQuery(ctx, "the index", http.MethodGet, "/Overfin/Items", q, admin, nil, &out)
}

// IndexItems reads those of ids (at most 200) still in a library.
func (c *Client) IndexItems(ctx context.Context, admin Auth, ids []string) (*IndexPage, error) {
	var out IndexPage
	return &out, c.doQuery(ctx, "the index", http.MethodGet, "/Overfin/Items", url.Values{"ids": {strings.Join(ids, ",")}}, admin, nil, &out)
}

// IndexChanges asks the plugin's feed what changed after since in
// generation ("" for none yet: the answer is a Reset).
func (c *Client) IndexChanges(ctx context.Context, admin Auth, generation string, since int64, limit int) (*IndexChanges, error) {
	q := url.Values{"since": {strconv.FormatInt(since, 10)}, "limit": {strconv.Itoa(limit)}}
	if generation != "" {
		q.Set("generation", generation)
	}
	var out IndexChanges
	return &out, c.doQuery(ctx, "the index's changes", http.MethodGet, "/Overfin/Changes", q, admin, nil, &out)
}
