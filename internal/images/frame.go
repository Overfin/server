package images

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"math"
)

// frameSample is the width of the copy a frame's light is measured on.
const frameSample = 32

// Frame makes a frame of a video Overfin's own: raw (from Overfin's plugin,
// untrusted) checked and decoded as every image from outside is (in a
// worker, when the backend has them), encoded again no wider than the
// widest still served. It also says how the frame looks: its mean
// brightness and how much that varies across it (both 0 to 1), measured on
// a small copy of Overfin's own encoding.
func (p *Pipeline) Frame(ctx context.Context, raw []byte) (data []byte, brightness, contrast float64, err error) {
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, 0, 0, ctx.Err()
	}
	defer func() { <-p.slots }()
	outs, _, err := transform(raw, []int{Largest("still"), frameSample}, "image/jpeg")
	if err != nil {
		return nil, 0, 0, err
	}
	small, err := jpeg.Decode(bytes.NewReader(outs[1]))
	if err != nil {
		return nil, 0, 0, err
	}
	brightness, contrast = light(small)
	return outs[0], brightness, contrast, nil
}

// light is an image's mean luma and its standard deviation, 0 to 1.
func light(img image.Image) (mean, deviation float64) {
	b := img.Bounds()
	var sum, squares float64
	n := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			l := (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(bl)) / 65535
			sum += l
			squares += l * l
			n++
		}
	}
	if n == 0 {
		return 0, 0
	}
	mean = sum / float64(n)
	return mean, math.Sqrt(max(squares/float64(n)-mean*mean, 0))
}
