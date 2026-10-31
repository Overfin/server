package images

import (
	"image"
	"image/color"
	"testing"
)

func fill(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

// Each pixel is the average of the block it covers: whole blocks keep their
// color; a pixel straddling two gets both, by how much of each it covers.
func TestShrinkAverages(t *testing.T) {
	red, blue := color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	fill(src, image.Rect(0, 0, 2, 2), red)
	fill(src, image.Rect(2, 0, 4, 2), blue)
	got := Shrink(src, 2).(*image.RGBA)
	if got.Bounds() != image.Rect(0, 0, 2, 1) || got.RGBAAt(0, 0) != red || got.RGBAAt(1, 0) != blue {
		t.Errorf("halved: %v %v %v", got.Bounds(), got.RGBAAt(0, 0), got.RGBAAt(1, 0))
	}
	// Three columns into two: the middle one shared half and half.
	three := image.NewRGBA(image.Rect(0, 0, 3, 3))
	fill(three, image.Rect(0, 0, 1, 3), red)
	fill(three, image.Rect(1, 0, 2, 3), color.RGBA{255, 255, 255, 255})
	fill(three, image.Rect(2, 0, 3, 3), blue)
	got = Shrink(three, 2).(*image.RGBA)
	// Left: 1 red + 0.5 white over 1.5 → red, with a third of white.
	if c := got.RGBAAt(0, 0); c != (color.RGBA{255, 85, 85, 255}) {
		t.Errorf("shared column: %v", c)
	}
}

// Transparency averages premultiplied: half transparent, half opaque white
// is white at half alpha, never grey.
func TestShrinkAlpha(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	src.SetRGBA(1, 0, color.RGBA{255, 255, 255, 255})
	c := Shrink(src, 1).(*image.RGBA).RGBAAt(0, 0)
	if c != (color.RGBA{128, 128, 128, 128}) { // premultiplied: white at alpha 128
		t.Errorf("edge: %v", c)
	}
}

// Sizes: the height follows, rounded, never below 1; nothing is enlarged;
// any kind of image (JPEG's YCbCr too) and bounds not at 0 are taken.
func TestShrinkSizes(t *testing.T) {
	poster := image.NewYCbCr(image.Rect(10, 10, 2010, 3010), image.YCbCrSubsampleRatio420)
	if got := Shrink(poster, 200).Bounds(); got != image.Rect(0, 0, 200, 300) {
		t.Errorf("poster: %v", got)
	}
	backdrop := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	if got := Shrink(backdrop, 320).Bounds(); got != image.Rect(0, 0, 320, 180) {
		t.Errorf("backdrop: %v", got)
	}
	line := image.NewRGBA(image.Rect(0, 0, 1000, 1))
	if got := Shrink(line, 10).Bounds(); got != image.Rect(0, 0, 10, 1) {
		t.Errorf("a line: %v", got)
	}
	small := image.NewRGBA(image.Rect(0, 0, 100, 50))
	for _, w := range []int{100, 400, 0, -1} {
		if Shrink(small, w) != image.Image(small) {
			t.Errorf("width %d: changed", w)
		}
	}
}

// Every weight adds up to one pixel, whatever the ratio.
func TestSpans(t *testing.T) {
	for _, nm := range [][2]int{{3840, 320}, {3, 2}, {1000, 7}, {2001, 200}, {5, 4}} {
		for i, s := range spans(nm[0], nm[1]) {
			var sum float32
			for _, p := range s {
				sum += p.weight
			}
			if sum < 0.999 || sum > 1.001 {
				t.Errorf("%d into %d, pixel %d: %v", nm[0], nm[1], i, sum)
			}
		}
	}
}

func BenchmarkShrinkBackdrop(b *testing.B) {
	img := image.NewYCbCr(image.Rect(0, 0, 3840, 2160), image.YCbCrSubsampleRatio420)
	for b.Loop() {
		Shrink(img, 320)
	}
}

// A JPEG's image is shrunk in its own colors, each plane its size: the
// same colors as through RGB, within rounding.
func TestShrinkYCbCr(t *testing.T) {
	for _, ratio := range []image.YCbCrSubsampleRatio{image.YCbCrSubsampleRatio420, image.YCbCrSubsampleRatio444, image.YCbCrSubsampleRatio422} {
		src := image.NewYCbCr(image.Rect(0, 0, 41, 31), ratio)
		for i := range src.Y {
			src.Y[i] = uint8(i * 7)
		}
		for i := range src.Cb {
			src.Cb[i], src.Cr[i] = uint8(i*3), uint8(255-i*5)
		}
		got, ok := Shrink(src, 10).(*image.YCbCr)
		if !ok || got.Bounds() != image.Rect(0, 0, 10, 8) || got.SubsampleRatio != ratio {
			t.Fatalf("%v: %T %v", ratio, got, got.Bounds())
		}
		// A flat image stays flat.
		flat := image.NewYCbCr(image.Rect(0, 0, 40, 20), ratio)
		for i := range flat.Y {
			flat.Y[i] = 100
		}
		for i := range flat.Cb {
			flat.Cb[i], flat.Cr[i] = 50, 200
		}
		f := Shrink(flat, 7).(*image.YCbCr)
		if r, g, b, _ := f.At(3, 2).RGBA(); [3]uint32{r, g, b} != func() [3]uint32 {
			r, g, b, _ := flat.At(0, 0).RGBA()
			return [3]uint32{r, g, b}
		}() {
			t.Errorf("%v: flat changed", ratio)
		}
	}
}

// Any image, any width: no panic, never larger, the width asked when
// smaller.
func FuzzShrink(f *testing.F) {
	f.Add(41, 31, 10, true, uint8(0))
	f.Add(1, 1000, 1, false, uint8(2))
	f.Add(3, 2, 2, true, uint8(5))
	f.Fuzz(func(t *testing.T, w, h, width int, ycbcr bool, ratio uint8) {
		w, h = 1+abs(w)%300, 1+abs(h)%300
		var img image.Image = image.NewRGBA(image.Rect(5, 7, 5+w, 7+h))
		if ycbcr {
			img = image.NewYCbCr(image.Rect(5, 7, 5+w, 7+h), image.YCbCrSubsampleRatio(ratio%6))
		}
		got := Shrink(img, width).Bounds()
		switch {
		case width <= 0 || width >= w:
			if got != img.Bounds() {
				t.Fatalf("%dx%d to %d: %v", w, h, width, got)
			}
		case got.Dx() != width || got.Dy() < 1 || got.Dy() > h:
			t.Fatalf("%dx%d to %d: %v", w, h, width, got)
		}
	})
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
