package jellyfin

import (
	"context"
	"net/url"
	"strconv"
)

// VideoFile is one version of a film or an episode, as the backend makes
// its previews from: where the file is (inside the backend's container,
// which sees what Jellyfin sees), and the previews Jellyfin made of it. Its
// path is for reading the file, never kept, logged or served (rule 78).
type VideoFile struct {
	ItemID, SourceID string
	Path             string
	Size             int64
	RunTimeTicks     int64
	// Trickplay are the preview sets Jellyfin made of it, by width.
	Trickplay []Trickplay
	// NoVideo: Jellyfin read its streams and none is a video (a broken
	// file, sound only); false while it hasn't read them.
	NoVideo bool
	// Container is Jellyfin's name of its container ("mkv", "mov,mp4…").
	Container string
	// Streams are its streams as Jellyfin read them, the subtitle files
	// beside it among them (IsExternal); nil while it hasn't.
	Streams []MediaStream
	// Parts are a stacked film's files (CD1, CD2): its first is Path; 1
	// for one file.
	Parts int
}

// videoFilesPage is how many items a page of VideoFiles asks for.
const videoFilesPage = 500

// VideoFiles lists every film's and episode's files the libraries have,
// with an administrator's key; each page goes to each, in turn, until
// it returns false.
func (c *Client) VideoFiles(ctx context.Context, admin Auth, each func(VideoFile) bool) error {
	return c.videoFiles(ctx, admin, nil, each)
}

// VideoFileOf is one version's file (an item's media source), as
// VideoFiles lists it; a 404 StatusError when it has none.
func (c *Client) VideoFileOf(ctx context.Context, admin Auth, itemID, sourceID string) (VideoFile, error) {
	item, err := NormalizeID(itemID)
	if err != nil {
		return VideoFile{}, err
	}
	var found *VideoFile
	err = c.videoFiles(ctx, admin, url.Values{"ids": {item}}, func(f VideoFile) bool {
		if f.ItemID == item && f.SourceID == sourceID {
			found = &f
			return false
		}
		return true
	})
	if err != nil {
		return VideoFile{}, err
	}
	if found == nil {
		return VideoFile{}, &StatusError{Op: "a video file", Status: 404}
	}
	return *found, nil
}

func (c *Client) videoFiles(ctx context.Context, admin Auth, extra url.Values, each func(VideoFile) bool) error {
	for start := 0; ; start += videoFilesPage {
		var page struct {
			Items []struct {
				Id           string
				MediaSources []struct {
					Id           string
					Path         string
					Protocol     string
					IsRemote     bool
					Size         int64
					RunTimeTicks int64
					Container    string
					MediaStreams []MediaStream
				}
				PartCount *int
				Trickplay map[string]map[string]struct {
					Width, Height, TileWidth, TileHeight, ThumbnailCount, Interval int
				}
			}
			TotalRecordCount int
		}
		v := url.Values{"recursive": {"true"}, "includeItemTypes": {"Movie,Episode"}, "isMissing": {"false"},
			"fields": {"MediaSources,Trickplay"}, "enableImages": {"false"}, "enableUserData": {"false"},
			"startIndex": {strconv.Itoa(start)}, "limit": {strconv.Itoa(videoFilesPage)}, "sortBy": {"DateCreated"}, "sortOrder": {"Descending"}}
		for k, vs := range extra {
			v[k] = vs
		}
		if err := c.get(ctx, "video files", "/Items", admin, v, &page); err != nil {
			return err
		}
		for _, it := range page.Items {
			item, err := NormalizeID(it.Id)
			if err != nil {
				continue
			}
			for _, ms := range it.MediaSources {
				source, err := NormalizeID(ms.Id)
				if err != nil || ms.Path == "" || ms.IsRemote || (ms.Protocol != "" && ms.Protocol != "File") {
					continue
				}
				f := VideoFile{ItemID: item, SourceID: source, Path: ms.Path, Size: ms.Size, RunTimeTicks: ms.RunTimeTicks,
					Container: ms.Container, Streams: ms.MediaStreams, Parts: 1}
				if it.PartCount != nil && *it.PartCount > 1 {
					f.Parts = *it.PartCount
				}
				f.NoVideo = len(ms.MediaStreams) > 0
				for _, st := range ms.MediaStreams {
					if st.Type == "Video" {
						f.NoVideo = false
					}
				}
				for src, byWidth := range it.Trickplay {
					if s, err := NormalizeID(src); err != nil || s != source {
						continue
					}
					for _, t := range byWidth {
						tp := Trickplay{Width: t.Width, Height: t.Height, Columns: t.TileWidth, Rows: t.TileHeight,
							Count: t.ThumbnailCount, IntervalMs: t.Interval}
						if tp.Width > 0 && tp.Width <= 1024 && tp.Height > 0 && tp.Height <= 1024 && tp.Columns > 0 && tp.Columns <= 32 &&
							tp.Rows > 0 && tp.Rows <= 32 && tp.Count > 0 && tp.Count <= 100_000 && tp.IntervalMs >= 1000 {
							f.Trickplay = append(f.Trickplay, tp)
						}
					}
				}
				if !each(f) {
					return nil
				}
			}
		}
		if len(page.Items) < videoFilesPage || start+len(page.Items) >= page.TotalRecordCount {
			return nil
		}
	}
}
