package imdb

import (
	"bufio"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The datasets Build reads, as IMDb names them.
const (
	BasicsFile   = "title.basics.tsv.gz"
	EpisodesFile = "title.episode.tsv.gz"
	RatingsFile  = "title.ratings.tsv.gz"
)

// kinds are the types of titles kept, as the datasets name them: shorts,
// videos, video games and the like aren't.
var kinds = map[string]Kind{
	"movie": Movie, "tvMovie": Movie,
	"tvSeries": Series, "tvMiniSeries": Series,
	"tvEpisode": Episode, "tvSpecial": Special,
}

// maxSeasons bounds the seasons a build keeps in memory: far above IMDb's
// (well under a million).
const maxSeasons = 10_000_000

// Build writes a file Open reads from the three datasets, uncompressed:
// title.basics, title.episode and title.ratings. They're read side by
// side, once each, in IMDb's order (by ID, as text), so memory stays small
// whatever their size: only each season's sums are kept until the end. It
// returns how many titles it wrote.
func Build(out io.Writer, basics, episodes, ratings io.Reader, now time.Time) (int, error) {
	b, err := newTable(basics, "tconst", "titleType", "isAdult", "startYear", "endYear", "runtimeMinutes")
	if err != nil {
		return 0, fmt.Errorf("imdb: %s: %w", BasicsFile, err)
	}
	e, err := newTable(episodes, "tconst", "parentTconst", "seasonNumber", "episodeNumber")
	if err != nil {
		return 0, fmt.Errorf("imdb: %s: %w", EpisodesFile, err)
	}
	r, err := newTable(ratings, "tconst", "averageRating", "numVotes")
	if err != nil {
		return 0, fmt.Errorf("imdb: %s: %w", RatingsFile, err)
	}

	w := &writer{w: bufio.NewWriterSize(out, 1<<20), buf: make([]byte, 0, 64)}
	w.write([]byte(magic))
	w.begin()
	type sums struct{ weighted, votes, episodes float64 }
	seasons := map[[2]uint64]*sums{} // series number, season → its rated episodes' sums
	titles := 0
	for {
		row, ok, err := b.next()
		if err != nil {
			return 0, fmt.Errorf("imdb: %s: %w", BasicsFile, err)
		}
		if !ok {
			break
		}
		kind, keep := kinds[row[1]]
		n, valid := number(row[0])
		if !keep || !valid {
			continue
		}
		t := Title{Kind: kind, Adult: row[2] == "1", Start: small(row[3]), Minutes: small(row[5])}
		if kind == Series {
			t.End = small(row[4])
		}
		var series uint64
		if kind == Episode {
			ep, found, err := e.find(row[0])
			if err != nil {
				return 0, fmt.Errorf("imdb: %s: %w", EpisodesFile, err)
			}
			if found {
				series, _ = number(ep[1])
				t.Season, t.Number = small(ep[2]), small(ep[3])
			}
		}
		rating, votes := 0, 0
		if rt, found, err := r.find(row[0]); err != nil {
			return 0, fmt.Errorf("imdb: %s: %w", RatingsFile, err)
		} else if found {
			f, ferr := strconv.ParseFloat(rt[1], 64)
			v, verr := strconv.Atoi(rt[2])
			if ferr == nil && verr == nil && f >= 1 && f <= 10 && v > 0 {
				rating, votes = int(math.Round(f*10)), v
			}
		}

		rec := w.record(n, true)
		flags := byte(t.Kind)
		if t.Adult {
			flags |= 0x80
		}
		rec = append(rec, flags)
		rec = binary.AppendUvarint(rec, uint64(t.Start))
		if kind == Series {
			rec = binary.AppendUvarint(rec, uint64(t.End))
		}
		rec = binary.AppendUvarint(rec, uint64(t.Minutes))
		if kind == Episode {
			rec = binary.AppendUvarint(rec, series)
			rec = binary.AppendUvarint(rec, uint64(t.Season))
			rec = binary.AppendUvarint(rec, uint64(t.Number))
		}
		rec = append(rec, byte(rating))
		if rating > 0 {
			rec = binary.AppendUvarint(rec, uint64(votes))
		}
		w.write(rec)
		titles++

		if kind == Episode && series > 0 && rating > 0 && t.Season > 0 {
			k := [2]uint64{series, uint64(t.Season)}
			s := seasons[k]
			if s == nil {
				if len(seasons) >= maxSeasons {
					return 0, errors.New("imdb: more seasons than IMDb has")
				}
				s = &sums{}
				seasons[k] = s
			}
			s.weighted += float64(rating) / 10 * float64(votes)
			s.votes += float64(votes)
			s.episodes++
		}
	}

	// The rest of the other two, read to the end: a row out of order
	// would have hidden every row after it.
	for name, t := range map[string]*table{EpisodesFile: e, RatingsFile: r} {
		for {
			_, ok, err := t.next()
			if err != nil {
				return 0, fmt.Errorf("imdb: %s: %w", name, err)
			}
			if !ok {
				break
			}
		}
	}

	// The seasons, by series in IMDb's order (as text), then by number.
	seasonsAt := w.at
	titleBlocks := w.blocks
	w.begin()
	keys := make([][2]uint64, 0, len(seasons))
	for k := range seasons {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b [2]uint64) int {
		if a[0] != b[0] {
			return strings.Compare(text(a[0]), text(b[0]))
		}
		return cmp.Compare(a[1], b[1])
	})
	for i, k := range keys {
		s := seasons[k]
		first := i == 0 || keys[i-1][0] != k[0]
		rec := w.record(k[0], first)
		rec = binary.AppendUvarint(rec, k[1])
		rec = binary.AppendUvarint(rec, uint64(math.Round(s.weighted/s.votes*100)))
		rec = binary.AppendUvarint(rec, uint64(s.votes))
		rec = binary.AppendUvarint(rec, uint64(s.episodes))
		w.write(rec)
	}

	indexAt := w.at
	seasonBlocks := w.blocks
	w.blocks = titleBlocks
	w.writeIndex()
	w.blocks = seasonBlocks
	w.writeIndex()
	foot := make([]byte, 0, footerSize)
	foot = binary.LittleEndian.AppendUint64(foot, uint64(seasonsAt))
	foot = binary.LittleEndian.AppendUint64(foot, uint64(indexAt))
	foot = binary.LittleEndian.AppendUint64(foot, uint64(titles))
	foot = binary.LittleEndian.AppendUint64(foot, uint64(now.Unix()))
	w.write(append(foot, magic...))
	if w.err == nil {
		w.err = w.w.Flush()
	}
	if w.err != nil {
		return 0, w.err
	}
	return titles, nil
}

// small is a dataset's number, 0 when it has none (\N) or it's out of
// bounds.
func small(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 65535 {
		return 0
	}
	return n
}

// table reads a dataset row by row: tab-separated, a header first, sorted
// by its first column.
type table struct {
	s    *bufio.Scanner
	cols []int // the columns asked for, in the header's numbering
	row  []string
	last string // the ID of the row before
	// held is a row read ahead by find, not yet asked for.
	held bool
	done bool
}

// maxLine bounds a dataset's line.
const maxLine = 1 << 20

func newTable(r io.Reader, names ...string) (*table, error) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64<<10), maxLine)
	if !s.Scan() {
		if err := s.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("empty")
	}
	header := strings.Split(s.Text(), "\t")
	t := &table{s: s, row: make([]string, len(names))}
	for _, name := range names {
		i := slices.Index(header, name)
		if i < 0 {
			return nil, fmt.Errorf("no column %q", name)
		}
		t.cols = append(t.cols, i)
	}
	if t.cols[0] != 0 {
		return nil, errors.New("the ID isn't the first column")
	}
	return t, nil
}

// next reads the next row: the columns asked for, in their order. Rows
// out of order are an error: Build depends on it.
func (t *table) next() ([]string, bool, error) {
	if t.held {
		t.held = false
		return t.row, true, nil
	}
	if t.done {
		return nil, false, nil
	}
	if !t.s.Scan() {
		t.done = true
		return nil, false, t.s.Err()
	}
	fields := strings.Split(t.s.Text(), "\t")
	for i, c := range t.cols {
		if c >= len(fields) {
			return nil, false, fmt.Errorf("a row with %d columns", len(fields))
		}
		t.row[i] = fields[c]
	}
	if t.row[0] <= t.last {
		return nil, false, fmt.Errorf("not sorted: %s after %s", t.row[0], t.last)
	}
	t.last = t.row[0]
	return t.row, true, nil
}

// find reads on to the row of id: rows before it are skipped, a row after
// it is kept for the next find. Asked in IMDb's order, it reads the table
// once.
func (t *table) find(id string) ([]string, bool, error) {
	for {
		row, ok, err := t.next()
		if err != nil || !ok {
			return nil, false, err
		}
		switch {
		case row[0] == id:
			return row, true, nil
		case row[0] > id:
			t.held = true
			return nil, false, nil
		}
	}
}
