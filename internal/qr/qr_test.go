package qr

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// image draws the code 4 pixels a module, with its light margin.
func (c *Code) image() image.Image {
	const scale = 4
	side := (c.Size + 8) * scale
	img := image.NewGray(image.Rect(0, 0, side, side))
	for y := range side {
		for x := range side {
			mx, my := x/scale-4, y/scale-4
			v := uint8(255)
			if mx >= 0 && my >= 0 && mx < c.Size && my < c.Size && c.Dark(mx, my) {
				v = 0
			}
			img.SetGray(x, y, color.Gray{Y: v})
		}
	}
	return img
}

// Texts of every length a version holds, read back by a QR reader
// (zbarimg, skipped without it): the code says what was put in.
func TestReadBack(t *testing.T) {
	reader, err := exec.LookPath("zbarimg")
	if err != nil {
		t.Skip("no zbarimg to read the codes back")
	}
	texts := []string{
		"A",
		"https://example.org/link?code=KDRT-WXPM",
		"https://streaming.example.org/link?code=KDRT-WXPM",
	}
	// One text at each version's capacity, and one a byte longer than the
	// version before (the next version).
	for v := 1; v <= 10; v++ {
		count := 8
		if v >= 10 {
			count = 16
		}
		most := (8*dataCodewords(v) - 4 - count) / 8
		text := strings.Repeat("x", most)
		if most > 20 {
			text = "https://example.org/" + text[20:]
		}
		texts = append(texts, text)
	}
	dir := t.TempDir()
	for i, text := range texts {
		c, err := Encode(text)
		if err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		file := filepath.Join(dir, "code.png")
		var b bytes.Buffer
		if err := png.Encode(&b, c.image()); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(reader, "--raw", "-q", file).Output()
		if got := strings.TrimSuffix(string(out), "\n"); err != nil || got != text {
			t.Errorf("text %d (version %d): read %q, %v", i, (c.Size-17)/4, got, err)
		}
	}
}

func TestVersions(t *testing.T) {
	for _, tc := range []struct {
		length, version int
	}{{1, 1}, {14, 1}, {15, 2}, {42, 3}, {43, 4}, {180, 9}, {181, 10}, {213, 10}} {
		c, err := Encode(strings.Repeat("a", tc.length))
		if err != nil || c.Size != 17+4*tc.version {
			t.Errorf("%d bytes: size %v, %v; want version %d", tc.length, c, err, tc.version)
		}
	}
	if _, err := Encode(strings.Repeat("a", 214)); !errors.Is(err, ErrTooLong) {
		t.Errorf("214 bytes: %v", err)
	}
}

// The path draws exactly the dark modules, with the margin.
func TestPath(t *testing.T) {
	c, err := Encode("https://example.org/link?code=KDRT-WXPM")
	if err != nil {
		t.Fatal(err)
	}
	dark := 0
	for y := range c.Size {
		for x := range c.Size {
			if c.Dark(x, y) {
				dark++
			}
		}
	}
	drawn := 0
	for _, part := range strings.Split(c.Path(), "z") {
		var x, y, w, h, back int
		if part == "" {
			continue
		}
		if _, err := fmtSscanf(part, &x, &y, &w, &h, &back); err != nil || x < 4 || y < 4 || x+w > c.Size+4 {
			t.Fatalf("bad part %q", part)
		}
		drawn += w
	}
	if drawn != dark {
		t.Errorf("path draws %d modules, the code has %d", drawn, dark)
	}
}

func fmtSscanf(s string, x, y, w, h, back *int) (int, error) {
	return fmt.Sscanf(s, "M%d %dh%dv%dh-%d", x, y, w, h, back)
}
