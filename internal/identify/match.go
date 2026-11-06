package identify

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/overfin/server/internal/lang"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// Matching: which TMDB title a film or a series of the libraries is, from
// what its files say. An ID written in its folder or file name comes
// first; otherwise TMDB is searched by its name and year, and its first
// candidates weighed against the files. The best is taken; when it isn't
// clearly the one, it's said why (Result.Sure, Result.Reasons), for an
// admin to check in the panel.

// Source is TMDB, as matching asks it.
type Source interface {
	SearchKind(ctx context.Context, kind, query string, year int) ([]tmdb.Found, error)
	Title(ctx context.Context, kind string, tmdbID int) (*tmdb.Full, error)
	Find(ctx context.Context, kind, source, id string) (int, error)
}

// Files are what the libraries know of a film or a series, from its files
// alone.
type Files struct {
	Kind         string // movie, series
	Folder, File string // the last names of where it is
	RuntimeMs    int64  // a film's length; 0 when unknown
	// Audio are the languages of its audio tracks, as files write them
	// ("fre", "eng"…).
	Audio []string
	// Episodes counts a series' episodes, by season.
	Episodes map[int]int
}

// Candidate is a TMDB title weighed for a film or a series.
type Candidate struct {
	TMDB         string
	Name         string
	Year         int
	OriginalName string
	Language     string
	Score        int
}

// Result is what matching found: the TMDB title taken ("" for none),
// whether it's clearly the one, why not when it isn't, and the candidates
// weighed, best first.
type Result struct {
	TMDB       string
	Sure       bool
	Reasons    []Reason
	Candidates []Candidate
}

// candidatesWeighed is how many of TMDB's search results are looked at
// closely (their pages fetched).
const candidatesWeighed = 5

// The scores: a name that is one of the title's counts most; then the
// year, a film's length or a series' seasons, its language among the
// audio's, and how known it is. A candidate is sure from sureScore with
// sureMargin over the next.
const (
	nameScore, nameInScore   = 50, 25
	yearScore, yearNearScore = 20, 15
	yearOffScore             = -20
	fitScore, misfitScore    = 15, -25
	languageScore            = 10
	knownMost                = 5
	sureScore, sureMargin    = 60, 15
)

// Match finds which TMDB title the files are.
func Match(ctx context.Context, src Source, f Files) (Result, error) {
	if f.Kind != "movie" && f.Kind != "series" {
		return Result{}, fmt.Errorf("identify: not a film or a series: %q", f.Kind)
	}
	n := parse(f.Folder)
	if f.Kind == "movie" {
		file := parse(f.File)
		if n.name == "" {
			n.name, n.year = file.name, file.year
		}
		n.tmdb, n.imdb, n.tvdb = or(n.tmdb, file.tmdb), or(n.imdb, file.imdb), or(n.tvdb, file.tvdb)
	}
	id, err := byID(ctx, src, f.Kind, n)
	if err != nil {
		return Result{}, err
	}
	if id != "" {
		// Written by whoever named the files: taken, and checked like any
		// match; what contradicts it is said, never undone.
		full, err := page(ctx, src, f.Kind, id)
		if err == nil {
			c := candidate(full)
			c.Score = score(n, f, full, 0)
			reasons := checks(libraryTitle(f, full), n)
			return Result{TMDB: id, Sure: len(reasons) == 0, Reasons: reasons, Candidates: []Candidate{c}}, nil
		}
		if !errors.Is(err, tmdb.ErrNotFound) {
			return Result{}, err
		}
		// TMDB doesn't know that ID (as this kind): searched for instead.
	}
	return search(ctx, src, n, f)
}

// byID is the TMDB ID the names carry, directly or through IMDb's or
// TVDB's; "" without one.
func byID(ctx context.Context, src Source, kind string, n named) (string, error) {
	if n.tmdb != "" {
		return n.tmdb, nil
	}
	for _, x := range []struct{ source, id string }{{"imdb", n.imdb}, {"tvdb", n.tvdb}} {
		if x.id == "" {
			continue
		}
		found, err := src.Find(ctx, kind, x.source, x.id)
		switch {
		case err == nil && found > 0:
			return strconv.Itoa(found), nil
		case err != nil && !errors.Is(err, tmdb.ErrNotFound):
			return "", err
		}
	}
	return "", nil
}

// search looks for the title by its name: with its year, then without
// when nothing came; its first candidates weighed, the best taken.
func search(ctx context.Context, src Source, n named, f Files) (Result, error) {
	if n.name == "" {
		return Result{Reasons: []Reason{{"no-name", "Its folder and file say no name to search for."}}}, nil
	}
	found, err := src.SearchKind(ctx, f.Kind, n.name, n.year)
	if err == nil && len(found) == 0 && n.year > 0 {
		found, err = src.SearchKind(ctx, f.Kind, n.name, 0)
	}
	if err != nil && !errors.Is(err, tmdb.ErrNotFound) {
		return Result{}, err
	}
	if len(found) == 0 {
		return Result{Reasons: []Reason{{"no-tmdb", fmt.Sprintf("TMDB found nothing named “%s”.", n.name)}}}, nil
	}
	var out Result
	for i, hit := range found[:min(len(found), candidatesWeighed)] {
		full, err := page(ctx, src, f.Kind, strconv.Itoa(hit.TMDBID))
		if errors.Is(err, tmdb.ErrNotFound) {
			continue
		}
		if err != nil {
			return Result{}, err
		}
		c := candidate(full)
		c.Score = score(n, f, full, i) + known(hit.Votes)
		out.Candidates = append(out.Candidates, c)
	}
	if len(out.Candidates) == 0 {
		return Result{Reasons: []Reason{{"no-tmdb", fmt.Sprintf("TMDB found nothing named “%s”.", n.name)}}}, nil
	}
	// The best first; equals keep TMDB's order.
	slices.SortStableFunc(out.Candidates, func(a, b Candidate) int { return b.Score - a.Score })
	best := out.Candidates[0]
	out.TMDB = best.TMDB
	out.Sure = best.Score >= sureScore && (len(out.Candidates) == 1 || best.Score-out.Candidates[1].Score >= sureMargin)
	switch {
	case out.Sure:
	case best.Score < sureScore:
		out.Reasons = []Reason{{"unsure", fmt.Sprintf("No title on TMDB clearly fits “%s”; the closest is taken.", n.name)}}
	default:
		next := out.Candidates[1]
		out.Reasons = []Reason{{"several", fmt.Sprintf("Several titles on TMDB fit: “%s” (%s) and “%s” (%s).",
			best.Name, yearText(best.Year), next.Name, yearText(next.Year))}}
	}
	return out, nil
}

// score weighs a candidate (TMDB's rank-th result) against the names and
// files.
func score(n named, f Files, full *tmdb.Full, rank int) int {
	s := 0
	lt := libraryTitle(f, full)
	switch want := simple(n.name); {
	case want == "":
	case exactName(want, lt):
		s += nameScore
	case matches(n.name, lt):
		s += nameInScore
	}
	if n.year > 0 && full.Year > 0 {
		switch d := abs(n.year - full.Year); {
		case d == 0:
			s += yearScore
		case d == 1:
			s += yearNearScore
		default:
			s += yearOffScore
		}
	}
	// A film's length, a series' seasons: as Identification's doubts
	// judge them.
	if f.Kind == "movie" && f.RuntimeMs > 0 && full.Runtime > 0 {
		if _, off := runtimeDoubt(lt); off {
			s += misfitScore
		} else {
			s += fitScore
		}
	}
	if f.Kind == "series" && len(f.Episodes) > 0 && len(full.Seasons) > 0 {
		if _, off := seasons(lt); off {
			s += misfitScore
		} else {
			s += fitScore
		}
	}
	// Dubbed releases are many: the original language among the audio's
	// counts for it, its absence not against it.
	if full.Language != "" && slices.ContainsFunc(f.Audio, func(a string) bool { return lang.Same(a, full.Language) }) {
		s += languageScore
	}
	// TMDB's order, for equals: its first results a little ahead.
	return s + max(0, 3-rank)
}

// known is a little more for titles many people voted on: a spin-off or
// a TV special rarely has as many as the work itself.
func known(votes int) int {
	return min(knownMost, int(math.Log10(float64(votes)+1)))
}

// exactName says whether name (simple) is one of the title's names.
func exactName(name string, t store.LibraryTitle) bool {
	for _, x := range append([]string{t.TMDBName, t.Details.OriginalName}, t.Details.Names...) {
		if simple(x) == name {
			return true
		}
	}
	return false
}

// page is a title's page on TMDB.
func page(ctx context.Context, src Source, kind, id string) (*tmdb.Full, error) {
	n, err := strconv.Atoi(id)
	if err != nil || !tmdb.ValidID(n) {
		return nil, tmdb.ErrNotFound
	}
	full, err := src.Title(ctx, kind, n)
	if err == nil && full == nil {
		return nil, tmdb.ErrNotFound
	}
	return full, err
}

func candidate(full *tmdb.Full) Candidate {
	return Candidate{TMDB: strconv.Itoa(full.TMDBID), Name: full.Name, Year: full.Year, OriginalName: full.OriginalName,
		Language: full.Language}
}

// libraryTitle is the files taken for full, as Identification's doubts
// read them.
func libraryTitle(f Files, full *tmdb.Full) store.LibraryTitle {
	t := store.LibraryTitle{Kind: f.Kind, FolderName: f.Folder, FileName: f.File, RuntimeMs: f.RuntimeMs,
		TMDB: strconv.Itoa(full.TMDBID), TMDBName: full.Name, TMDBYear: full.Year, Episodes: f.Episodes,
		Details: store.TitleDetails{Runtime: full.Runtime, OriginalName: full.OriginalName, Language: full.Language, Names: full.Names}}
	for _, se := range full.Seasons {
		t.Details.Seasons = append(t.Details.Seasons, store.SeasonInfo{Number: se.Number, Name: se.Name, EpisodeCount: se.EpisodeCount})
	}
	return t
}

func yearText(y int) string {
	if y == 0 {
		return "year unknown"
	}
	return strconv.Itoa(y)
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
