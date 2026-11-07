package imdb

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Each day's votes are kept, for how fast titles gain them: what people
// watch now. A day's file holds every rated title's votes, gzipped varints
// (each ID as the difference from the one before, then its votes); it's
// named votes-YYYY-MM-DD.
const (
	votesPrefix = "votes-"
	// VotesKept is how many days of votes are kept.
	VotesKept = 35
)

// WriteVotes writes the votes of every title in title.ratings (as IMDb
// publishes it, gzipped) to the day's file in dir, and deletes the days
// past VotesKept. It returns how many titles it wrote.
func WriteVotes(dir, ratings string, day time.Time) (int, error) {
	in, err := os.Open(ratings)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	z, err := gzip.NewReader(in)
	if err != nil {
		return 0, fmt.Errorf("imdb: %s: %w", RatingsFile, err)
	}
	t, err := newTable(z, "tconst", "averageRating", "numVotes")
	if err != nil {
		return 0, fmt.Errorf("imdb: %s: %w", RatingsFile, err)
	}
	path := filepath.Join(dir, votesPrefix+day.UTC().Format(time.DateOnly))
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := writeVotes(f, t)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	return n, pruneVotes(dir, day)
}

func writeVotes(f *os.File, t *table) (int, error) {
	w := bufio.NewWriter(f)
	z := gzip.NewWriter(w)
	var prev uint64
	n := 0
	buf := make([]byte, 0, 2*binary.MaxVarintLen64)
	for {
		row, ok, err := t.next()
		if err != nil {
			return 0, fmt.Errorf("imdb: %s: %w", RatingsFile, err)
		}
		if !ok {
			break
		}
		id, valid := number(row[0])
		votes, verr := strconv.Atoi(row[2])
		if !valid || verr != nil || votes <= 0 {
			continue
		}
		buf = binary.AppendVarint(buf[:0], int64(id)-int64(prev))
		buf = binary.AppendUvarint(buf, uint64(votes))
		prev = id
		if _, err := z.Write(buf); err != nil {
			return 0, err
		}
		n++
	}
	if err := z.Close(); err != nil {
		return 0, err
	}
	if err := w.Flush(); err != nil {
		return 0, err
	}
	return n, f.Sync()
}

// pruneVotes deletes the days' files past VotesKept before day.
func pruneVotes(dir string, day time.Time) error {
	days, err := VoteDays(dir)
	if err != nil {
		return err
	}
	oldest := day.UTC().AddDate(0, 0, -VotesKept).Format(time.DateOnly)
	for _, d := range days {
		if d < oldest {
			if err := os.Remove(filepath.Join(dir, votesPrefix+d)); err != nil {
				return err
			}
		}
	}
	return nil
}

// VoteDays lists the days whose votes dir holds (YYYY-MM-DD), oldest first.
func VoteDays(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var days []string
	for _, e := range entries {
		d, ok := strings.CutPrefix(e.Name(), votesPrefix)
		if _, err := time.Parse(time.DateOnly, d); ok && err == nil {
			days = append(days, d)
		}
	}
	sort.Strings(days)
	return days, nil
}

// ReadVotes hands fn every title's votes on day (YYYY-MM-DD), from dir.
func ReadVotes(dir, day string, fn func(id string, votes int)) error {
	f, err := os.Open(filepath.Join(dir, votesPrefix+day))
	if err != nil {
		return err
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	r := bufio.NewReader(z)
	var prev int64
	for {
		d, err := binary.ReadVarint(r)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		votes, err := binary.ReadUvarint(r)
		if err != nil {
			return io.ErrUnexpectedEOF
		}
		prev += d
		if prev <= 0 || votes > 1<<40 {
			return fmt.Errorf("imdb: votes of %s: a broken file", day)
		}
		fn(text(uint64(prev)), int(votes))
	}
}
