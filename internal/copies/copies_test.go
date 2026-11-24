package copies

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go/describe"
	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/files"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

func fixture(t *testing.T, name string) (*describe.Probe, []byte) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	pr, err := parseKept(b)
	if err != nil {
		t.Fatal(err)
	}
	return pr, b
}

// served is a copy as it would be served: its ID and etag set, read back
// by msfp-go's own reader, which refuses anything out of form.
func served(t *testing.T, c wire.Copy) wire.Copy {
	t.Helper()
	c.ID, c.ETag = "v1", "e1"
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := wire.ReadCopy(b)
	if err != nil {
		t.Fatalf("not a copy MSFP reads: %v\n%s", err, b)
	}
	return got
}

// Jellyfin's view of handmade-dv-atmos: a subtitle file beside it first
// (index 0), the file's streams after it, the cover art as an image.
func atmosFile() jellyfin.VideoFile {
	return jellyfin.VideoFile{ItemID: "item", SourceID: "item", Container: "mkv", Parts: 1, Streams: []jellyfin.MediaStream{
		{Index: 0, Type: "Subtitle", Codec: "srt", Language: "spa", IsExternal: true, IsTextSubtitleStream: true},
		{Index: 1, Type: "Video", Codec: "hevc", Width: 3840, Height: 2160},
		{Index: 2, Type: "Audio", Codec: "eac3", Language: "eng"},
		{Index: 3, Type: "Audio", Codec: "truehd", Language: "eng"},
		{Index: 4, Type: "Audio", Codec: "dts", Language: "fre"},
		{Index: 5, Type: "Audio", Codec: "aac", Language: "eng"},
		{Index: 6, Type: "Subtitle", Codec: "PGSSUB", Language: "fre"},
		{Index: 7, Type: "Subtitle", Codec: "subrip", Language: "eng"},
		{Index: 9, Type: "EmbeddedImage", Codec: "mjpeg"},
	}}
}

// Shaping: describe's copy with the subtitle file beside it numbered
// above the file's streams; every track mapped to Jellyfin's index by
// kind and order; the languages as the catalog keeps them, regions from
// the tracks' tags, forced subtitles aside; a copy MSFP reads.
func TestShape(t *testing.T) {
	pr, _ := fixture(t, "handmade-dv-atmos")
	c, streams, l := Shape(pr, atmosFile())
	c = served(t, c)
	if c.Video == nil || c.Video.Codec != "hevc" || c.Video.DV == nil || len(c.AudioTracks) != 4 {
		t.Fatalf("copy: %+v", c)
	}
	last := c.SubtitleTracks[len(c.SubtitleTracks)-1]
	if !last.External || last.ID != "s9" || last.Language != "es" || last.Format != "subrip" {
		t.Errorf("the subtitle file: %+v", last)
	}
	want := map[string]int{"v": 1, "a1": 2, "a2": 3, "a3": 4, "a4": 5, "s5": 6, "s6": 7, "s9": 0}
	if !mapsEqual(streams, want) {
		t.Errorf("map: %v, want %v", streams, want)
	}
	if !slices.Equal(l.AudioLanguages, []string{"en", "fr"}) || !slices.Contains(l.Audio, "en") ||
		!slices.Contains(l.Subtitles, "en") || !slices.Contains(l.Subtitles, "es") || slices.Contains(l.Subtitles, "fr") {
		t.Errorf("languages: %+v", l)
	}
	// Jellyfin's count of a kind differs (it hasn't read the file again):
	// that kind isn't mapped, the others are.
	f := atmosFile()
	f.Streams = slices.DeleteFunc(f.Streams, func(s jellyfin.MediaStream) bool { return s.Index == 5 })
	_, streams, _ = Shape(pr, f)
	if _, ok := streams["a1"]; ok || streams["v"] != 1 || streams["s6"] != 7 {
		t.Errorf("a kind that differs: %v", streams)
	}
	// A stacked film: its parts, from Jellyfin.
	f = atmosFile()
	f.Parts = 2
	if c, _, _ := Shape(pr, f); served(t, c).Parts != 2 {
		t.Errorf("parts: %d", c.Parts)
	}
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// A minimal copy, from Jellyfin's data alone: size, duration, media type
// from the container, the picture's codec and size, audio tracks with
// their languages and file indexes; no codecs strings, so it never plays
// as it is.
func TestMinimal(t *testing.T) {
	f := atmosFile()
	f.Size, f.RunTimeTicks = 1000, 81_600_430_000
	c, streams := Minimal(f)
	c = served(t, c)
	if c.MediaType != "video/matroska" || c.Size != 1000 || c.DurationMs != 8_160_043 || c.Video == nil ||
		c.Video.Codecs != "" || c.Video.Height != 2160 || len(c.AudioTracks) != 4 {
		t.Fatalf("minimal: %+v", c)
	}
	a := c.AudioTracks[2]
	if a.ID != "a3" || a.Language != "fr" || a.Codecs != "" || streams["a3"] != 4 || streams["v"] != 1 {
		t.Errorf("audio track: %+v, map %v", a, streams)
	}
	if wire.RequiresCodecs(c.Video.Codec) && c.Video.Codecs == "" {
		// what makes a viewer's server never play it as it is
	} else {
		t.Error("a minimal copy could play as it is")
	}
	// Nothing read yet: still a copy, of the container's type.
	c, _ = Minimal(jellyfin.VideoFile{Container: "mp4", Size: 1})
	if c = served(t, c); c.MediaType != "video/mp4" || len(c.AudioTracks) != 0 {
		t.Errorf("nothing known: %+v", c)
	}
}

// IDs: the copy's and the etag from its fingerprint, each keyed, never
// the same; an episode's after the first, from the file's.
func TestIDs(t *testing.T) {
	k1, k2 := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	fp := "1-" + strings.Repeat("a", 32)
	id := CopyID(k1, fp)
	if !regexp.MustCompile(`^v[a-z2-7]{26}$`).MatchString(id) || !wire.ID(id) {
		t.Errorf("copy ID %q", id)
	}
	if id != CopyID(k1, fp) || id == CopyID(k1, "1-"+strings.Repeat("b", 32)) || id == CopyID(k2, fp) {
		t.Error("copy IDs: not stable, or not by fingerprint and key")
	}
	e := ETag(k1, "1-"+strings.Repeat("a", 32))
	if len(e) != 22 || !wire.ID(e) || e == ETag(k1, "1-"+strings.Repeat("b", 32)) || e == ETag(k2, "1-"+strings.Repeat("a", 32)) {
		t.Errorf("etag %q", e)
	}
	ep := EpisodeCopyID(k1, id, "series/tmdb/1/s1/e2")
	if !wire.ID(ep) || ep == id || ep == EpisodeCopyID(k1, id, "series/tmdb/1/s1/e3") {
		t.Errorf("episode copy ID %q", ep)
	}
}

// A kept probe has no file name, no attachment, no string naming the
// library's folder, and is read again whole.
func TestKeepable(t *testing.T) {
	pr, _ := fixture(t, "with-font-mkv")
	var tree map[string]any
	json.Unmarshal(pr.Outputs.Streams, &tree)
	tree["format"].(map[string]any)["tags"] = map[string]any{"comment": "/media/films/A/a.mkv", "title": "A"}
	pr.Outputs.Streams, _ = json.Marshal(tree)
	b, err := keepable(pr, "/media/films")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"/media/films", "filename", "attachment", "font.ttf", "/dev/fd"} {
		if bytes.Contains(b, []byte(bad)) {
			t.Errorf("kept probe holds %q", bad)
		}
	}
	if !bytes.Contains(b, []byte(`"title":"A"`)) {
		t.Error("the title tag not kept (the identifier needs it)")
	}
	again, err := parseKept(b)
	if err != nil {
		t.Fatal(err)
	}
	if got := describe.Shape(again, nil); got.Video == nil || len(got.SubtitleTracks) != 1 {
		t.Errorf("read again: %+v", got)
	}
}

type fakeFiles []jellyfin.VideoFile

func (f fakeFiles) list(_ context.Context, each func(jellyfin.VideoFile) bool) error {
	for _, v := range f {
		if !each(v) {
			break
		}
	}
	return nil
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func id(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }

// keeperSetup is a database with films 0 to 4 indexed, a library folder
// with their files, and a keeper over them.
func keeperSetup(t *testing.T) (*Keeper, *store.Store, string) {
	t.Helper()
	return keeperSetupN(t, 5)
}

// keeperSetupN is keeperSetup with films 0 to n-1.
func keeperSetupN(t *testing.T, films int) (*Keeper, *store.Store, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	db, err := store.Open(ctx, filepath.Join(dir, "overfin.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var items []store.CatalogItem
	for n := range films {
		items = append(items, store.CatalogItem{ID: id(n), Kind: "movie", Name: "F", SortName: "f", TMDB: string(rune('1' + n)),
			AudioTags: []string{"de"}})
	}
	if err := db.PutCatalog(ctx, items, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(dir, "media")
	k := &Keeper{DB: db, CopyKey: bytes.Repeat([]byte{1}, 32), EtagKey: bytes.Repeat([]byte{2}, 32), Every: time.Hour,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Libraries: func(context.Context) (files.Libraries, error) { return files.NewLibraries([]string{media}), nil }}
	return k, db, media
}

// copyIDOf is the copy ID of the file at path, from its fingerprint.
func copyIDOf(t *testing.T, k *Keeper, path string) string {
	t.Helper()
	f, err := files.NewLibraries([]string{filepath.Dir(path)}).Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fp, err := files.Fingerprint(f)
	if err != nil {
		t.Fatal(err)
	}
	return CopyID(k.CopyKey, fp)
}

// Two libraries holding a file at the same path in each (Films and Films
// 4K), not the same bytes: two copies, neither dropped.
func TestSamePathTwoLibraries(t *testing.T) {
	ctx := context.Background()
	k, db, media := keeperSetup(t)
	hd, uhd := filepath.Join(media, "films"), filepath.Join(media, "films-4k")
	write(t, filepath.Join(hd, "A (2001)", "a.mkv"), []byte("film a"))
	write(t, filepath.Join(uhd, "A (2001)", "a.mkv"), []byte("film a in 4k"))
	k.Libraries = func(context.Context) (files.Libraries, error) { return files.NewLibraries([]string{hd, uhd}), nil }
	k.Files = fakeFiles{
		{ItemID: id(0), SourceID: id(0), Path: filepath.Join(hd, "A (2001)", "a.mkv"), Container: "mkv", Parts: 1},
		{ItemID: id(1), SourceID: id(1), Path: filepath.Join(uhd, "A (2001)", "a.mkv"), Container: "mkv", Parts: 1},
	}.list
	if err := k.pass(ctx); err != nil {
		t.Fatal(err)
	}
	a, okA, _ := db.CopyOf(ctx, id(0), id(0))
	b, okB, _ := db.CopyOf(ctx, id(1), id(1))
	if !okA || !okB || a.CopyID == b.CopyID {
		t.Errorf("two libraries, same path: %v %v %q %q", okA, okB, a.CopyID, b.CopyID)
	}
}

// The same file in two libraries, and twice in the second (a hardlink):
// one copy ID, a row for each place, ranked by path; players get it once,
// through the first library in the libraries' order they may see, then
// the first path; a profile allowed one library only gets it through
// that library's place, and a paired server through a library offered
// to it, its handle the bare copy ID.
func TestSameFileTwoLibraries(t *testing.T) {
	ctx := context.Background()
	k, db, media := keeperSetup(t)
	hd, uhd := filepath.Join(media, "films"), filepath.Join(media, "more")
	write(t, filepath.Join(hd, "A (2001)", "a.mkv"), []byte("the same film"))
	write(t, filepath.Join(uhd, "B", "a.mkv"), []byte("the same film"))
	if err := os.MkdirAll(filepath.Join(uhd, "A"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(uhd, "B", "a.mkv"), filepath.Join(uhd, "A", "a.mkv")); err != nil {
		t.Fatal(err)
	}
	// The items in their libraries: lib-more comes first in the order.
	items := []store.CatalogItem{
		{ID: id(0), Kind: "movie", Name: "F", SortName: "f", TMDB: "1", LibraryID: "lib-films"},
		{ID: id(1), Kind: "movie", Name: "F", SortName: "f", TMDB: "1", LibraryID: "lib-more"},
		{ID: id(2), Kind: "movie", Name: "F", SortName: "f", TMDB: "1", LibraryID: "lib-more"},
	}
	if err := db.PutCatalog(ctx, items, 2); err != nil {
		t.Fatal(err)
	}
	if err := db.SetLibraries(ctx, []store.Library{{ID: "lib-more", Item: []byte("{}")}, {ID: "lib-films", Item: []byte("{}")}}); err != nil {
		t.Fatal(err)
	}
	k.Libraries = func(context.Context) (files.Libraries, error) { return files.NewLibraries([]string{hd, uhd}), nil }
	k.Files = fakeFiles{
		{ItemID: id(0), SourceID: id(0), Path: filepath.Join(hd, "A (2001)", "a.mkv"), Container: "mkv", Parts: 1},
		{ItemID: id(1), SourceID: id(1), Path: filepath.Join(uhd, "B", "a.mkv"), Container: "mkv", Parts: 1},
		{ItemID: id(2), SourceID: id(2), Path: filepath.Join(uhd, "A", "a.mkv"), Container: "mkv", Parts: 1},
	}.list
	if err := k.pass(ctx); err != nil {
		t.Fatal(err)
	}
	want := copyIDOf(t, k, filepath.Join(hd, "A (2001)", "a.mkv"))
	places := map[string]int{}
	for n := range 3 {
		c, ok, _ := db.CopyOf(ctx, id(n), id(n))
		if !ok || c.CopyID != want {
			t.Fatalf("place %d: %+v %v, want ID %s", n, c, ok, want)
		}
		places[c.ItemID] = c.Place
	}
	// By real path: films/A (2001), more/A, more/B.
	if places[id(0)] != 0 || places[id(2)] != 1 || places[id(1)] != 2 {
		t.Errorf("places %v", places)
	}
	all := []string{id(0), id(1), id(2)}
	cs, err := db.ItemsCopies(ctx, all)
	if err != nil || len(cs) != 1 || cs[0].ItemID != id(2) || cs[0].Library != "lib-more" {
		t.Fatalf("one copy, through lib-more's first path: %+v %v", cs, err)
	}
	for lib, item := range map[string]string{"lib-films": id(0), "lib-more": id(2)} {
		allowed, _, err := db.AllowedItems(ctx, store.Access{Libraries: []string{lib}}, all)
		if err != nil {
			t.Fatal(err)
		}
		mine := slices.DeleteFunc(slices.Clone(all), func(i string) bool { return !allowed[i] })
		cs, err := db.ItemsCopies(ctx, mine)
		if err != nil || len(cs) != 1 || cs[0].ItemID != item || cs[0].CopyID != want {
			t.Errorf("allowed %s only: %+v %v", lib, cs, err)
		}
		c, ok, err := db.CopyByHandle(ctx, want, []string{lib})
		if err != nil || !ok || c.ItemID != item {
			t.Errorf("handle through %s: %+v %v %v", lib, c, ok, err)
		}
	}
	// A paired server offered both libraries: the place the catalog lists
	// it by (lib-more first, then its first path); offered neither, none.
	if c, ok, err := db.CopyByHandle(ctx, want, []string{"lib-films", "lib-more"}); err != nil || !ok || c.ItemID != id(2) {
		t.Errorf("handle through both: %+v %v %v", c, ok, err)
	}
	if _, ok, err := db.CopyByHandle(ctx, want, []string{"lib-other"}); err != nil || ok {
		t.Errorf("handle through a library not holding it: %v %v", ok, err)
	}
	// The same pass again: nothing changes.
	head := changes(t, db)
	if err := k.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if changes(t, db) != head {
		t.Error("a pass with nothing new told changes")
	}
}

func changes(t *testing.T, db *store.Store) int64 {
	t.Helper()
	head, _, err := db.TitleChanges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return head
}

// A pass without ffprobe: every readable version of the libraries gets a
// minimal copy; a link out of the libraries gets none; a file listed
// twice (an alternate version) is one copy, under its own item; a file
// whose kept probe is of this Version is shaped from it, its languages
// the title's; the same pass again changes nothing; a subtitle file
// added shapes it again, unprobed; a file gone loses its copy.
func TestKeeperPass(t *testing.T) {
	ctx := context.Background()
	k, db, media := keeperSetup(t)
	outside := filepath.Join(filepath.Dir(media), "secret.mkv")
	write(t, outside, []byte("secret"))
	write(t, filepath.Join(media, "a.mkv"), []byte("film a"))
	write(t, filepath.Join(media, "b.mkv"), []byte("film b"))
	write(t, filepath.Join(media, "alt.mkv"), []byte("second version"))
	if err := os.Symlink(outside, filepath.Join(media, "link.mkv")); err != nil {
		t.Fatal(err)
	}
	atmos := atmosFile()
	list := fakeFiles{
		{ItemID: id(0), SourceID: id(0), Path: filepath.Join(media, "a.mkv"), Container: "mkv", Size: 6, Parts: 1},
		{ItemID: id(0), SourceID: id(2), Path: filepath.Join(media, "alt.mkv"), Container: "mp4", Parts: 1},
		{ItemID: id(1), SourceID: id(1), Path: filepath.Join(media, "b.mkv"), Container: "mkv", Streams: atmos.Streams, Parts: 1},
		{ItemID: id(2), SourceID: id(2), Path: filepath.Join(media, "alt.mkv"), Container: "mp4", Parts: 1},
		{ItemID: id(3), SourceID: id(3), Path: filepath.Join(media, "link.mkv"), Container: "mkv", Parts: 1},
	}
	k.Files = list.list

	// b's file was read before (an import, a twin): its probe is kept.
	libs, _ := k.Libraries(ctx)
	bf, err := libs.Open(filepath.Join(media, "b.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	fp, err := files.Cache{DB: db}.Fingerprint(ctx, id(1), id(1), bf, time.Now())
	bf.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, raw := fixture(t, "handmade-dv-atmos")
	if err := db.PutCopyProbe(ctx, store.CopyProbe{Fingerprint: fp, ProbeVersion: Version, MediaType: "video/mp4", Size: 5, Raw: raw},
		time.Now()); err != nil {
		t.Fatal(err)
	}

	if err := k.pass(ctx); err != nil {
		t.Fatal(err)
	}
	a, ok, _ := db.CopyOf(ctx, id(0), id(0))
	if !ok || !a.Minimal || a.CopyID != copyIDOf(t, k, filepath.Join(media, "a.mkv")) || a.ETag == "" || a.Facts.MediaType != "video/matroska" {
		t.Errorf("a's minimal copy: %+v %v", a, ok)
	}
	if _, ok, _ := db.CopyOf(ctx, id(3), id(3)); ok {
		t.Error("a link out of the libraries got a copy")
	}
	if _, ok, _ := db.CopyOf(ctx, id(0), id(2)); ok {
		t.Error("an alternate version kept under its film too")
	}
	if alt, ok, _ := db.CopyOf(ctx, id(2), id(2)); !ok || alt.CopyID != copyIDOf(t, k, filepath.Join(media, "alt.mkv")) {
		t.Error("the alternate version's own copy missing")
	}
	b, ok, _ := db.CopyOf(ctx, id(1), id(1))
	if !ok || b.Minimal || b.ProbeVersion != Version || b.Facts.Video == nil || b.Streams["s9"] != 0 || b.Languages == nil {
		t.Fatalf("b shaped from its kept probe: %+v", b)
	}
	if cs, _ := db.ItemCopies(ctx, id(1)); len(cs) != 1 {
		t.Errorf("b's copies: %d", len(cs))
	}

	// The same pass again: nothing changes.
	head := changes(t, db)
	if err := k.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if changes(t, db) != head {
		t.Error("a pass with nothing new told changes")
	}

	// A second subtitle file beside b: shaped again, not probed.
	list[2].Streams = append(slices.Clone(list[2].Streams), jellyfin.MediaStream{Index: 10, Type: "Subtitle", Codec: "ass",
		Language: "ita", IsExternal: true})
	if err := k.pass(ctx); err != nil {
		t.Fatal(err)
	}
	b2, _, _ := db.CopyOf(ctx, id(1), id(1))
	if len(b2.Facts.SubtitleTracks) != len(b.Facts.SubtitleTracks)+1 || b2.Streams["s10"] != 10 || b2.Externals == b.Externals {
		t.Errorf("a subtitle file added: %+v %v", b2.Facts.SubtitleTracks, b2.Streams)
	}
	if changes(t, db) == head {
		t.Error("a subtitle file added isn't a change")
	}

	// a renamed and moved to another folder: the same copy ID, no change
	// of its title.
	head = changes(t, db)
	moved := filepath.Join(media, "elsewhere", "renamed.mkv")
	os.MkdirAll(filepath.Dir(moved), 0o700)
	os.Rename(filepath.Join(media, "a.mkv"), moved)
	list[0].Path = moved
	if err := k.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if a2, ok, _ := db.CopyOf(ctx, id(0), id(0)); !ok || a2.CopyID != a.CopyID {
		t.Errorf("renamed: %s, was %s", a2.CopyID, a.CopyID)
	}
	if changes(t, db) != head {
		t.Error("a rename told changes")
	}

	// Its bytes replaced (another size): another copy ID, the old one gone.
	write(t, moved, []byte("film a, cut again"))
	if err := k.pass(ctx); err != nil {
		t.Fatal(err)
	}
	a3, _, _ := db.CopyOf(ctx, id(0), id(0))
	if a3.CopyID == a.CopyID || a3.CopyID != copyIDOf(t, k, moved) || a3.ETag == a.ETag {
		t.Errorf("replaced: %s, was %s", a3.CopyID, a.CopyID)
	}
	if cs, _ := db.ItemCopies(ctx, id(0)); len(cs) != 1 {
		t.Errorf("replaced: %d copies", len(cs))
	}
	if changes(t, db) == head {
		t.Error("replaced bytes aren't a change")
	}

	// The alternate version's file replaced by a link out of the
	// libraries: never read, its copy goes.
	os.Remove(filepath.Join(media, "alt.mkv"))
	if err := os.Symlink(outside, filepath.Join(media, "alt.mkv")); err != nil {
		t.Fatal(err)
	}
	if err := k.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.CopyOf(ctx, id(2), id(2)); ok {
		t.Error("a file now a link out of the libraries kept its copy")
	}

	// a gone from the libraries: its copy goes.
	k.Files = fakeFiles(list[1:]).list
	if err := k.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.CopyOf(ctx, id(0), id(0)); ok {
		t.Error("a gone, its copy kept")
	}
}

// What needs a probe: a new file, a changed one, one probed by an older
// Version (shaped from its old probe meanwhile); not one that failed
// lately, nor one read by this Version.
func TestRefresh(t *testing.T) {
	ctx := context.Background()
	k, db, media := keeperSetup(t)
	k.Prober = &describe.Prober{} // never run here
	write(t, filepath.Join(media, "a.mkv"), []byte("film a"))
	v := version{f: jellyfin.VideoFile{ItemID: id(0), SourceID: id(0), Container: "mkv", Parts: 1}, fingerprint: "6-" + strings.Repeat("a", 32),
		copyID: CopyID(k.CopyKey, "6-"+strings.Repeat("a", 32)), size: 6, mod: 1}
	need := func(failed map[string]bool) bool {
		t.Helper()
		n, err := k.refresh(ctx, v, failed)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if !need(nil) {
		t.Error("a new file not probed")
	}
	if c, _, _ := db.CopyOf(ctx, id(0), id(0)); !c.Minimal {
		t.Error("no minimal copy meanwhile")
	}
	if need(map[string]bool{v.fingerprint: true}) {
		t.Error("a file that failed lately probed again")
	}
	_, raw := fixture(t, "h264-aac-mp4")
	db.PutCopyProbe(ctx, store.CopyProbe{Fingerprint: v.fingerprint, ProbeVersion: Version - 1, MediaType: "video/mp4", Size: 6, Raw: raw}, time.Now())
	if !need(nil) {
		t.Error("a file probed by an older Version not probed again")
	}
	if c, _, _ := db.CopyOf(ctx, id(0), id(0)); c.Minimal || c.Facts.Video == nil {
		t.Error("not shaped from its older probe meanwhile")
	}
	db.PutCopyProbe(ctx, store.CopyProbe{Fingerprint: v.fingerprint, ProbeVersion: Version, MediaType: "video/mp4", Size: 6, Raw: raw}, time.Now())
	if need(nil) {
		t.Error("a file with a probe of this Version probed again")
	}
	if c, _, _ := db.CopyOf(ctx, id(0), id(0)); c.ProbeVersion != Version {
		t.Errorf("probe version %d", c.ProbeVersion)
	}
	if need(nil) {
		t.Error("a file read by this Version probed again")
	}
	v.fingerprint = "6-" + strings.Repeat("b", 32) // the file changed
	if !need(nil) {
		t.Error("a changed file not probed")
	}
	if c, _, _ := db.CopyOf(ctx, id(0), id(0)); !c.Minimal || c.Fingerprint != v.fingerprint {
		t.Error("a changed file's copy kept its old facts")
	}
}

// With ffprobe and the test media (COPIES_MEDIA, a folder of
// msfp-tests/describe's files): files are probed, the new ones first,
// their probes kept without paths; a file ffprobe can't read waits.
func TestKeeperProbes(t *testing.T) {
	src := os.Getenv("COPIES_MEDIA")
	if src == "" {
		t.Skip("COPIES_MEDIA not set")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe")
	}
	ctx := context.Background()
	k, db, media := keeperSetup(t)
	k.Prober = &describe.Prober{FFprobe: ffprobe, Frames: true}
	for n, name := range []string{"hevc-hdr10.mkv", "h264-aac.mp4", "playlist.mkv"} {
		b, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Skip(err)
		}
		write(t, filepath.Join(media, "Films", name), b)
		_ = n
	}
	k.Files = fakeFiles{
		{ItemID: id(0), SourceID: id(0), Path: filepath.Join(media, "Films", "hevc-hdr10.mkv"), Container: "mkv", Parts: 1},
		{ItemID: id(1), SourceID: id(1), Path: filepath.Join(media, "Films", "h264-aac.mp4"), Container: "mp4", Parts: 1},
		{ItemID: id(2), SourceID: id(2), Path: filepath.Join(media, "Films", "playlist.mkv"), Container: "mkv", Parts: 1},
	}.list
	if err := k.pass(ctx); err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{id(0), id(1)} {
		c, ok, _ := db.CopyOf(ctx, item, item)
		if !ok || c.Minimal || c.ProbeVersion != Version || c.ProbedAt.IsZero() {
			t.Errorf("%s not probed: %+v", item, c)
		}
		served(t, c.Facts)
		p, ok, _ := db.CopyProbeOf(ctx, c.Fingerprint)
		if !ok || bytes.Contains(p.Raw, []byte(media)) || bytes.Contains(p.Raw, []byte("Films")) {
			t.Errorf("%s's kept probe: %v, or it names its path", item, ok)
		}
	}
	if c, _, _ := db.CopyOf(ctx, id(2), id(2)); !c.Minimal {
		t.Error("a playlist disguised as a film was probed")
	}
	if failed, _ := db.CopyProbesFailedSince(ctx, time.Now().Add(-time.Hour)); len(failed) != 1 {
		t.Errorf("failed: %v", failed)
	}
}
