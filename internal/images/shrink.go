package images

import (
	"image"
	"image/draw"
)

// Shrinking: Overfin makes its images the sizes it wants itself, from one
// original each, rather than taking what each source sizes them to.

// Shrink returns img at width pixels wide, its height in proportion (1 at
// least): each pixel the average of those of img it covers, in part at
// its edges (an area average, the right way to make an image smaller).
// It never enlarges: an image width or narrower is returned as it is.
// Colors are averaged premultiplied by their alpha, so a transparent
// logo's edges don't darken.
func Shrink(img image.Image, width int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if width <= 0 || width >= w || h == 0 {
		return img
	}
	height := max(1, (h*width+w/2)/w)
	if y, ok := img.(*image.YCbCr); ok {
		return shrinkYCbCr(y, width, height)
	}
	src := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)

	// Across, then down: each output column (row) is a weighted sum of
	// the source columns (rows) it covers.
	cols := spans(w, width)
	rows := spans(h, height)
	across := make([]float32, width*h*4)
	for y := 0; y < h; y++ {
		line := src.Pix[y*src.Stride : y*src.Stride+w*4]
		out := across[y*width*4 : (y+1)*width*4]
		for x, span := range cols {
			var r, g, bl, a float32
			for _, c := range span {
				p := line[c.from*4 : c.from*4+4]
				r += float32(p[0]) * c.weight
				g += float32(p[1]) * c.weight
				bl += float32(p[2]) * c.weight
				a += float32(p[3]) * c.weight
			}
			out[x*4], out[x*4+1], out[x*4+2], out[x*4+3] = r, g, bl, a
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	for y, span := range rows {
		out := dst.Pix[y*dst.Stride : y*dst.Stride+width*4]
		for x := 0; x < width; x++ {
			var r, g, bl, a float32
			for _, c := range span {
				p := across[(c.from*width+x)*4:]
				r += p[0] * c.weight
				g += p[1] * c.weight
				bl += p[2] * c.weight
				a += p[3] * c.weight
			}
			out[x*4], out[x*4+1], out[x*4+2], out[x*4+3] = toByte(r), toByte(g), toByte(bl), toByte(a)
		}
	}
	return dst
}

// shrinkYCbCr shrinks a JPEG's image as it's kept, plane by plane
// (brightness, then each color at its own, smaller size): no conversion to
// RGB and back, which would cost as much as the shrinking itself.
func shrinkYCbCr(src *image.YCbCr, width, height int) *image.YCbCr {
	dst := image.NewYCbCr(image.Rect(0, 0, width, height), src.SubsampleRatio)
	sb := src.Bounds()
	// The planes' sizes, as the image package lays them out.
	cw, ch := chromaSize(sb.Dx(), sb.Dy(), src.SubsampleRatio)
	dcw, dch := chromaSize(width, height, src.SubsampleRatio)
	y0 := src.YOffset(sb.Min.X, sb.Min.Y)
	c0 := src.COffset(sb.Min.X, sb.Min.Y)
	shrinkPlane(src.Y[y0:], src.YStride, sb.Dx(), sb.Dy(), dst.Y, dst.YStride, width, height)
	shrinkPlane(src.Cb[c0:], src.CStride, cw, ch, dst.Cb, dst.CStride, dcw, dch)
	shrinkPlane(src.Cr[c0:], src.CStride, cw, ch, dst.Cr, dst.CStride, dcw, dch)
	return dst
}

// chromaSize is the size of a w by h image's color planes.
func chromaSize(w, h int, r image.YCbCrSubsampleRatio) (int, int) {
	switch r {
	case image.YCbCrSubsampleRatio422:
		return (w + 1) / 2, h
	case image.YCbCrSubsampleRatio420:
		return (w + 1) / 2, (h + 1) / 2
	case image.YCbCrSubsampleRatio440:
		return w, (h + 1) / 2
	case image.YCbCrSubsampleRatio411:
		return (w + 3) / 4, h
	case image.YCbCrSubsampleRatio410:
		return (w + 3) / 4, (h + 1) / 2
	}
	return w, h
}

// shrinkPlane shrinks one channel, w by h, into dw by dh (no larger), as
// Shrink does; a plane already as small is copied.
func shrinkPlane(src []byte, stride, w, h int, dst []byte, dstStride, dw, dh int) {
	dw, dh = min(dw, w), min(dh, h)
	cols, rows := spans(w, dw), spans(h, dh)
	across := make([]float32, dw*h)
	for y := 0; y < h; y++ {
		line := src[y*stride : y*stride+w]
		out := across[y*dw : (y+1)*dw]
		for x, span := range cols {
			var v float32
			for _, c := range span {
				v += float32(line[c.from]) * c.weight
			}
			out[x] = v
		}
	}
	for y, span := range rows {
		out := dst[y*dstStride : y*dstStride+dw]
		for x := range out {
			var v float32
			for _, c := range span {
				v += across[c.from*dw+x] * c.weight
			}
			out[x] = toByte(v)
		}
	}
}

// part is a source pixel's share of an output pixel.
type part struct {
	from   int
	weight float32
}

// spans divides n source pixels among m output ones (m <= n): each output
// pixel covers n/m source pixels, the first and the last in part, and its
// weights add up to 1.
func spans(n, m int) [][]part {
	out := make([][]part, m)
	scale := float64(n) / float64(m)
	for i := range out {
		start, end := float64(i)*scale, float64(i+1)*scale
		for s := int(start); s < n && float64(s) < end; s++ {
			covered := min(end, float64(s+1)) - max(start, float64(s))
			if covered > 0 {
				out[i] = append(out[i], part{s, float32(covered / scale)})
			}
		}
	}
	return out
}

// toByte rounds a channel's average back to a byte.
func toByte(v float32) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 255:
		return 255
	}
	return uint8(v + 0.5)
}
