package jellyfin

import (
	"bufio"
	"bytes"
	"io"
	"testing"
)

// Whatever bytes Jellyfin's socket sends, read returns whole messages
// within the limit, and a message split in frames comes back whole.
func FuzzSocketRead(f *testing.F) {
	f.Add(serverFrame(0x1, true, `{"MessageType":"LibraryChanged"}`), "abc", uint16(2))
	f.Add(append(serverFrame(0x9, true, "hi"), serverFrame(0x1, true, "x")...), "", uint16(0))
	f.Add([]byte{0x81, 0x7f, 0, 0, 0, 0, 0xff, 0xff, 0xff, 0xff}, "a", uint16(1))
	f.Add([]byte{0x81, 0xfe, 0, 1}, "message", uint16(3))
	f.Fuzz(func(t *testing.T, raw []byte, text string, cut uint16) {
		s := &socket{r: bufio.NewReader(bytes.NewReader(raw)), w: io.Discard}
		for range 10 {
			msg, err := s.read()
			if err != nil {
				break
			}
			if len(msg) > maxMessage {
				t.Fatalf("%d bytes", len(msg))
			}
		}
		if len(text) > 125 {
			return // serverFrame writes short frames only
		}
		at := int(cut) % (len(text) + 1)
		frames := append(serverFrame(0x1, false, text[:at]), serverFrame(0x9, true, "ping")...)
		frames = append(frames, serverFrame(0x0, true, text[at:])...)
		s = &socket{r: bufio.NewReader(bytes.NewReader(frames)), w: io.Discard}
		if msg, err := s.read(); err != nil || string(msg) != text {
			t.Fatalf("%q: %q %v", text, msg, err)
		}
	})
}

// Frames out of order and control frames too large or split are refused.
func TestSocketRefusesMalformedFrames(t *testing.T) {
	big := make([]byte, 126)
	for name, raw := range map[string][]byte{
		"continuation first":  serverFrame(0x0, true, "x"),
		"text inside text":    append(serverFrame(0x1, false, "a"), serverFrame(0x1, true, "b")...),
		"large ping":          append([]byte{0x89, 126, 0, 126}, big...),
		"split ping":          serverFrame(0x9, false, "hi"),
		"announced, not sent": {0x81, 0x7f, 0, 0, 0, 0, 0, 0xff, 0, 0},
	} {
		s := &socket{r: bufio.NewReader(bytes.NewReader(raw)), w: io.Discard}
		if msg, err := s.read(); err == nil {
			t.Errorf("%s: read %q", name, msg)
		}
	}
}
