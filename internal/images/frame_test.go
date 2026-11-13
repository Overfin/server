package images

import (
	"image"
	"image/color"
	"testing"
)

// Black is dark, one flat grey has no contrast, a checkerboard has both.
func TestLight(t *testing.T) {
	fill := func(f func(x, y int) uint8) image.Image {
		img := image.NewGray(image.Rect(0, 0, 8, 8))
		for y := range 8 {
			for x := range 8 {
				img.SetGray(x, y, color.Gray{Y: f(x, y)})
			}
		}
		return img
	}
	if m, d := light(fill(func(int, int) uint8 { return 0 })); m != 0 || d != 0 {
		t.Errorf("black: %v %v", m, d)
	}
	if m, d := light(fill(func(int, int) uint8 { return 128 })); m < 0.49 || m > 0.51 || d > 0.001 {
		t.Errorf("grey: %v %v", m, d)
	}
	if m, d := light(fill(func(x, y int) uint8 { return uint8(255 * ((x + y) % 2)) })); m < 0.49 || m > 0.51 || d < 0.49 {
		t.Errorf("checkerboard: %v %v", m, d)
	}
}
