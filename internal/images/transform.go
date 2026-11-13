package images

import (
	"image"
)

// transform is how every image Overfin keeps is made: raw (untrusted: from
// TMDB, Jellyfin, an upload) checked and decoded (decodeChecked), then
// encoded again as contentType at each of widths (0: as wide as it is;
// never wider than it is). It also gives the original's width. ErrUnsafe
// when raw isn't an image Overfin takes.
//
// In process by default; the backend has it run in worker processes of
// its own (SetTransformer, Workers), so a picture built to exhaust memory
// or crash a decoder takes a worker down, not the backend.
var transform Transformer = InProcess

// Transformer makes images as transform says.
type Transformer func(raw []byte, widths []int, contentType string) (outs [][]byte, width int, err error)

// SetTransformer has every image made by t from now on.
func SetTransformer(t Transformer) { transform = t }

// InProcess makes images in this process.
func InProcess(raw []byte, widths []int, contentType string) ([][]byte, int, error) {
	img, err := decodeChecked(raw)
	if err != nil {
		return nil, 0, err
	}
	outs := make([][]byte, 0, len(widths))
	for _, w := range widths {
		var sized image.Image = img
		if w > 0 {
			sized = Shrink(img, w)
		}
		data, err := encode(sized, contentType)
		if err != nil {
			return nil, 0, err
		}
		outs = append(outs, data)
	}
	return outs, img.Bounds().Dx(), nil
}
