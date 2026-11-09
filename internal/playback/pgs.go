package playback

import (
	"encoding/binary"
	"errors"
)

// Subtitles that are pictures (PGS, from Blu-rays): the track Jellyfin
// extracts as it is, served only when every part of it is well-formed and
// within its limits (SECURITY.md rule 11). The player decodes and draws
// it; nothing in it is run, and a browser never renders it as a page.

// The limits of a PGS track Overfin serves.
const (
	MaxPGS        = 64 << 20 // bytes, a whole track
	maxPGSSide    = 4096     // a picture's or the screen's width or height, as rule 10
	maxPGSObjects = 20_000   // pictures in a track
)

// ErrPGS: a PGS track isn't one Overfin serves.
var ErrPGS = errors.New("playback: not a well-formed PGS track")

// PGS segment types.
const (
	pgsPalette      = 0x14
	pgsObject       = 0x15
	pgsPresentation = 0x16
	pgsWindow       = 0x17
	pgsEnd          = 0x80
)

// CheckPGS reads a PGS track whole: every segment's header and length,
// the presentations', windows' and palettes' sizes, and every picture's
// run-length data decoded (counted, not drawn) within the size it says it
// has. Anything else is ErrPGS.
func CheckPGS(raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxPGS {
		return ErrPGS
	}
	type object struct {
		w, h int
		data []byte
	}
	objects := map[uint16]*object{}
	count := 0
	for pos := 0; pos < len(raw); {
		if pos+13 > len(raw) || raw[pos] != 'P' || raw[pos+1] != 'G' {
			return ErrPGS
		}
		typ := raw[pos+10]
		size := int(binary.BigEndian.Uint16(raw[pos+11:]))
		if pos+13+size > len(raw) {
			return ErrPGS
		}
		seg := raw[pos+13 : pos+13+size]
		pos += 13 + size
		switch typ {
		case pgsPresentation:
			// Width, height, rate, number, state, palette update, palette,
			// then each object shown: 8 bytes, 16 when cropped.
			if len(seg) < 11 {
				return ErrPGS
			}
			// The screen the player makes a canvas of.
			if w, h := int(binary.BigEndian.Uint16(seg)), int(binary.BigEndian.Uint16(seg[2:])); w == 0 || h == 0 || w > maxPGSSide || h > maxPGSSide {
				return ErrPGS
			}
			o := 11
			for range int(seg[10]) {
				if o+8 > len(seg) {
					return ErrPGS
				}
				cropped := seg[o+3]&0x80 != 0
				o += 8
				if cropped {
					o += 8
				}
			}
			if o > len(seg) {
				return ErrPGS
			}
		case pgsWindow:
			if len(seg) < 1 || len(seg) != 1+9*int(seg[0]) {
				return ErrPGS
			}
		case pgsPalette:
			if len(seg) < 2 || (len(seg)-2)%5 != 0 {
				return ErrPGS
			}
		case pgsObject:
			if len(seg) < 4 {
				return ErrPGS
			}
			id, flags := binary.BigEndian.Uint16(seg), seg[3]
			if flags&0x80 != 0 { // its first fragment: its size
				if len(seg) < 11 {
					return ErrPGS
				}
				w, h := int(binary.BigEndian.Uint16(seg[7:])), int(binary.BigEndian.Uint16(seg[9:]))
				if w == 0 || h == 0 || w > maxPGSSide || h > maxPGSSide {
					return ErrPGS
				}
				if count++; count > maxPGSObjects {
					return ErrPGS
				}
				objects[id] = &object{w: w, h: h, data: append([]byte(nil), seg[11:]...)}
			} else {
				o := objects[id]
				if o == nil {
					return ErrPGS
				}
				o.data = append(o.data, seg[4:]...)
			}
			if flags&0x40 != 0 { // its last fragment: checked whole
				o := objects[id]
				if o == nil || !runsFit(o.data, o.w, o.h) {
					return ErrPGS
				}
				delete(objects, id)
			}
		case pgsEnd:
			if len(seg) != 0 {
				return ErrPGS
			}
		default:
			return ErrPGS
		}
	}
	return nil
}

// runsFit says whether a picture's run-length data stays within w by h:
// no line longer than w, no more than h lines, no run cut short.
func runsFit(d []byte, w, h int) bool {
	x, y := 0, 0
	for i := 0; i < len(d); {
		b := d[i]
		i++
		if b != 0 { // one pixel
			x++
		} else {
			if i >= len(d) {
				return false
			}
			f := d[i]
			i++
			if f == 0 { // end of a line
				x, y = 0, y+1
				if y > h {
					return false
				}
				continue
			}
			n := int(f & 0x3f)
			if f&0x40 != 0 {
				if i >= len(d) {
					return false
				}
				n = n<<8 | int(d[i])
				i++
			}
			if f&0x80 != 0 { // a color follows
				if i >= len(d) {
					return false
				}
				i++
			}
			x += n
		}
		if x > w {
			return false
		}
	}
	return true
}
