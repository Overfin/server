package tmdb

import (
	"encoding/json"
	"testing"
)

// Whatever a title's page holds, what Overfin keeps of it is checked:
// image paths of the one shape, dates as dates, years in range, and
// certifications short, plain and per real country code.
func FuzzDetails(f *testing.F) {
	f.Add([]byte(`{"id":949,"title":"Heat","release_date":"1995-12-15","poster_path":"/abcdefgh1.jpg","genres":[{"name":"Crime"}],
		"credits":{"cast":[{"id":1,"name":"Al Pacino","character":"Hanna","profile_path":"/norton1234.jpg"}]},
		"release_dates":{"results":[{"iso_3166_1":"US","release_dates":[{"certification":"R","type":3,"release_date":"1995-11-01T00:00:00.000Z"}]},
		{"iso_3166_1":"bad","release_dates":[{"certification":"X","type":3,"release_date":"soon"}]}]}}`), false)
	f.Add([]byte(`{"id":1399,"name":"Thrones","first_air_date":"2011-04-17","seasons":[{"season_number":1,"name":"Season 1","episode_count":10,"air_date":"2011-04-17"}],
		"content_ratings":{"results":[{"iso_3166_1":"US","rating":"TV-MA"}]}}`), true)
	f.Add([]byte(`{"id":1,"title":"<b>x</b>","poster_path":"../../etc/passwd","release_date":"0000"}`), false)
	f.Fuzz(func(t *testing.T, body []byte, series bool) {
		kind := "movie"
		if series {
			kind = "series"
		}
		var raw rawDetails
		if json.Unmarshal(body, &raw) != nil {
			return
		}
		if ti, ok := raw.title(kind); ok {
			if !ValidID(ti.TMDBID) || ti.Name == "" || ti.Kind != kind || (ti.Year != 0 && (ti.Year <= 1800 || ti.Year >= 3000)) ||
				(ti.Poster != "" && !imagePath.MatchString(ti.Poster)) || (ti.Backdrop != "" && !imagePath.MatchString(ti.Backdrop)) ||
				(ti.Language != "" && !isoLanguage.MatchString(ti.Language)) || len(ti.Name) > 300 || len(ti.Overview) > 2000 {
				t.Fatalf("title %+v", ti)
			}
			var cast []Person
			for _, p := range raw.Credits.Cast {
				cast = append(cast, Person{ID: p.ID, Name: p.Name, Character: p.Character, Profile: p.ProfilePath})
			}
			var seasons []Season
			for _, s := range raw.Seasons {
				seasons = append(seasons, Season{Number: s.SeasonNumber, Name: s.Name, EpisodeCount: s.EpisodeCount, AirDate: s.AirDate})
			}
			d := MakeDetails(ti, nil, cast, seasons)
			if len(d.Cast) > MaxCast {
				t.Fatalf("%d cast", len(d.Cast))
			}
			for _, c := range d.Cast {
				if c.Name == "" || (c.ID != 0 && !ValidID(c.ID)) || (c.Profile != "" && !imagePath.MatchString(c.Profile)) {
					t.Fatalf("cast %+v", c)
				}
			}
			for _, s := range d.Seasons {
				if s.Number < 0 || s.Number > 1000 || s.EpisodeCount < 0 || s.EpisodeCount > 10_000 || (s.AirDate != "" && !dateOnly.MatchString(s.AirDate)) {
					t.Fatalf("season %+v", s)
				}
			}
		}
		rel := raw.release(kind)
		for _, d := range []string{rel.Released, rel.AtHome} {
			if d != "" && !dateOnly.MatchString(d) {
				t.Fatalf("date %q", d)
			}
		}
		if rel.AtHome != "" && rel.Released != "" && rel.AtHome < rel.Released {
			t.Fatalf("at home %q before released %q", rel.AtHome, rel.Released)
		}
		for cc, certs := range rel.Ratings {
			if !country.MatchString(cc) || len(certs) == 0 || len(certs) > 20 {
				t.Fatalf("ratings %q: %q", cc, certs)
			}
			for _, c := range certs {
				if c == "" || len(c) > 16 || certification(c) != c {
					t.Fatalf("certification %q", c)
				}
			}
		}
	})
}
