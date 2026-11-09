// Package qr makes QR codes, written from the QR code standard (ISO/IEC
// 18004) for what Overfin needs: a short text, such as the address a TV
// shows to be signed in from a phone, in byte mode at error correction
// level M (15% of the code can be damaged), in versions 1 to 10 (up to
// 213 bytes). It has no dependencies, and is checked against a QR reader
// in its tests.
package qr

import (
	"errors"
	"fmt"
	"strings"
)

// ErrTooLong is a text longer than version 10 holds.
var ErrTooLong = errors.New("qr: text too long")

// Code is a QR code: Size modules a side, dark or light.
type Code struct {
	Size    int
	modules []bool
}

// Dark says whether the module at column x, row y is dark.
func (c *Code) Dark(x, y int) bool { return c.modules[y*c.Size+x] }

// level M's blocks for versions 1 to 10: error correction codewords per
// block, then the blocks' data codewords (the standard's table 9).
var blocks = [11]struct {
	ec   int
	data []int
}{
	1:  {10, []int{16}},
	2:  {16, []int{28}},
	3:  {26, []int{44}},
	4:  {18, []int{32, 32}},
	5:  {24, []int{43, 43}},
	6:  {16, []int{27, 27, 27, 27}},
	7:  {18, []int{31, 31, 31, 31}},
	8:  {22, []int{38, 38, 39, 39}},
	9:  {22, []int{36, 36, 36, 37, 37}},
	10: {26, []int{43, 43, 43, 43, 44}},
}

// alignment patterns' centres, rows and columns alike (annex E).
var alignment = [11][]int{
	2: {6, 18}, 3: {6, 22}, 4: {6, 26}, 5: {6, 30}, 6: {6, 34},
	7: {6, 22, 38}, 8: {6, 24, 42}, 9: {6, 26, 46}, 10: {6, 28, 50},
}

func dataCodewords(version int) (n int) {
	for _, d := range blocks[version].data {
		n += d
	}
	return n
}

// Encode makes the smallest code that holds text.
func Encode(text string) (*Code, error) {
	version := 0
	for v := 1; v <= 10; v++ {
		count := 8
		if v >= 10 {
			count = 16
		}
		if 4+count+8*len(text) <= 8*dataCodewords(v) {
			version = v
			break
		}
	}
	if version == 0 {
		return nil, ErrTooLong
	}
	data := encodeData(text, version)
	words := interleave(data, version)

	b := newBuilder(version)
	b.functionPatterns()
	b.place(words)
	best, bestPenalty := -1, 0
	for mask := range 8 {
		c := b.with(mask)
		if p := penalty(c); best < 0 || p < bestPenalty {
			best, bestPenalty = mask, p
		}
	}
	return b.with(best), nil
}

// encodeData is the data codewords: byte mode, the count, the bytes, a
// terminator, then padding.
func encodeData(text string, version int) []byte {
	var bits bitBuffer
	bits.add(0b0100, 4)
	if version >= 10 {
		bits.add(len(text), 16)
	} else {
		bits.add(len(text), 8)
	}
	for i := 0; i < len(text); i++ {
		bits.add(int(text[i]), 8)
	}
	capacity := 8 * dataCodewords(version)
	bits.add(0, min(4, capacity-bits.n))
	bits.add(0, (8-bits.n%8)%8)
	for pad := 0; bits.n < capacity; pad++ {
		bits.add([]int{0xEC, 0x11}[pad%2], 8)
	}
	return bits.bytes
}

// interleave splits the data into the version's blocks, adds each one's
// error correction, and takes them a codeword from each block in turn.
func interleave(data []byte, version int) []byte {
	spec := blocks[version]
	gen := generator(spec.ec)
	var dataBlocks, ecBlocks [][]byte
	at := 0
	for _, n := range spec.data {
		block := data[at : at+n]
		at += n
		dataBlocks = append(dataBlocks, block)
		ecBlocks = append(ecBlocks, remainder(block, gen))
	}
	var out []byte
	longest := spec.data[len(spec.data)-1]
	for i := range longest {
		for _, block := range dataBlocks {
			if i < len(block) {
				out = append(out, block[i])
			}
		}
	}
	for i := range spec.ec {
		for _, block := range ecBlocks {
			out = append(out, block[i])
		}
	}
	return out
}

type bitBuffer struct {
	bytes []byte
	n     int
}

func (b *bitBuffer) add(value, length int) {
	for i := length - 1; i >= 0; i-- {
		if b.n%8 == 0 {
			b.bytes = append(b.bytes, 0)
		}
		if value>>i&1 == 1 {
			b.bytes[b.n/8] |= 0x80 >> (b.n % 8)
		}
		b.n++
	}
}

// ---- Reed-Solomon over GF(256), modulo x⁸+x⁴+x³+x²+1 ---------------------

var exp, logs [256]int

func init() {
	x := 1
	for i := range 255 {
		exp[i] = x
		logs[x] = i
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11D
		}
	}
	exp[255] = exp[0]
}

func mul(a, b int) int {
	if a == 0 || b == 0 {
		return 0
	}
	return exp[(logs[a]+logs[b])%255]
}

// generator is the polynomial (x−α⁰)(x−α¹)…(x−αⁿ⁻¹), highest power
// first, its leading 1 left out.
func generator(n int) []int {
	g := []int{1}
	for i := range n {
		next := make([]int, len(g)+1)
		for j, c := range g {
			next[j] ^= c
			next[j+1] ^= mul(c, exp[i])
		}
		g = next
	}
	return g[1:]
}

// remainder is data's error correction: data·xⁿ modulo the generator.
func remainder(data []byte, gen []int) []byte {
	r := make([]int, len(gen))
	for _, d := range data {
		factor := int(d) ^ r[0]
		copy(r, r[1:])
		r[len(r)-1] = 0
		for i, g := range gen {
			r[i] ^= mul(g, factor)
		}
	}
	out := make([]byte, len(r))
	for i, v := range r {
		out[i] = byte(v)
	}
	return out
}

// ---- Placing the modules ----------------------------------------------------

type builder struct {
	version, size int
	dark, fixed   []bool
}

func newBuilder(version int) *builder {
	size := 17 + 4*version
	return &builder{version: version, size: size, dark: make([]bool, size*size), fixed: make([]bool, size*size)}
}

func (b *builder) set(x, y int, dark bool) {
	b.dark[y*b.size+x] = dark
	b.fixed[y*b.size+x] = true
}

func (b *builder) functionPatterns() {
	// The timing patterns, then what's drawn over their ends.
	for i := range b.size {
		b.set(6, i, i%2 == 0)
		b.set(i, 6, i%2 == 0)
	}
	b.finder(3, 3)
	b.finder(b.size-4, 3)
	b.finder(3, b.size-4)
	centres := alignment[b.version]
	for _, y := range centres {
		for _, x := range centres {
			// None over a finder.
			if (x == 6 && y == 6) || (x == 6 && y == centres[len(centres)-1]) || (y == 6 && x == centres[len(centres)-1]) {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					b.set(x+dx, y+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	// The format's places are kept (written with the mask), and the one
	// module always dark.
	b.format(0)
	b.set(8, b.size-8, true)
	if b.version >= 7 {
		bits := b.version<<12 | bch(b.version, 0x1F25, 12)
		for i := range 18 {
			a, c := b.size-11+i%3, i/3
			b.set(a, c, bits>>i&1 == 1)
			b.set(c, a, bits>>i&1 == 1)
		}
	}
}

// finder draws a finder pattern centred on x, y, with its light border.
func (b *builder) finder(x, y int) {
	for dy := -4; dy <= 4; dy++ {
		for dx := -4; dx <= 4; dx++ {
			px, py := x+dx, y+dy
			if px < 0 || py < 0 || px >= b.size || py >= b.size {
				continue
			}
			d := max(abs(dx), abs(dy))
			b.set(px, py, d != 2 && d != 4)
		}
	}
}

// format writes level M (bits 00) and the mask, twice.
func (b *builder) format(mask int) {
	data := 0b00<<3 | mask
	bits := (data<<10 | bch(data, 0x537, 10)) ^ 0x5412
	bit := func(i int) bool { return bits>>i&1 == 1 }
	for i := range 6 {
		b.set(8, i, bit(i))
	}
	b.set(8, 7, bit(6))
	b.set(8, 8, bit(7))
	b.set(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		b.set(14-i, 8, bit(i))
	}
	for i := range 8 {
		b.set(b.size-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		b.set(8, b.size-15+i, bit(i))
	}
}

// bch is the BCH code's check bits for data: data·2ⁿ modulo poly.
func bch(data, poly, n int) int {
	r := data << n
	for i := bitLength(r) - 1; i >= n; i-- {
		if r>>i&1 == 1 {
			r ^= poly << (i - bitLength(poly) + 1)
		}
	}
	return r
}

func bitLength(v int) (n int) {
	for ; v > 0; v >>= 1 {
		n++
	}
	return n
}

// place lays the codewords out in two-column strips from the bottom right,
// up then down in turn, around what's fixed; the timing column is
// skipped.
func (b *builder) place(words []byte) {
	i := 0
	total := 8 * len(words)
	up := true
	for right := b.size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for step := range b.size {
			y := step
			if up {
				y = b.size - 1 - step
			}
			for _, x := range []int{right, right - 1} {
				if b.fixed[y*b.size+x] {
					continue
				}
				// Past the last codeword: the remainder bits, light.
				if i < total {
					b.dark[y*b.size+x] = words[i/8]>>(7-i%8)&1 == 1
				}
				i++
			}
		}
		up = !up
	}
}

// with is the code with a mask applied, and its format written.
func (b *builder) with(mask int) *Code {
	c := &builder{version: b.version, size: b.size, dark: append([]bool(nil), b.dark...), fixed: append([]bool(nil), b.fixed...)}
	for y := range c.size {
		for x := range c.size {
			if !c.fixed[y*c.size+x] && masked(mask, x, y) {
				c.dark[y*c.size+x] = !c.dark[y*c.size+x]
			}
		}
	}
	c.format(mask)
	return &Code{Size: c.size, modules: c.dark}
}

func masked(mask, x, y int) bool {
	switch mask {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (y/2+x/3)%2 == 0
	case 5:
		return x*y%2+x*y%3 == 0
	case 6:
		return (x*y%2+x*y%3)%2 == 0
	default:
		return ((x+y)%2+x*y%3)%2 == 0
	}
}

// penalty scores a masked code by the standard's four rules: the lowest
// is the easiest to read.
func penalty(c *Code) int {
	n := c.Size
	score := 0
	line := func(at func(i int) bool) {
		run := 1
		for i := 1; i <= n; i++ {
			if i < n && at(i) == at(i-1) {
				run++
				continue
			}
			if run >= 5 {
				score += 3 + run - 5
			}
			run = 1
		}
		// The finder's look, 1:1:3:1:1 with four light modules on a side.
		for i := 0; i+10 < n; i++ {
			pattern := []bool{true, false, true, true, true, false, true}
			light := func(from int) bool {
				for j := from; j < from+4; j++ {
					if at(j) {
						return false
					}
				}
				return true
			}
			matches := func(from int) bool {
				for j, p := range pattern {
					if at(from+j) != p {
						return false
					}
				}
				return true
			}
			if (matches(i) && light(i+7)) || (light(i) && matches(i+4)) {
				score += 40
			}
		}
	}
	dark := 0
	for y := range n {
		line(func(x int) bool { return c.Dark(x, y) })
	}
	for x := range n {
		line(func(y int) bool { return c.Dark(x, y) })
	}
	for y := range n {
		for x := range n {
			if c.Dark(x, y) {
				dark++
			}
			if x+1 < n && y+1 < n {
				d := c.Dark(x, y)
				if c.Dark(x+1, y) == d && c.Dark(x, y+1) == d && c.Dark(x+1, y+1) == d {
					score += 3
				}
			}
		}
	}
	percent := dark * 100 / (n * n)
	return score + 10*(abs(percent-50)/5)
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Path is the dark modules as an SVG path, one rectangle per run of dark
// modules in a row, in module units with a 4-module light margin around:
// the code is drawn in a viewBox Size+8 modules a side.
func (c *Code) Path() string {
	var p strings.Builder
	for y := range c.Size {
		for x := 0; x < c.Size; {
			if !c.Dark(x, y) {
				x++
				continue
			}
			start := x
			for x < c.Size && c.Dark(x, y) {
				x++
			}
			fmt.Fprintf(&p, "M%d %dh%dv1h-%dz", start+4, y+4, x-start, x-start)
		}
	}
	return p.String()
}
