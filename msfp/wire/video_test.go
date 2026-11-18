package wire

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Video title keys in their one canonical form (MSFP video, section 4):
// any other form of the same title is refused.
func TestParseVideoKey(t *testing.T) {
	valid := map[string]VideoKey{
		"movie/tmdb/949":             {Kind: "movie", TMDB: 949},
		"movie/tmdb/9999999999":      {Kind: "movie", TMDB: 9_999_999_999},
		"series/tmdb/1399":           {Kind: "series", TMDB: 1399},
		"series/tmdb/1399/s0":        {Kind: "season", TMDB: 1399},
		"series/tmdb/1399/s9999":     {Kind: "season", TMDB: 1399, Season: 9999},
		"series/tmdb/1399/e63056":    {Kind: "episode", TMDB: 1399, EpisodeID: 63056},
		"series/tmdb/1399/e1":        {Kind: "episode", TMDB: 1399, EpisodeID: 1},
		"series/tmdb/1399/s1/e2":     {Kind: "episode", TMDB: 1399, Season: 1, Episode: 2},
		"series/tmdb/1399/s1/e0":     {Kind: "episode", TMDB: 1399, Season: 1},
		"series/tmdb/1399/s0/e99999": {Kind: "episode", TMDB: 1399, Episode: 99999},
		"series/tmdb/1/e9999999999":  {Kind: "episode", TMDB: 1, EpisodeID: 9_999_999_999},
	}
	for key, want := range valid {
		got, ok := ParseVideoKey(key)
		if !ok || got != want || got.String() != key {
			t.Errorf("%s: %+v %v, want %+v", key, got, ok, want)
		}
	}
	for _, key := range []string{
		"", "movie/tmdb/", "movie/tmdb/0", "movie/tmdb/0949", "movie/tmdb/10000000000", "movie/tmdb/-1",
		"movie/tmdb/949/s1", "movie/imdb/tt0133093", "series/tvdb/81189", "Movie/tmdb/949", "movie/TMDB/949",
		"season/tmdb/1399", "episode/tmdb/1399", "series/tmdb/1399/", "series/tmdb/1399/s01", "series/tmdb/1399/s10000",
		"series/tmdb/1399/e0", "series/tmdb/1399/e063056", "series/tmdb/1399/e12345678901",
		"series/tmdb/1399/s1/e01", "series/tmdb/1399/s1/e100000", "series/tmdb/1399/s1/e", "series/tmdb/1399/s/e1",
		"series/tmdb/1399/e63056/e1", "series/tmdb/1399/s1/e2/x", "series/tmdb/+1399", "series/tmdb/1399/s+1",
		"series/tmdb/1399/s1/s2", "movie/tmdb/949 ", "item/0123456789abcdef0123456789abcdef",
	} {
		if k, ok := ParseVideoKey(key); ok {
			t.Errorf("%q accepted as %+v", key, k)
		}
	}
}

func FuzzParseVideoKey(f *testing.F) {
	for _, k := range []string{"movie/tmdb/949", "series/tmdb/1399/s1/e0", "series/tmdb/1399/e63056", "series/tmdb/1/s01"} {
		f.Add(k)
	}
	f.Fuzz(func(t *testing.T, key string) {
		k, ok := ParseVideoKey(key)
		if ok && k.String() != key {
			t.Fatalf("%q read as %q", key, k.String())
		}
	})
}

// What a receiver keeps of a title (catalog section 3.3, video section
// 3.1): a bad member skipped, or counted as absent, not the whole title.
func TestReadVideoTitle(t *testing.T) {
	read := func(s string) (VideoTitle, bool) { return ReadVideoTitle(json.RawMessage(s)) }
	if _, ok := read(`{"key":"movie/tmdb/949","kind":"series","library":"L"}`); ok {
		t.Error("a kind other than the key's kept")
	}
	if _, ok := read(`{"key":"movie/tmdb/0949","kind":"movie","library":"L"}`); ok {
		t.Error("a key not canonical kept")
	}
	for _, s := range []string{`"x"`, `{"key":1,"kind":"movie","library":"L"}`, `{"key":"movie/tmdb/949","kind":"movie","library":3}`} {
		if _, ok := read(s); ok {
			t.Errorf("%s kept", s)
		}
	}
	tags := `["fr","FR","fr-CA","x","fr_CA",3,"` + strings.Repeat("a", 13) + `","de","en","es","it","nl","pt","ja","ko","zh","ru","sv"]`
	got, ok := read(`{"key":"series/tmdb/1399/e63056","kind":"episode","library":"L","audio":` + tags + `,"subtitles":"fr",
		"with":["series/tmdb/1399/e63057","series/tmdb/1399/e63056","series/tmdb/2/e1","series/tmdb/1399/s1/e3",7,"series/tmdb/1399/s1"],
		"ids":{"imdb":"tt1480055","tvdb":"0123","wikidata":"Q1"}}`)
	if !ok {
		t.Fatal("refused")
	}
	if want := []string{"fr", "fr-CA", "de", "en", "es", "it", "nl", "pt", "ja", "ko", "zh", "ru"}; !slices.Equal(got.Audio, want) {
		t.Errorf("audio %v", got.Audio)
	}
	if got.Subtitles != nil {
		t.Errorf("subtitles that aren't a list: %v", got.Subtitles)
	}
	if want := []string{"series/tmdb/1399/e63057", "series/tmdb/1399/s1/e3"}; !slices.Equal(got.With, want) {
		t.Errorf("with %v", got.With)
	}
	if len(got.IDs) != 1 || got.IDs["imdb"] != "tt1480055" {
		t.Errorf("ids %v", got.IDs)
	}
	// ids that isn't an object, with that isn't a list: absent.
	got, ok = read(`{"key":"movie/tmdb/949","kind":"movie","library":"L","ids":["tt0133093"],"with":"x","audio":["en"]}`)
	if !ok || got.IDs != nil || got.With != nil || !slices.Equal(got.Audio, []string{"en"}) {
		t.Errorf("%+v %v", got, ok)
	}
	// Seasons and series have no languages, seasons no ids, films no with.
	got, _ = read(`{"key":"series/tmdb/1399/s1","kind":"season","library":"L","audio":["en"],"ids":{"imdb":"tt0944947"}}`)
	if got.Audio != nil || got.IDs != nil {
		t.Errorf("season %+v", got)
	}
	got, _ = read(`{"key":"movie/tmdb/949","kind":"movie","library":"L","with":["series/tmdb/1399/e1"]}`)
	if got.With != nil {
		t.Errorf("film %+v", got)
	}
	// At most 16 in with.
	var with []string
	for i := range 20 {
		with = append(with, VideoKey{Kind: "episode", TMDB: 1, EpisodeID: int64(i + 2)}.String())
	}
	if n := len(CleanWith(VideoKey{Kind: "episode", TMDB: 1, EpisodeID: 1}, with)); n != MaxWith {
		t.Errorf("%d kept in with", n)
	}
}

func TestCleanTags(t *testing.T) {
	got := CleanTags([]string{"fr-CA", "fr-ca", "", "x", "<script>", "es-419", "toolongtagvalue"})
	if !slices.Equal(got, []string{"fr-CA", "es-419"}) {
		t.Fatalf("%v", got)
	}
}

func TestOtherID(t *testing.T) {
	for _, c := range []struct {
		scheme, value string
		ok            bool
	}{
		{"imdb", "tt0133093", true}, {"imdb", "tt1234567890", true}, {"imdb", "tt012345", false},
		{"imdb", "tt12345678901", false}, {"imdb", "nm0000206", false}, {"imdb", "TT0133093", false},
		{"tvdb", "81189", true}, {"tvdb", "9999999999", true}, {"tvdb", "081189", false}, {"tvdb", "0", false},
		{"tvdb", "12345678901", false}, {"tmdb", "949", false},
	} {
		if OtherID(c.scheme, c.value) != c.ok {
			t.Errorf("%s %s: %v", c.scheme, c.value, !c.ok)
		}
	}
}

// The cases of vectors/v1/video.json: what a caller keeps of each title,
// its picture included.
func TestVideoVectors(t *testing.T) {
	var v struct {
		Libraries []string
		Titles    []struct {
			Name, Text, Output string
			Valid              bool
		}
	}
	readVectors(t, "video.json", &v)
	if len(v.Titles) == 0 {
		t.Fatal("no video cases")
	}
	for _, c := range v.Titles {
		got, ok := ReadVideoTitle(json.RawMessage(c.Text))
		if ok && !slices.Contains(v.Libraries, got.Library) {
			ok = false // the caller's check: a library it asked for
		}
		if ok != c.Valid {
			t.Errorf("title %s: kept %v, want %v", c.Name, ok, c.Valid)
			continue
		}
		if !ok {
			continue
		}
		var want, have map[string]any
		if err := json.Unmarshal([]byte(c.Output), &want); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(struct {
			Key       string            `json:"key"`
			Kind      string            `json:"kind"`
			Library   string            `json:"library"`
			Audio     []string          `json:"audio,omitempty"`
			Subtitles []string          `json:"subtitles,omitempty"`
			Video     *Picture          `json:"video,omitempty"`
			With      []string          `json:"with,omitempty"`
			IDs       map[string]string `json:"ids,omitempty"`
		}{got.Key, got.Kind, got.Library, got.Audio, got.Subtitles, got.Video, got.With, got.IDs})
		json.Unmarshal(b, &have)
		if !reflect.DeepEqual(have, want) {
			t.Errorf("title %s:\n got %s\nwant %s", c.Name, b, c.Output)
		}
	}
}

func TestStepHeight(t *testing.T) {
	for in, want := range map[int]int{0: 0, -5: 0, 1: 480, 479: 480, 480: 480, 719: 480, 720: 720, 1600: 1440, 2160: 2160, 3000: 2160, 4320: 4320, 9000: 4320} {
		if got := StepHeight(in); got != want {
			t.Errorf("StepHeight(%d) = %d, want %d", in, got, want)
		}
	}
}
