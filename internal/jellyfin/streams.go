package jellyfin

import (
	"context"
)

// MediaStream is one stream of an item's file as Jellyfin lists it (the
// catalog's languages and frames, before Overfin read the file itself).
type MediaStream struct {
	Index    int
	Type     string
	Codec    string
	Language string
	// Title is the track's own, as the file names it ("VFQ", "Commentary");
	// DisplayTitle, Jellyfin's, made of its language and format.
	Title                string
	DisplayTitle         string
	Channels             int
	IsDefault            bool
	IsForced             bool
	IsHearingImpaired    bool
	IsTextSubtitleStream bool
	// IsExternal: a subtitle file beside the video, not one of its streams.
	IsExternal bool
	Width      int
	Height     int
	VideoRange string
	// For the player's information: SDR, HDR10, HDR10Plus, DOVIWithHDR10…
	VideoRangeType string
	Profile        string
	BitDepth       int
	BitRate        int // bits per second
}

// Segment is a marked part of an item, such as its intro.
type Segment struct {
	Type       string
	StartTicks int64
	EndTicks   int64
}

// MediaSegments lists an item's marked parts.
func (c *Client) MediaSegments(ctx context.Context, a Auth, itemID string) ([]Segment, error) {
	id, err := NormalizeID(itemID)
	if err != nil {
		return nil, err
	}
	var out struct{ Items []Segment }
	if err := c.get(ctx, "media segments", "/MediaSegments/"+id, a, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}
