package playback

import (
	"encoding/binary"
	"errors"
	"os"
	"testing"
)

// segment is one PGS segment: its header (magic, times, type, size), then
// its body.
func segment(typ byte, body []byte) []byte {
	b := make([]byte, 13, 13+len(body))
	b[0], b[1], b[10] = 'P', 'G', typ
	binary.BigEndian.PutUint16(b[11:], uint16(len(body)))
	return append(b, body...)
}

// object is a picture's segment, whole in one: id 1, w by h, its runs.
func object(w, h int, runs []byte) []byte {
	body := []byte{0, 1, 0, 0xC0, 0, 0, 0, byte(w >> 8), byte(w), byte(h >> 8), byte(h)}
	return segment(pgsObject, append(body, runs...))
}

// track is a subtitle shown, then hidden: presentation, window, palette,
// a picture, end; then an empty presentation and end.
func track(pic []byte) []byte {
	var t []byte
	t = append(t, segment(pgsPresentation, []byte{7, 128, 4, 56, 16, 0, 0, 0x80, 0, 0, 1, 0, 1, 0, 0, 1, 0, 2, 0})...)
	t = append(t, segment(pgsWindow, []byte{1, 0, 1, 0, 2, 0, 0, 3, 0, 1})...)
	t = append(t, segment(pgsPalette, []byte{0, 0, 1, 235, 128, 128, 255})...)
	t = append(t, pic...)
	t = append(t, segment(pgsEnd, nil)...)
	t = append(t, segment(pgsPresentation, []byte{7, 128, 4, 56, 16, 0, 1, 0, 0, 0, 0})...)
	t = append(t, segment(pgsEnd, nil)...)
	return t
}

// A well-formed track passes; what's cut, bent or too large doesn't.
func TestCheckPGS(t *testing.T) {
	// 3 by 2: a pixel of color 1, a run of 2 of color 0, end of line;
	// a run of 3 of color 1, end of line.
	ok := track(object(3, 2, []byte{1, 0, 2, 0, 0, 0, 0x83, 1, 0, 0}))
	if err := CheckPGS(ok); err != nil {
		t.Fatalf("well-formed: %v", err)
	}
	// In two fragments, the second carrying the rest.
	first := segment(pgsObject, []byte{0, 1, 0, 0x80, 0, 0, 0, 0, 3, 0, 2, 1, 0, 2})
	// The next fragment: id, version, flags, then the rest of the data (the
	// length and the size come with the first only).
	rest := segment(pgsObject, []byte{0, 1, 0, 0x40, 0, 0, 0, 0x83, 1, 0, 0})
	if err := CheckPGS(track(append(first, rest...))); err != nil {
		t.Errorf("in fragments: %v", err)
	}
	for name, bad := range map[string][]byte{
		"empty":            nil,
		"cut short":        ok[:len(ok)-3],
		"not PGS":          append([]byte("PX"), ok[2:]...),
		"unknown segment":  append(ok, segment(0x42, nil)...),
		"a huge screen":    append(segment(pgsPresentation, []byte{0x40, 0, 0x40, 0, 16, 0, 0, 0x80, 0, 0, 0}), segment(pgsEnd, nil)...),
		"no screen":        append(segment(pgsPresentation, []byte{0, 0, 0, 0, 16, 0, 0, 0x80, 0, 0, 0}), segment(pgsEnd, nil)...),
		"a line too long":  track(object(3, 2, []byte{0, 0x84, 1})),
		"too many lines":   track(object(3, 1, []byte{1, 0, 0, 1, 0, 0, 1, 0, 0})),
		"a run cut":        track(object(3, 2, []byte{0, 0x83})),
		"a picture huge":   track(object(5000, 2, nil)),
		"a picture empty":  track(object(0, 2, nil)),
		"a lone fragment":  track(rest),
		"a bent palette":   append(ok, segment(pgsPalette, []byte{0, 0, 1})...),
		"a bent window":    append(ok, segment(pgsWindow, []byte{2, 0})...),
		"a short show":     append(ok, segment(pgsPresentation, []byte{1, 2})...),
		"objects past end": append(ok, segment(pgsPresentation, []byte{7, 128, 4, 56, 16, 0, 0, 0, 0, 0, 3, 0, 1})...),
		"end with a body":  append(ok, segment(pgsEnd, []byte{1})...),
	} {
		if err := CheckPGS(bad); !errors.Is(err, ErrPGS) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := CheckPGS(make([]byte, MaxPGS+1)); !errors.Is(err, ErrPGS) {
		t.Errorf("too large: %v", err)
	}
}

// Real tracks pass, when there are some to try (PGS_SAMPLES: files of a
// film's PGS tracks, as Jellyfin extracts them; none ships here).
func TestCheckPGSSamples(t *testing.T) {
	dir := os.Getenv("PGS_SAMPLES")
	if dir == "" {
		t.Skip("PGS_SAMPLES unset")
	}
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		raw, err := os.ReadFile(dir + "/" + f.Name())
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > 2 && raw[0] == 'P' && raw[1] == 'G' {
			if err := CheckPGS(raw); err != nil {
				t.Errorf("%s: %v", f.Name(), err)
			}
		}
	}
}

// Any bytes: no panic, never accepted when they aren't segments.
func FuzzCheckPGS(f *testing.F) {
	f.Add(track(object(3, 2, []byte{1, 0, 2, 0, 0, 0, 0x83, 1, 0, 0})))
	f.Add([]byte("PG"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if CheckPGS(raw) == nil && (len(raw) < 13 || raw[0] != 'P' || raw[1] != 'G') {
			t.Fatalf("accepted %x", raw[:min(len(raw), 16)])
		}
	})
}
