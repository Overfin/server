// Package imdb keeps what Overfin uses of IMDb's free datasets (rule 115):
// each film's, series' and episode's type, years, length and rating, and
// each season's rating, from its episodes. IMDb publishes the datasets
// every day for personal and non-commercial use; a server whose operator
// switched them on downloads them itself, once a day.
//
// The three files Overfin reads (about 300 MB compressed) become one
// compact file (about 130 MB), sorted by IMDb ID, with a small index, so a
// title is found at once, whenever it arrives, without keeping the
// datasets themselves.
package imdb

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"sync"
)

// Kind is what a title is, as Overfin keeps it.
type Kind uint8

const (
	Movie   Kind = 1 // a film, made for cinemas or for TV
	Series  Kind = 2 // a series, mini-series included
	Episode Kind = 3
	Special Kind = 4 // a TV special
)

// Title is what the datasets say of a title.
type Title struct {
	ID    string // "tt0133093"
	Kind  Kind
	Adult bool
	// Start is the year it came out (a series: its first), End a series'
	// last; 0 when unknown or still running.
	Start, End int
	// Minutes is its length; 0 when unknown.
	Minutes int
	// Rating, from 1 to 10, and the votes it's from; 0 and 0 when it has
	// none.
	Rating float64
	Votes  int
	// For an episode: its series' ID, its season and number, 0 when
	// unknown.
	Series         string
	Season, Number int
}

// Season is a season's rating: its rated episodes' ratings, each counting
// for its votes.
type Season struct {
	Rating float64
	Votes  int
	// Episodes is how many of its episodes have a rating.
	Episodes int
}

// The file: the titles in blocks of blockSize, the seasons in blocks
// (a series' seasons never split between two), then both indexes, then a
// footer saying where each part starts. Integers are varints; IDs are
// IMDb's numbers, each block's first in full and the others as the
// difference from the one before.
const (
	magic     = "OVERFIN-IMDB-1"
	blockSize = 256
	// maxBlock bounds a block's bytes, so a broken file can't make a
	// reader allocate without end.
	maxBlock = 1 << 20
)

// footerSize is the footer's length: four offsets, then the magic.
const footerSize = 4*8 + len(magic)

// block is an index entry: the first ID in a block and where it starts.
type block struct {
	first  string
	offset int64
}

// Data is an open file of IMDb's data. It's safe for concurrent use;
// after Close, nothing is found.
type Data struct {
	mu      sync.RWMutex
	f       *os.File
	titles  []block // ends with the start of the seasons
	seasons []block // ends with the start of the indexes
	// Titles is how many titles the file holds; Built, when it was made
	// (Unix seconds).
	Titles int
	Built  int64
}

// Open opens a file Build wrote.
func Open(path string) (*Data, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	d, err := read(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("imdb: %s: %w", path, err)
	}
	return d, nil
}

func read(f *os.File) (*Data, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	if size < int64(len(magic)+footerSize) {
		return nil, errors.New("not a file of IMDb's data")
	}
	foot := make([]byte, footerSize)
	if _, err := f.ReadAt(foot, size-int64(footerSize)); err != nil {
		return nil, err
	}
	if string(foot[32:]) != magic {
		return nil, errors.New("not a file of IMDb's data")
	}
	seasonsAt := int64(binary.LittleEndian.Uint64(foot[0:]))
	indexAt := int64(binary.LittleEndian.Uint64(foot[8:]))
	titles := int(binary.LittleEndian.Uint64(foot[16:]))
	built := int64(binary.LittleEndian.Uint64(foot[24:]))
	end := size - int64(footerSize)
	if seasonsAt < int64(len(magic)) || indexAt < seasonsAt || indexAt > end {
		return nil, errors.New("a broken file of IMDb's data")
	}
	idx := make([]byte, end-indexAt)
	if _, err := f.ReadAt(idx, indexAt); err != nil {
		return nil, err
	}
	r := &reader{b: idx}
	d := &Data{f: f, Titles: titles, Built: built}
	d.titles = r.index(int64(len(magic)), seasonsAt)
	d.seasons = r.index(seasonsAt, indexAt)
	if r.err != nil || r.pos != len(idx) {
		return nil, errors.New("a broken file of IMDb's data")
	}
	return d, nil
}

// Close closes the file; lookups then find nothing.
func (d *Data) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f == nil {
		return nil
	}
	err := d.f.Close()
	d.f = nil
	return err
}

// Title returns what the file says of the title id ("tt0133093"), and
// whether it has it.
func (d *Data) Title(id string) (Title, bool) {
	n, ok := number(id)
	if !ok {
		return Title{}, false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	b, ok := d.load(d.titles, id)
	if !ok {
		return Title{}, false
	}
	r := &reader{b: b}
	var prev uint64
	for r.pos < len(r.b) && r.err == nil {
		t, at := r.title(prev)
		prev = at
		if at == n {
			return t, r.err == nil
		}
	}
	return Title{}, false
}

// Season returns the rating of a series' season as IMDb numbers its
// seasons, and whether any of its episodes has one. IMDb's numbers aren't
// another site's (orders differ): to rate another site's season, match its
// episodes by ID instead.
func (d *Data) Season(series string, season int) (Season, bool) {
	n, ok := number(series)
	if !ok {
		return Season{}, false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	b, ok := d.load(d.seasons, series)
	if !ok {
		return Season{}, false
	}
	r := &reader{b: b}
	var prev uint64
	for r.pos < len(r.b) && r.err == nil {
		s, of, number := r.season(prev)
		prev = of
		if of == n && number == season {
			return s, r.err == nil
		}
	}
	return Season{}, false
}

// load reads the block that holds id, if any can: the last one whose
// first ID comes before it, in the datasets' order (as text).
func (d *Data) load(index []block, id string) ([]byte, bool) {
	if d.f == nil || len(index) < 2 {
		return nil, false
	}
	blocks := index[:len(index)-1] // the last entry only says where they end
	i := sort.Search(len(blocks), func(i int) bool { return blocks[i].first > id }) - 1
	if i < 0 {
		return nil, false
	}
	size := index[i+1].offset - blocks[i].offset
	if size <= 0 || size > maxBlock {
		return nil, false
	}
	b := make([]byte, size)
	if _, err := d.f.ReadAt(b, blocks[i].offset); err != nil {
		return nil, false
	}
	return b, true
}

// number is the number in an IMDb ID ("tt0133093" → 133093).
func number(id string) (uint64, bool) {
	if len(id) < 9 || len(id) > 12 || id[:2] != "tt" {
		return 0, false
	}
	n, err := strconv.ParseUint(id[2:], 10, 32)
	return n, err == nil && n > 0
}

// text is an IMDb ID from its number: at least seven digits.
func text(n uint64) string {
	return fmt.Sprintf("tt%07d", n)
}

// reader reads varints from a block, remembering the first error.
type reader struct {
	b   []byte
	pos int
	err error
}

func (r *reader) uint() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.b[r.pos:])
	if n <= 0 {
		r.err = io.ErrUnexpectedEOF
		return 0
	}
	r.pos += n
	return v
}

func (r *reader) int() int64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Varint(r.b[r.pos:])
	if n <= 0 {
		r.err = io.ErrUnexpectedEOF
		return 0
	}
	r.pos += n
	return v
}

func (r *reader) byte() byte {
	if r.err != nil {
		return 0
	}
	if r.pos >= len(r.b) {
		r.err = io.ErrUnexpectedEOF
		return 0
	}
	c := r.b[r.pos]
	r.pos++
	return c
}

// index reads an index: its blocks' first IDs and offsets, from start,
// and then end, where they stop.
func (r *reader) index(start, end int64) []block {
	n := int(r.uint())
	if r.err != nil || n > (r.len()+1)/2 {
		r.err = io.ErrUnexpectedEOF
		return nil
	}
	out := make([]block, 0, n+1)
	at := start
	for range n {
		first := r.uint()
		at += int64(r.uint())
		out = append(out, block{first: text(first), offset: at})
	}
	if r.err == nil && (n > 0 && out[0].offset != start || at > end) {
		r.err = io.ErrUnexpectedEOF
	}
	return append(out, block{offset: end})
}

func (r *reader) len() int { return len(r.b) - r.pos }

// title reads a title, whose ID comes as the difference from prev.
func (r *reader) title(prev uint64) (Title, uint64) {
	n := uint64(int64(prev) + r.int())
	flags := r.byte()
	t := Title{ID: text(n), Kind: Kind(flags & 0x7), Adult: flags&0x80 != 0}
	t.Start = int(r.uint())
	if t.Kind == Series {
		t.End = int(r.uint())
	}
	t.Minutes = int(r.uint())
	if t.Kind == Episode {
		if s := r.uint(); s > 0 {
			t.Series = text(s)
		}
		t.Season, t.Number = int(r.uint()), int(r.uint())
	}
	if rating := r.byte(); rating > 0 {
		t.Rating = float64(rating) / 10
		t.Votes = int(r.uint())
	}
	return t, n
}

// season reads a season's rating, with its series' number (as the
// difference from prev) and its own.
func (r *reader) season(prev uint64) (Season, uint64, int) {
	of := uint64(int64(prev) + r.int())
	number := int(r.uint())
	s := Season{Rating: float64(r.uint()) / 100, Votes: int(r.uint()), Episodes: int(r.uint())}
	return s, of, number
}

// writer writes blocks of records and keeps their index.
type writer struct {
	w   *bufio.Writer
	at  int64 // bytes written
	err error
	// The current part's blocks: each one's first number, and the size of
	// the block before it (the first's: 0).
	blocks [][2]uint64
	start  int64 // where the current block started
	count  int   // records in it
	prev   uint64
	buf    []byte
}

func (w *writer) write(b []byte) {
	if w.err != nil {
		return
	}
	n, err := w.w.Write(b)
	w.at += int64(n)
	w.err = err
}

// begin starts a part of the file: titles, or seasons.
func (w *writer) begin() {
	w.blocks, w.start, w.count, w.prev = nil, w.at, 0, 0
}

// record starts a record whose ID is n, in a new block when the current
// one is full and may end here; it returns the buffer to append to, the
// ID's difference already in.
func (w *writer) record(n uint64, mayEnd bool) []byte {
	if w.count == 0 || (w.count >= blockSize && mayEnd) {
		w.blocks = append(w.blocks, [2]uint64{n, uint64(w.at - w.start)})
		w.start, w.count, w.prev = w.at, 0, 0
	}
	w.count++
	b := binary.AppendVarint(w.buf[:0], int64(n)-int64(w.prev))
	w.prev = n
	return b
}

// writeIndex writes the index of w.blocks.
func (w *writer) writeIndex() {
	b := binary.AppendUvarint(nil, uint64(len(w.blocks)))
	for _, bl := range w.blocks {
		b = binary.AppendUvarint(b, bl[0])
		b = binary.AppendUvarint(b, bl[1])
	}
	w.write(b)
}
