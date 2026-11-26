package previews

import (
	"slices"
	"testing"
)

// ffmpeg's decoding on a GPU, for each of Overfin's families: NVIDIA by
// CUDA; Intel and AMD by VA-API, at their render device only (a device
// that isn't one never becomes an option of ffmpeg's); none, or a family
// that isn't one, on the processor.
func TestHardwareFor(t *testing.T) {
	for _, c := range []struct {
		mode, device string
		want         []string
	}{
		{"nvenc", "", []string{"-hwaccel", "cuda"}},
		{"qsv", "/dev/dri/renderD128", []string{"-hwaccel", "vaapi", "-hwaccel_device", "/dev/dri/renderD128"}},
		{"vaapi", "/dev/dri/renderD129", []string{"-hwaccel", "vaapi", "-hwaccel_device", "/dev/dri/renderD129"}},
		{"vaapi", "", nil},
		{"vaapi", "-vf drawtext", nil},
		{"qsv", "/tmp/dri/renderD128", nil},
		{"none", "/dev/dri/renderD128", nil},
		{"rkmpp", "/dev/dri/renderD128", nil},
	} {
		if got := HardwareFor(c.mode, c.device); !slices.Equal(got, c.want) {
			t.Errorf("%s %q: %q, want %q", c.mode, c.device, got, c.want)
		}
	}
}
