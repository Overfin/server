package store

import (
	"database/sql"
	"encoding/json"
	"slices"

	"github.com/msfp-io/msfp-go/wire"
)

// Picture is a copy's picture as the index keeps it and title pages show
// it: its height stepped (for a picture wider than 16:9, a 16:9
// picture's as wide: 2160 for 3840 × 1600), and its high dynamic range
// formats, "hdr10", "hlg" or "dv" (none: standard range).
//
// It is Overfin's own: msfp-go's revised v0 has no picture summary
// (copies carry their facts), so the type that was msfp.Picture lives
// here, with the same JSON, the rows kept read as before.
type Picture struct {
	Height int      `json:"height"`
	HDR    []string `json:"hdr,omitempty"`
}

// The heights a picture is stepped to, and the HDR formats it may list,
// in their order.
var (
	pictureHeights = []int{480, 720, 1080, 1440, 2160, 4320}
	hdrFormats     = []string{"hdr10", "hlg", "dv"}
)

// stepHeight steps a height down to the greatest of pictureHeights not
// above it, 480 below 480; 0 stays 0, unknown.
func stepHeight(h int) int {
	if h <= 0 {
		return 0
	}
	out := pictureHeights[0]
	for _, s := range pictureHeights {
		if s <= h {
			out = s
		}
	}
	return out
}

// cleanHDR keeps of formats those hdrFormats lists, each once, in its
// order; none for standard range.
func cleanHDR(formats []string) []string {
	var out []string
	for _, f := range hdrFormats {
		if slices.Contains(formats, f) {
			out = append(out, f)
		}
	}
	return out
}

// Better says whether a copy of picture p is better than one of q: of a
// greater height, then with more HDR formats (HDR before none).
func (p Picture) Better(q Picture) bool {
	if p.Height != q.Height {
		return p.Height > q.Height
	}
	return len(p.HDR) > len(q.HDR)
}

// BestPicture is the best of pictures, each stepped: the greatest
// height, then HDR before none, then more formats; false when no
// picture's size is known.
func BestPicture(pictures []Picture) (Picture, bool) {
	var best Picture
	for _, p := range pictures {
		w := Picture{Height: stepHeight(p.Height), HDR: cleanHDR(p.HDR)}
		if w.Height > 0 && (best.Height == 0 || w.Better(best)) {
			best = w
		}
	}
	return best, best.Height > 0
}

// readPicture reads a copy's picture as kept (copyindex.go); false when
// its size isn't known.
func readPicture(s sql.NullString) (Picture, bool) {
	var p Picture
	if !s.Valid || json.Unmarshal([]byte(s.String), &p) != nil || p.Height <= 0 {
		return Picture{}, false
	}
	return p, true
}

// copyPicture is the picture of a peer's copy, as the index keeps a
// peer's title's (peer_titles.video): its height (a 16:9 picture's as
// wide, for a wider one) and its range from the copy's facts (Dolby
// Vision, then PQ as hdr10, HLG as hlg); false when the copy has no
// picture or its size isn't known.
func copyPicture(c wire.Copy) (Picture, bool) {
	v := c.Video
	if v == nil {
		return Picture{}, false
	}
	h := v.Height
	if v.Width*9 > v.Height*16 {
		h = v.Width * 9 / 16
	}
	var hdr []string
	if v.Colour != nil && v.Colour.Transfer != nil {
		switch *v.Colour.Transfer {
		case wire.TransferPQ:
			hdr = append(hdr, "hdr10")
		case wire.TransferHLG:
			hdr = append(hdr, "hlg")
		}
	}
	if v.DV != nil {
		hdr = append(hdr, "dv")
	}
	return BestPicture([]Picture{{Height: h, HDR: hdr}})
}
