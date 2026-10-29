package store

import (
	"context"
	"database/sql"
	"slices"
)

// Ages: each country's ratings as the youngest age they allow, so a title
// gets one age from any country's rating, and kids profiles and
// categories go by age, the same everywhere.
//
// A title's age is its rating's in the server's ratings country, when it
// has one there; else the middle of the others' (the higher of the two
// middle ones, with an even count). With several ratings in one country
// (a cut, say), the highest counts.
//
// Ratings that only advise parents (PG and the like) have no age by law:
// they count as 8 here. Ratings that say nothing of age (exempt, not
// rated, refused) are left out. Countries not listed are left out too.

// guidance is the age of a rating that only advises parents.
const guidance = 8

// filmAges and tvAges are the ages of films' and series' ratings, by
// country (ISO 3166-1) then rating, as TMDB writes them.
var filmAges = map[string]map[string]int{
	"US": {"G": 0, "PG": guidance, "PG-13": 13, "R": 17, "NC-17": 18},
	// With Québec's (13+, 16+, 18+).
	"CA": {"G": 0, "PG": guidance, "13+": 13, "14A": 14, "16+": 16, "18A": 18, "18+": 18, "R": 18, "A": 18},
	"GB": {"U": 0, "PG": guidance, "12A": 12, "12": 12, "15": 15, "18": 18, "R18": 18},
	"IE": {"G": 0, "PG": guidance, "12": 12, "12A": 12, "15": 15, "15A": 15, "16": 16, "18": 18},
	"AU": {"G": 0, "PG": guidance, "M": 15, "MA 15+": 15, "R 18+": 18, "X 18+": 18},
	// RP: restricted unless with a parent; counted at the age.
	"NZ": {"G": 0, "PG": guidance, "M": 16, "R13": 13, "RP13": 13, "R15": 15, "R16": 16, "RP16": 16, "R18": 18, "RP18": 18, "R": 18},
	"FR": {"TP": 0, "10": 10, "12": 12, "16": 16, "18": 18},
	"DE": {"0": 0, "6": 6, "12": 12, "16": 16, "18": 18},
	"AT": {"AA": 0, "6": 6, "8": 8, "10": 10, "12": 12, "14": 14, "16": 16, "18": 18},
	"CH": {"0": 0, "6": 6, "8": 8, "10": 10, "12": 12, "14": 14, "16": 16, "18": 18},
	"NL": {"AL": 0, "6": 6, "9": 9, "12": 12, "14": 14, "16": 16, "18": 18},
	"BE": {"AL": 0, "KT": 0, "6": 6, "9": 9, "12": 12, "14": 14, "16": 16, "18": 18, "KNT": 16},
	"ES": {"A": 0, "Ai": 0, "7": 7, "7i": 7, "12": 12, "16": 16, "18": 18, "X": 18},
	"PT": {"Públicos": 0, "M/3": 3, "M/6": 6, "M/12": 12, "M/14": 14, "M/16": 16, "M/18": 18},
	"IT": {"T": 0, "6+": 6, "10+": 10, "14+": 14, "18+": 18, "VM14": 14, "VM18": 18},
	"SE": {"Btl": 0, "7": 7, "11": 11, "15": 15},
	"DK": {"A": 0, "7": 7, "11": 11, "15": 15},
	"NO": {"A": 0, "6": 6, "9": 9, "12": 12, "15": 15, "18": 18},
	"FI": {"S": 0, "K-7": 7, "K-12": 12, "K-16": 16, "K-18": 18, "7": 7, "12": 12, "16": 16, "18": 18},
	"BR": {"L": 0, "6": 6, "10": 10, "12": 12, "14": 14, "16": 16, "18": 18},
	"MX": {"AA": 0, "A": 0, "B": 12, "B-15": 15, "C": 18, "D": 18},
	"KR": {"ALL": 0, "12": 12, "15": 15, "19": 19, "Restricted Screening": 19},
	"JP": {"G": 0, "PG12": 12, "R15+": 15, "R18+": 18},
	"SG": {"G": 0, "PG": guidance, "PG13": 13, "NC16": 16, "M18": 18, "R21": 21},
}

var tvAges = map[string]map[string]int{
	"US": {"TV-Y": 0, "TV-Y7": 7, "TV-G": 0, "TV-PG": guidance, "TV-14": 14, "TV-MA": 17},
	"CA": {"C": 0, "C8": 8, "G": 0, "PG": guidance, "13+": 13, "14+": 14, "16+": 16, "18+": 18},
	"GB": {"U": 0, "PG": guidance, "12A": 12, "12": 12, "15": 15, "18": 18, "R18": 18},
	// And RTÉ's own labels.
	"IE": {"G": 0, "PG": guidance, "12": 12, "15": 15, "16": 16, "18": 18, "GA": 0, "Ch": 0, "YA": 12, "PS": 15, "MA": 18},
	"AU": {"P": 0, "C": 0, "G": 0, "PG": guidance, "M": 15, "MA 15+": 15, "AV 15+": 15, "R 18+": 18},
	"NZ": {"G": 0, "PG": guidance, "M": 16, "16": 16, "18": 18},
	"FR": {"TP": 0, "10": 10, "12": 12, "16": 16, "18": 18},
	"AT": {"AA": 0, "6": 6, "8": 8, "10": 10, "12": 12, "14": 14, "16": 16, "18": 18},
	"CH": {"0": 0, "6": 6, "12": 12, "16": 16, "18": 18},
	"BE": {"AL": 0, "6": 6, "9": 9, "12": 12, "14": 14, "16": 16, "18": 18},
	"DE": {"0": 0, "6": 6, "12": 12, "16": 16, "18": 18},
	"NL": {"AL": 0, "6": 6, "9": 9, "12": 12, "14": 14, "16": 16, "18": 18},
	"ES": {"TP": 0, "ERI": 0, "Infantil": 0, "7": 7, "10": 10, "12": 12, "13": 13, "16": 16, "18": 18},
	"PT": {"T": 0, "10": 10, "10AP": 10, "12": 12, "12AP": 12, "16": 16, "18": 18},
	"BR": {"L": 0, "6": 6, "10": 10, "12": 12, "14": 14, "16": 16, "18": 18},
	"MX": {"AA": 0, "A": 0, "B": 12, "B-15": 15, "C": 18, "D": 18},
	"KR": {"ALL": 0, "7": 7, "12": 12, "15": 15, "19": 19},
	"SG": {"G": 0, "PG": guidance, "PG13": 13, "NC16": 16, "M18": 18, "R21": 21},
	"IT": {"T": 0, "BA": guidance, "VM12": 12, "VM14": 14, "VM18": 18},
	"SE": {"Btl": 0, "Från 7 år": 7, "Från 11 år": 11, "Från 15 år": 15, "7": 7, "11": 11, "15": 15},
	"NO": {"A": 0, "6": 6, "9": 9, "12": 12, "15": 15, "18": 18},
	"DK": {"A": 0, "7": 7, "11": 11, "15": 15},
	"FI": {"S": 0, "K7": 7, "K12": 12, "K16": 16, "K18": 18, "7": 7, "12": 12, "16": 16, "18": 18},
}

// RatingAge is a rating's age, ok false when it says nothing of age.
func RatingAge(kind, country, certification string) (int, bool) {
	table := filmAges
	if kind == "series" {
		table = tvAges
	}
	age, ok := table[country][certification]
	return age, ok
}

// TitleAge is a title's age from its ratings (by country): the ratings
// country's when it has one, else the middle of the others'.
func TitleAge(kind string, ratings map[string][]string, country string) (int, bool) {
	// One age per country: its highest.
	byCountry := map[string]int{}
	for c, certs := range ratings {
		for _, cert := range certs {
			if age, ok := RatingAge(kind, c, cert); ok {
				if old, seen := byCountry[c]; !seen || age > old {
					byCountry[c] = age
				}
			}
		}
	}
	if age, ok := byCountry[country]; ok {
		return age, true
	}
	if len(byCountry) == 0 {
		return 0, false
	}
	ages := make([]int, 0, len(byCountry))
	for _, a := range byCountry {
		ages = append(ages, a)
	}
	slices.Sort(ages)
	return ages[len(ages)/2], true
}

// setTitleAge keeps a title's age, or forgets it when its ratings give
// none.
func setTitleAge(ctx context.Context, ex execer, kind, tmdb string, ratings map[string][]string, country string) error {
	age, ok := TitleAge(kind, ratings, country)
	if !ok {
		_, err := ex.ExecContext(ctx, `DELETE FROM title_ages WHERE kind = ? AND tmdb = ?`, kind, tmdb)
		return err
	}
	_, err := ex.ExecContext(ctx, `INSERT INTO title_ages (kind, tmdb, age) VALUES (?, ?, ?)
		ON CONFLICT (kind, tmdb) DO UPDATE SET age = excluded.age`, kind, tmdb, age)
	return err
}

// RefreshTitleAges works every title's age out again from its ratings:
// at start (the table of ages may have changed with Overfin), and when
// the ratings country changes.
func (s *Store) RefreshTitleAges(ctx context.Context) error {
	country, err := s.RatingsCountry(ctx)
	if err != nil {
		return err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT kind, tmdb, country, certification FROM title_ratings ORDER BY kind, tmdb`)
	if err != nil {
		return err
	}
	type key struct{ kind, tmdb string }
	all := map[key]map[string][]string{}
	for rows.Next() {
		var k key
		var c, cert string
		if err := rows.Scan(&k.kind, &k.tmdb, &c, &cert); err != nil {
			rows.Close()
			return err
		}
		if all[k] == nil {
			all[k] = map[string][]string{}
		}
		all[k][c] = append(all[k][c], cert)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM title_ages`); err != nil {
		return err
	}
	for k, ratings := range all {
		if err := setTitleAge(ctx, tx, k.kind, k.tmdb, ratings, country); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// execer runs a statement: the database, or a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// AgesFromCeilings gives kids profiles that still have rating ceilings
// (from before ages) an age: the younger of their two ceilings' ages, in
// the ratings country. A ceiling with no age leaves the profile without
// one, so it sees nothing until its owner picks one, as before.
func (s *Store) AgesFromCeilings(ctx context.Context) (int, error) {
	country, err := s.RatingsCountry(ctx)
	if err != nil {
		return 0, err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT id, coalesce(movie_ceiling, ''), coalesce(series_ceiling, '') FROM profiles
		WHERE kind = 'kids' AND max_age IS NULL AND (movie_ceiling IS NOT NULL OR series_ceiling IS NOT NULL)`)
	if err != nil {
		return 0, err
	}
	type old struct{ id, movie, series string }
	var all []old
	for rows.Next() {
		var o old
		if err := rows.Scan(&o.id, &o.movie, &o.series); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, o := range all {
		movie, okM := RatingAge("movie", country, o.movie)
		series, okS := RatingAge("series", country, o.series)
		// Both ceilings were needed to see anything: so are both ages.
		if okM && okS {
			if _, err := s.db.ExecContext(ctx, `UPDATE profiles SET max_age = ?, movie_ceiling = NULL, series_ceiling = NULL
				WHERE id = ?`, min(movie, series), o.id); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}
