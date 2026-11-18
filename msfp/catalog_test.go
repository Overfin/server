package msfp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/overfin/server/msfp"
)

// The catalog both ways: libraries offered by public IDs, titles paged,
// read whole then as changes, gone keys of titles sent only, keys
// checked, and what a reader keeps.

func TestLibrariesAndPaging(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.libs = []msfp.Library{{ID: "films", Name: " Films\u200b ", Kind: "movies"}, {ID: "other", Name: "Other", Kind: "weird"}}
	for i := 1; i <= 12_001; i++ {
		h.film("films", fmt.Sprintf("movie/tmdb/%d", i), "en")
	}
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	libs, err := s.b.eng.Wants(ctx, s.onB.ID)
	if err != nil || len(libs) != 2 {
		t.Fatalf("wants: %+v %v", libs, err)
	}
	for _, l := range libs {
		if l.ID == "films" || l.ID == "other" || !l.Wanted {
			t.Errorf("a library as B sees it: %+v (the host's IDs never leave)", l)
		}
		if l.Name == "Films" && l.Kind != "movies" || l.Name == "Other" && l.Kind != "" {
			t.Errorf("a library's kind: %+v", l)
		}
	}
	if got := len(s.peerTitles(t)); got != 12_001 {
		t.Fatalf("B kept %d titles", got)
	}
	pages := s.a.exchanges("titles")
	if len(pages) != 3 {
		t.Fatalf("%d pages", len(pages))
	}
	var first struct {
		Titles []json.RawMessage
		Next   string
		Seq    int64
	}
	json.Unmarshal([]byte(pages[0].resBody), &first)
	if len(first.Titles) != 5000 || first.Next == "" || first.Seq < 1 {
		t.Errorf("the first page: %d titles, next %q, seq %d", len(first.Titles), first.Next, first.Seq)
	}
	if !strings.Contains(pages[2].resBody, `"titles":[`) || strings.Contains(pages[2].resBody, `"next"`) {
		t.Errorf("the last page: %.200s", pages[2].resBody)
	}
	// The limits, with playback on.
	v, _ := s.b.eng.Peer(ctx, s.onB.ID)
	if v.TheirLimits == nil || v.TheirLimits.Streams != 2 || v.TheirLimits.Transcodes != 1 {
		t.Errorf("A's limits as B knows them: %+v", v.TheirLimits)
	}
}

// A whole reading that fails halfway keeps the previous complete list:
// nothing of the pages it read (MSFP catalog section 3.2).
func TestWholeReadingFailsHalfway(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.libs = []msfp.Library{{ID: "films", Name: "Films", Kind: "movies"}, {ID: "more", Name: "More", Kind: "movies"}}
	for i := 1; i <= 6_000; i++ {
		h.film("films", fmt.Sprintf("movie/tmdb/%d", i), "en")
	}
	h.film("more", "movie/tmdb/9999999", "en")
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	before := keysOf(s.peerTitles(t))
	if len(before) != 6_001 {
		t.Fatalf("B kept %d titles", len(before))
	}
	// A's next list, whole since its offers changed: one title less, one
	// more, on its first page; its second page is broken.
	h.remove("movie/tmdb/1")
	h.film("films", "movie/tmdb/10000", "en")
	s.a.eng.CatalogChanged()
	if err := s.a.eng.SetOffers(ctx, s.onA.ID, []string{"films"}); err != nil {
		t.Fatal(err)
	}
	pages := 0
	s.a.setRewrite(func(path string, _ http.Header, body []byte) ([]byte, bool) {
		if !strings.HasSuffix(path, "/titles") {
			return nil, false
		}
		if pages++; pages == 2 {
			return []byte(`{"titles":`), true
		}
		return nil, false
	})
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err == nil {
		t.Fatal("a broken page read")
	}
	if pages != 2 {
		t.Fatalf("%d pages", pages)
	}
	if got := keysOf(s.peerTitles(t)); !slices.Equal(got, before) {
		t.Errorf("B keeps %d titles, not the previous list of %d", len(got), len(before))
	}
}

func TestCatalogChanges(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.libs = []msfp.Library{{ID: "films", Name: "Films", Kind: "movies"}, {ID: "kept", Name: "Not offered", Kind: "movies"}}
	for _, k := range []string{"movie/tmdb/1", "movie/tmdb/2", "movie/tmdb/3"} {
		h.film("films", k, "en")
	}
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	// Offer one library only: the other's titles are never sent.
	if err := s.a.eng.SetOffers(ctx, s.onA.ID, []string{"films"}); err != nil {
		t.Fatal(err)
	}
	h.film("kept", "movie/tmdb/9", "en")
	s.a.eng.CatalogChanged()
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	if got := keysOf(s.peerTitles(t)); !slices.Equal(got, []string{"movie/tmdb/1", "movie/tmdb/2", "movie/tmdb/3"}) {
		t.Fatalf("B keeps %v", got)
	}
	// One added, one removed, one whose ids changed (a change too), and
	// the unoffered one removed: never named in gone.
	h.film("films", "movie/tmdb/4", "en")
	h.remove("movie/tmdb/1")
	h.remove("movie/tmdb/9")
	h.mu.Lock()
	h.titles["movie/tmdb/2"].(*msfp.VideoTitle).IDs = map[string]string{"imdb": "tt0133093"}
	h.mu.Unlock()
	s.a.eng.CatalogChanged()
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	last := s.a.exchanges("titles")
	x := last[len(last)-1]
	if !strings.Contains(x.reqBody, `"since":`) || !strings.Contains(x.resBody, `"changes":true`) {
		t.Fatalf("the second reading: %s → %s", x.reqBody, x.resBody)
	}
	var ans struct {
		Titles []struct{ Key string }
		Gone   []string
	}
	json.Unmarshal([]byte(x.resBody), &ans)
	var sent []string
	for _, ti := range ans.Titles {
		sent = append(sent, ti.Key)
	}
	if !slices.Equal(sent, []string{"movie/tmdb/2", "movie/tmdb/4"}) || !slices.Equal(ans.Gone, []string{"movie/tmdb/1"}) {
		t.Errorf("changes: titles %v, gone %v", sent, ans.Gone)
	}
	got := s.peerTitles(t)
	if !slices.Equal(keysOf(got), []string{"movie/tmdb/2", "movie/tmdb/3", "movie/tmdb/4"}) ||
		got["movie/tmdb/2"].(*msfp.VideoTitle).IDs["imdb"] != "tt0133093" {
		t.Errorf("B keeps %v", got)
	}
	// Offers changed: the next reading is a whole one.
	if err := s.a.eng.SetOffers(ctx, s.onA.ID, []string{"films", "kept"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	last = s.a.exchanges("titles")
	if x := last[len(last)-1]; strings.Contains(x.resBody, `"changes":true`) {
		t.Errorf("a reading after the offers changed: %s", x.resBody)
	}
}

// A host's ChangeLog answers changes; one too old, the whole list.
type loggingHost struct {
	*fakeHost
	seq   int64
	since []int64
	old   bool
}

func (l *loggingHost) Seq(context.Context) (int64, error) { return l.seq, nil }

func (l *loggingHost) Changes(ctx context.Context, libs []string, since int64, after string, limit int) (msfp.ChangePage, error) {
	l.since = append(l.since, since)
	if l.old {
		return msfp.ChangePage{}, msfp.ErrTooOld
	}
	page, _ := l.Titles(ctx, libs, after, limit)
	return msfp.ChangePage{Present: page.Titles, Gone: []string{"movie/tmdb/77"}}, nil
}

func TestHostChangeLog(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.film("films", "movie/tmdb/5", "en")
	l := &loggingHost{fakeHost: h, seq: 6}
	h.libs = []msfp.Library{{ID: "films", Name: "Films", Kind: "movies"}}
	a := newServer(t, msfp.Config{Catalog: l, Playback: h, Media: h})
	b := newServer(t, msfp.Config{})
	onA, onB := pair(t, a, b)
	a.eng.Check(ctx, onA.ID)
	b.eng.Check(ctx, onB.ID)
	a.eng.SetOffers(ctx, onA.ID, []string{"films"})
	libs, _ := b.eng.TheirLibraries(ctx, onB.ID)
	b.eng.SetWants(ctx, onB.ID, []string{libs[0].ID})
	// The offers were set at 6: a reading at 7 is after them.
	l.seq = 7
	if _, err := b.eng.ReadNow(ctx, onB.ID); err != nil {
		t.Fatal(err)
	}
	l.seq = 9
	if _, err := b.eng.ReadNow(ctx, onB.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(l.since, []int64{7}) {
		t.Errorf("changes asked since %v", l.since)
	}
	// movie/tmdb/77 was never sent: not in gone.
	x := a.exchanges("titles")
	if last := x[len(x)-1]; !strings.Contains(last.resBody, `"changes":true`) || strings.Contains(last.resBody, "77") {
		t.Errorf("changes: %s", last.resBody)
	}
	l.old = true
	if _, err := b.eng.ReadNow(ctx, onB.ID); err != nil {
		t.Fatal(err)
	}
	x = a.exchanges("titles")
	if last := x[len(x)-1]; strings.Contains(last.resBody, `"changes"`) || !strings.Contains(last.resBody, "movie/tmdb/5") {
		t.Errorf("a log too old: %s", last.resBody)
	}
}

// bumpingHost is a ChangeLog that takes change numbers itself
// (SeqBumper).
type bumpingHost struct {
	*loggingHost
	mu sync.Mutex
}

func (b *bumpingHost) Seq(context.Context) (int64, error) { return b.now(), nil }

func (b *bumpingHost) BumpSeq(context.Context) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	return b.seq, nil
}

func (b *bumpingHost) now() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// A host whose log takes a number after a change of offers: the reading
// after the next whole one is of changes, with no title changed between.
func TestHostChangeLogBumped(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.film("films", "movie/tmdb/5", "en")
	l := &bumpingHost{loggingHost: &loggingHost{fakeHost: h, seq: 6}}
	h.libs = []msfp.Library{{ID: "films", Name: "Films", Kind: "movies"}}
	a := newServer(t, msfp.Config{Catalog: l, Playback: h, Media: h})
	b := newServer(t, msfp.Config{})
	onA, onB := pair(t, a, b)
	a.eng.Check(ctx, onA.ID)
	b.eng.Check(ctx, onB.ID)
	waitAll(a, b)
	before := l.now()
	a.eng.SetOffers(ctx, onA.ID, []string{"films"})
	after := l.now()
	if after != before+1 {
		t.Fatalf("the log after the offers: %d, was %d", after, before)
	}
	libs, _ := b.eng.TheirLibraries(ctx, onB.ID)
	b.eng.SetWants(ctx, onB.ID, []string{libs[0].ID})
	for range 2 {
		if _, err := b.eng.ReadNow(ctx, onB.ID); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(l.since, []int64{after}) {
		t.Errorf("changes asked since %v, the offers at %d", l.since, after)
	}
}

// Keys MSFP doesn't take never leave: leading zeros, another kind, an
// unknown scheme; members are cleaned before they go.
func TestOwnTitlesChecked(t *testing.T) {
	h := newFakeHost()
	h.film("films", "movie/tmdb/1", "en", "fr")
	for _, k := range []string{"movie/tmdb/0949", "movie/imdb/949", "series/tmdb/1399/s01"} {
		h.titles[k] = &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: k, Kind: "movie", Libraries: []string{"films"}}}
	}
	h.titles["series/tmdb/1399"] = &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: "series/tmdb/1399", Kind: "movie",
		Libraries: []string{"films"}}}
	h.titles["series/tmdb/1399/e1"] = &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: "series/tmdb/1399/e1", Kind: "episode",
		Libraries: []string{"films"}}, Audio: []string{"en", "EN", "x"}, With: []string{"series/tmdb/1399/e3", "series/tmdb/2/e1", "series/tmdb/1399/e2"},
		IDs: map[string]string{"imdb": "tt01", "tvdb": "81189", "other": "1"}}
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	got := s.peerTitles(t)
	if !slices.Equal(keysOf(got), []string{"movie/tmdb/1", "series/tmdb/1399/e1"}) {
		t.Fatalf("B keeps %v", keysOf(got))
	}
	ep := got["series/tmdb/1399/e1"].(*msfp.VideoTitle)
	if !slices.Equal(ep.Audio, []string{"en"}) || !slices.Equal(ep.With, []string{"series/tmdb/1399/e2", "series/tmdb/1399/e3"}) ||
		len(ep.IDs) != 1 || ep.IDs["tvdb"] != "81189" {
		t.Errorf("an episode as sent: %+v", ep)
	}
}

// A hostile peer's titles: what a reader keeps is MSFP's, title by
// title; members of the wrong type at the top fail the reading, which
// keeps what was kept before.
func TestHostileTitles(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.film("films", "movie/tmdb/1", "en")
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	lib := s.libraryPub
	page := `{"titles":[` +
		`{"key":"movie/tmdb/2","kind":"movie","library":"` + lib + `","audio":["fr","fr","toolongtoolongtag"],"ids":{"imdb":"tt0133093","x":1}},` +
		`{"key":"movie/tmdb/03","kind":"movie","library":"` + lib + `"},` +
		`{"key":"movie/tmdb/4","kind":"series","library":"` + lib + `"},` +
		`{"key":"movie/tmdb/5","kind":"movie","library":"elsewhere"},` +
		`{"key":"track/mb/0c000000-0000-4000-8000-000000000003","kind":"track","library":"` + lib + `","name":"<b>x</b>"},` +
		`7,{"key":6}],"seq":3}`
	s.a.setRewrite(func(path string, _ http.Header, body []byte) ([]byte, bool) {
		return []byte(page), strings.HasSuffix(path, "/titles")
	})
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	got := s.peerTitles(t)
	keys := keysOf(got)
	if !slices.Equal(keys, []string{"movie/tmdb/2", "track/mb/0c000000-0000-4000-8000-000000000003"}) {
		t.Fatalf("B keeps %v", keys)
	}
	if v := got["movie/tmdb/2"].(*msfp.VideoTitle); !slices.Equal(v.Audio, []string{"fr"}) || len(v.IDs) != 1 {
		t.Errorf("a title kept: %+v", v)
	}
	if m := got["track/mb/0c000000-0000-4000-8000-000000000003"].(*msfp.MusicTitle); m.Name != "<b>x</b>" {
		t.Errorf("a name is text, kept as text: %q", m.Name)
	}
	// Wrong types at the top: the reading fails, what was kept stays.
	for _, bad := range []string{`{"titles":{}}`, `{"titles":[],"next":7}`, `{"titles":[],"seq":"1"}`, `{"titles":[],"changes":1}`, `{"titles":[],"gone":"x"}`} {
		page = bad
		if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err == nil {
			t.Errorf("%s: read", bad)
		}
		if n := len(s.peerTitles(t)); n != 2 {
			t.Errorf("%s: %d titles kept", bad, n)
		}
	}
	// A page of more than 5,000 keys: a peer filling the database.
	var many []string
	for i := range 5001 {
		many = append(many, fmt.Sprintf(`{"key":"movie/tmdb/%d","kind":"movie","library":"%s"}`, i+10, lib))
	}
	page = `{"titles":[` + strings.Join(many, ",") + `]}`
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err == nil {
		t.Error("a page of 5,001 titles was read")
	}
	// Changes mixed with the whole list on later pages: a failure.
	calls := 0
	s.a.setRewrite(func(path string, _ http.Header, body []byte) ([]byte, bool) {
		if !strings.HasSuffix(path, "/titles") {
			return nil, false
		}
		calls++
		if calls == 1 {
			return []byte(`{"titles":[],"next":"movie/tmdb/1","changes":true,"gone":["movie/tmdb/2"],"seq":5}`), true
		}
		return []byte(`{"titles":[]}`), true
	})
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err == nil {
		t.Error("a reading mixing changes and the whole list was read")
	}
}

func keysOf(m map[string]msfp.Title) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Music titles go with their descriptions, checked before they leave.
func TestMusicTitles(t *testing.T) {
	h := newFakeHost()
	h.libs = []msfp.Library{{ID: "music", Name: "Music", Kind: "music"}}
	track := "track/mb/0c000000-0000-4000-8000-000000000003"
	h.titles[track] = &msfp.MusicTitle{TitleCommon: msfp.TitleCommon{Key: track, Kind: "track", Libraries: []string{"music"}},
		Name: "The\tBoxer", Artists: []string{"artist/mb/0a000000-0000-4000-8000-000000000001", "nope"},
		ArtistNames: "Simon & Garfunkel", Album: "album/mb/0b000000-0000-4000-8000-000000000002", Disc: 1, Number: 2,
		Year: 1970, DurationMs: 308000, IDs: map[string]string{"isrc": "USSM17000123", "imdb": "tt0133093"}}
	nameless := "track/mb/0c000000-0000-4000-8000-000000000004"
	h.titles[nameless] = &msfp.MusicTitle{TitleCommon: msfp.TitleCommon{Key: nameless, Kind: "track", Libraries: []string{"music"}}}
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	got := s.peerTitles(t)
	m, ok := got[track].(*msfp.MusicTitle)
	if !ok || len(got) != 1 {
		t.Fatalf("B keeps %v", keysOf(got))
	}
	if m.Name != "The Boxer" || !slices.Equal(m.Artists, []string{"artist/mb/0a000000-0000-4000-8000-000000000001"}) ||
		m.ArtistNames != "Simon & Garfunkel" || m.Disc != 1 || m.Number != 2 || m.Year != 1970 || m.DurationMs != 308000 ||
		len(m.IDs) != 1 || m.Libraries[0] != s.libraryPub {
		t.Errorf("a track kept: %+v", m)
	}
	// A server without music: none of it is sent.
	h2 := newFakeHost()
	h2.libs = h.libs
	h2.titles[track] = h.titles[track]
	s2 := newSharing(t, h2, msfp.Config{}, msfp.Config{Parts: []string{"catalog", "playback", "media", "video"}})
	if got := s2.peerTitles(t); len(got) != 0 {
		t.Errorf("B without music keeps %v", keysOf(got))
	}
}

// A film's picture (MSFP video section 3.2): the host gives each copy's,
// the best goes, stepped; a reader keeps it with the title, and another
// picture is a change of the title.
func TestTitlePictures(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.film("films", "movie/tmdb/1", "en")
	h.film("films", "movie/tmdb/2", "en")
	h.film("films", "movie/tmdb/3", "en")
	h.mu.Lock()
	h.titles["movie/tmdb/1"].(*msfp.VideoTitle).Pictures = []msfp.Picture{{Height: 1080}, {Height: 2200, HDR: []string{"dv", "HDR10", "hdr10"}}, {Height: 2160}}
	h.titles["movie/tmdb/2"].(*msfp.VideoTitle).Pictures = []msfp.Picture{{Height: 360}, {Height: 0, HDR: []string{"hdr10"}}}
	h.mu.Unlock()
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	got := s.peerTitles(t)
	if v := got["movie/tmdb/1"].(*msfp.VideoTitle).Video; v == nil || v.Height != 2160 || !slices.Equal(v.HDR, []string{"hdr10", "dv"}) {
		t.Errorf("the best copy's picture: %+v", v)
	}
	if v := got["movie/tmdb/2"].(*msfp.VideoTitle).Video; v == nil || v.Height != 480 || v.HDR != nil {
		t.Errorf("below 480, and a copy of no known size: %+v", v)
	}
	if v := got["movie/tmdb/3"].(*msfp.VideoTitle).Video; v != nil {
		t.Errorf("no copy of known size: %+v", v)
	}
	last := s.a.exchanges("titles")
	if body := last[len(last)-1].resBody; !strings.Contains(body, `"video":{"height":2160,"hdr":["hdr10","dv"]}`) || strings.Contains(body, "pictures") {
		t.Errorf("as sent: %s", body)
	}
	// Another picture: the title is sent again in a changes reading.
	h.mu.Lock()
	h.titles["movie/tmdb/3"].(*msfp.VideoTitle).Pictures = []msfp.Picture{{Height: 720}}
	h.mu.Unlock()
	s.a.eng.CatalogChanged()
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	last = s.a.exchanges("titles")
	if x := last[len(last)-1]; !strings.Contains(x.resBody, `"changes":true`) || !strings.Contains(x.resBody, `"key":"movie/tmdb/3"`) ||
		strings.Contains(x.resBody, `"key":"movie/tmdb/1"`) {
		t.Errorf("a changes reading: %s", x.resBody)
	}
	if v := s.peerTitles(t)["movie/tmdb/3"].(*msfp.VideoTitle).Video; v == nil || v.Height != 720 {
		t.Errorf("the new picture kept: %+v", v)
	}
}

func TestBestPicture(t *testing.T) {
	for _, c := range []struct {
		in   []msfp.Picture
		want msfp.Picture
		ok   bool
	}{
		{nil, msfp.Picture{}, false},
		{[]msfp.Picture{{Height: 0}}, msfp.Picture{}, false},
		{[]msfp.Picture{{Height: 1600}}, msfp.Picture{Height: 1440}, true},
		{[]msfp.Picture{{Height: 9000}}, msfp.Picture{Height: 4320}, true},
		{[]msfp.Picture{{Height: 2160}, {Height: 2160, HDR: []string{"hlg"}}}, msfp.Picture{Height: 2160, HDR: []string{"hlg"}}, true},
		{[]msfp.Picture{{Height: 2160, HDR: []string{"hlg"}}, {Height: 2160, HDR: []string{"dv", "hdr10"}}}, msfp.Picture{Height: 2160, HDR: []string{"hdr10", "dv"}}, true},
		{[]msfp.Picture{{Height: 1080, HDR: []string{"hdr10"}}, {Height: 2160}}, msfp.Picture{Height: 2160}, true},
		{[]msfp.Picture{{Height: 1080, HDR: []string{"hdr10+", "sdr"}}}, msfp.Picture{Height: 1080}, true},
	} {
		got, ok := msfp.BestPicture(c.in)
		if ok != c.ok || got.Height != c.want.Height || !slices.Equal(got.HDR, c.want.HDR) {
			t.Errorf("%+v: %+v %v, want %+v %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
