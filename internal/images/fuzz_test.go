package images

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strconv"
	"testing"
)

// Whatever arrives from upstream, Reencode either refuses it or gives back
// a decodable image of the same size, within the limits, in the format
// asked for.
func FuzzReencode(f *testing.F) {
	rgba := image.NewRGBA(image.Rect(0, 0, 8, 6))
	rgba.Set(1, 1, color.RGBA{255, 0, 0, 128})
	pal := image.NewPaletted(image.Rect(0, 0, 3, 3), color.Palette{color.Black, color.White})
	var j, p, g, q bytes.Buffer
	jpeg.Encode(&j, rgba, nil)
	png.Encode(&p, rgba)
	png.Encode(&g, image.NewGray16(image.Rect(0, 0, 2, 5)))
	png.Encode(&q, pal)
	for _, b := range [][]byte{j.Bytes(), withExif(j.Bytes()), p.Bytes(), g.Bytes(), q.Bytes(), j.Bytes()[:50],
		[]byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), nil} {
		f.Add(b, false)
		f.Add(b, true)
	}
	f.Fuzz(func(t *testing.T, raw []byte, asPNG bool) {
		contentType, format := "image/jpeg", "jpeg"
		if asPNG {
			contentType, format = "image/png", "png"
		}
		out, err := Reencode(raw, contentType)
		if err != nil {
			if !errors.Is(err, ErrUnsafe) {
				t.Fatalf("error %v", err)
			}
			return
		}
		in, _, _ := image.DecodeConfig(bytes.NewReader(raw))
		cfg, got, err := image.DecodeConfig(bytes.NewReader(out))
		if err != nil || got != format || cfg.Width != in.Width || cfg.Height != in.Height ||
			cfg.Width > MaxDimension || cfg.Height > MaxDimension {
			t.Fatalf("out: %+v %s %v, in %+v", cfg, got, err, in)
		}
		if _, _, err := image.Decode(bytes.NewReader(out)); err != nil {
			t.Fatalf("out does not decode: %v", err)
		}
	})
}

// Image IDs that parse hold only what Jellyfin paths and cache keys can
// carry.
func FuzzParse(f *testing.F) {
	f.Add("img_" + itemID + "_primary_0_" + tag)
	f.Add("img_" + itemID + "_chapter_999_" + tag)
	f.Add("img_" + itemID + "_primary_1000_" + tag)
	f.Fuzz(func(t *testing.T, s string) {
		r, err := Parse(s)
		if err != nil {
			return
		}
		if r.Index < 0 || r.Index > 999 || len(r.ItemID) != 32 || len(r.Tag) != 32 || jellyfinTypes[r.Type] == "" {
			t.Fatalf("%q -> %+v", s, r)
		}
		again, err := Parse("img_" + r.ItemID + "_" + r.Type + "_" + strconv.Itoa(r.Index) + "_" + r.Tag)
		if err != nil || again != r {
			t.Fatalf("%q -> %+v -> %+v %v", s, r, again, err)
		}
	})
}

// A library artwork ID comes back from its path as it went in.
func FuzzParseArtwork(f *testing.F) {
	f.Add("tmdbart_poster_abcdefgh123_jpg")
	f.Add("tmdbart_profile_" + "x1234567" + "_png")
	f.Add("tmdbart_logo_short_jpg")
	f.Fuzz(func(t *testing.T, s string) {
		r, err := ParseArtwork(s)
		if err != nil {
			return
		}
		if ArtworkID(r.Type, r.Path) != s {
			t.Fatalf("%q -> %+v -> %q", s, r, ArtworkID(r.Type, r.Path))
		}
		if _, ok := tmdbWidths[r.Type]; !ok {
			t.Fatalf("%q -> type %q", s, r.Type)
		}
	})
}
