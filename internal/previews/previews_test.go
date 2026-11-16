package previews

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A file's fingerprint follows its content: a copy elsewhere has the same,
// a change anywhere it reads, another; a small file is read whole.
func TestFingerprint(t *testing.T) {
	dir := t.TempDir()
	big := bytes.Repeat([]byte("frame "), 1<<20) // 6 MiB
	writeFile(t, filepath.Join(dir, "a.mkv"), big)
	writeFile(t, filepath.Join(dir, "moved", "b.mkv"), big)
	changed := slices.Clone(big)
	changed[len(changed)-10] = 'X'
	writeFile(t, filepath.Join(dir, "c.mkv"), changed)
	writeFile(t, filepath.Join(dir, "small.mkv"), []byte("tiny"))
	fp := func(name string) string {
		t.Helper()
		f, err := Fingerprint(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !fingerprintRe.MatchString(f) {
			t.Fatalf("%s: %q", name, f)
		}
		return f
	}
	if fp("a.mkv") != fp("moved/b.mkv") {
		t.Error("a copy has another fingerprint")
	}
	if fp("a.mkv") == fp("c.mkv") {
		t.Error("a changed end kept the fingerprint")
	}
	fp("small.mkv")
	if _, err := Fingerprint(dir); err == nil {
		t.Error("a folder fingerprinted")
	}
}

// solid is a small JPEG of one color.
func solid(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, nil)
	return buf.Bytes()
}

// Thumbnails go on whole sheets, in order, the last one too (black where
// it has none); the set says how many and how big.
func TestTile(t *testing.T) {
	frames, out := t.TempDir(), t.TempDir()
	for i := 1; i <= 7; i++ {
		writeFile(t, filepath.Join(frames, strconv.Itoa(100000 + i)[1:]+".jpg"), solid(t, 16, 9, color.Gray{Y: uint8(30 * i)}))
	}
	f := Format{Width: 16, Columns: 2, Rows: 2, IntervalMs: 10_000, Quality: 90}
	set, err := tile(frames, out, f)
	if err != nil {
		t.Fatal(err)
	}
	if set != (Set{Width: 16, Height: 9, Columns: 2, Rows: 2, Count: 7, IntervalMs: 10_000}) || set.Sheets() != 2 {
		t.Fatalf("set: %+v", set)
	}
	for n := range 2 {
		b, err := os.ReadFile(filepath.Join(out, strconv.Itoa(n)+".jpg"))
		if err != nil {
			t.Fatal(err)
		}
		img, err := jpeg.Decode(bytes.NewReader(b))
		if err != nil || img.Bounds().Dx() != 32 || img.Bounds().Dy() != 18 {
			t.Fatalf("sheet %d: %v %v", n, img.Bounds(), err)
		}
	}
}

// The store keeps a set whole, gives its sheets back, lists it and
// removes it; nothing outside a fingerprint's folder is reachable.
func TestStore(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const fp = "1a-0123456789abcdef0123456789abcdef"
	made, _ := s.making()
	writeFile(t, filepath.Join(made, "0.jpg"), []byte("sheet"))
	set := Set{Width: 320, Height: 180, Columns: 10, Rows: 10, Count: 42, IntervalMs: 10_000}
	if err := s.put(fp, set, made); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Get(fp); !ok || got != set {
		t.Fatalf("get: %+v %v", got, ok)
	}
	if b, err := s.Sheet(fp, 0); err != nil || string(b) != "sheet" {
		t.Errorf("sheet 0: %q %v", b, err)
	}
	if _, err := s.Sheet(fp, 1); err == nil {
		t.Error("a sheet past the set")
	}
	if _, err := s.Sheet("../../etc", 0); err == nil {
		t.Error("not a fingerprint, read")
	}
	if all, _ := s.All(); !slices.Equal(all, []string{fp}) {
		t.Errorf("all: %v", all)
	}
	s.Remove(fp)
	if _, ok := s.Get(fp); ok {
		t.Error("kept after removal")
	}
}

// testVideo makes a short video with ffmpeg; the test is skipped without
// it.
func testVideo(t *testing.T, path string, seconds int) string {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	os.MkdirAll(filepath.Dir(path), 0o700)
	cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=10",
		"-t", strconv.Itoa(seconds), "-g", "20", "-c:v", "libx264", "-preset", "ultrafast", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg can't make a test video: %v %s", err, out)
	}
	return ff
}

// ffmpeg makes the thumbnails of a real video, one every interval; a GPU
// that fails falls back on the processor; a file that isn't a video fails.
func TestMake(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "film.mkv")
	ff := testVideo(t, video, 25)
	s, _ := NewStore(filepath.Join(dir, "store"))
	m := Maker{FFmpeg: ff, Hardware: []string{"-hwaccel", "no-such-accel"}, Busy: func() bool { return false }, Every: 50 * time.Millisecond}
	f := Format{Width: 160, Columns: 2, Rows: 2, IntervalMs: 5000, Quality: 80}
	set, err := m.Make(context.Background(), s, "1a-0123456789abcdef0123456789abcdef", video, f)
	if err != nil {
		t.Fatal(err)
	}
	if set.Width != 160 || set.Height != 90 || set.Count < 5 || set.Count > 6 || set.Sheets() != 2 {
		t.Errorf("set: %+v", set)
	}
	writeFile(t, filepath.Join(dir, "text.mkv"), []byte("not a video"))
	if _, err := m.Make(context.Background(), s, "1b-0123456789abcdef0123456789abcdef", filepath.Join(dir, "text.mkv"), f); err == nil {
		t.Error("made previews of a text")
	}
	if left, _ := os.ReadDir(filepath.Join(dir, "store", ".making")); len(left) != 0 {
		t.Errorf("left half made: %v", left)
	}
}

// fakeFiles is the libraries as Jellyfin lists them.
type fakeFiles []jellyfin.VideoFile

func (f fakeFiles) list(_ context.Context, each func(jellyfin.VideoFile) bool) error {
	for _, v := range f {
		if !each(v) {
			return nil
		}
	}
	return nil
}

// A pass: Jellyfin's previews of a file are moved into the store; a file
// without any gets them made (only with previews on), its copy shares
// them; a file that fails waits; previews no file has go after a week.
func TestKeeperPass(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	box, err := seal.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, filepath.Join(dir, "overfin.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	media, jfData := filepath.Join(dir, "media"), filepath.Join(dir, "jellyfin")
	ff := testVideo(t, filepath.Join(media, "new.mkv"), 12)
	data, _ := os.ReadFile(filepath.Join(media, "new.mkv"))
	writeFile(t, filepath.Join(media, "copy", "new.mkv"), data)
	writeFile(t, filepath.Join(media, "old.mkv"), append(slices.Clone(data), 'x')) // another file
	writeFile(t, filepath.Join(media, "broken.mkv"), []byte("not a video"))
	writeFile(t, filepath.Join(media, "sound.mkv"), []byte("sound only"))
	// Jellyfin made the old one's previews.
	const old = "00027710370fdf733c764e3e7401f44e"
	jfSets := filepath.Join(jfData, "data", "trickplay", "00", "00027710-370f-df73-3c76-4e3e7401f44e", "320 - 10x10")
	writeFile(t, filepath.Join(jfSets, "0.jpg"), []byte("jellyfin's"))
	files := fakeFiles{
		{ItemID: old, SourceID: old, Path: filepath.Join(media, "old.mkv"),
			Trickplay: []jellyfin.Trickplay{{Width: 320, Height: 180, Columns: 10, Rows: 10, Count: 2, IntervalMs: 10_000}}},
		{ItemID: "11111111111111111111111111111111", SourceID: "11111111111111111111111111111111", Path: filepath.Join(media, "new.mkv")},
		{ItemID: "22222222222222222222222222222222", SourceID: "22222222222222222222222222222222", Path: filepath.Join(media, "copy", "new.mkv")},
		{ItemID: "33333333333333333333333333333333", SourceID: "33333333333333333333333333333333", Path: filepath.Join(media, "broken.mkv")},
		{ItemID: "44444444444444444444444444444444", SourceID: "44444444444444444444444444444444", Path: filepath.Join(media, "sound.mkv"), NoVideo: true},
	}
	st, _ := NewStore(filepath.Join(dir, "previews"))
	on := false
	now := time.Now()
	k := &Keeper{Store: st, DB: db, Format: Format{Width: 320, Columns: 10, Rows: 10, IntervalMs: 5000, Quality: 80},
		Files: fakeFiles(files).list, JellyfinData: jfData, Every: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		On:    func(context.Context) (bool, error) { return on, nil },
		Maker: Maker{FFmpeg: ff, Busy: func() bool { return false }}, now: func() time.Time { return now }}

	// Off: Jellyfin's are taken over, nothing is made.
	if err := k.pass(ctx); err != nil {
		t.Fatal(err)
	}
	fpOld, set, ok := k.ForVersion(ctx, old, old)
	if !ok || set.Count != 2 {
		t.Fatalf("Jellyfin's not taken over: %+v %v", set, ok)
	}
	if b, _ := st.Sheet(fpOld, 0); string(b) != "jellyfin's" {
		t.Errorf("sheet: %q", b)
	}
	if _, err := os.Stat(jfSets); !os.IsNotExist(err) {
		t.Error("Jellyfin's copy left")
	}
	if _, _, ok := k.ForVersion(ctx, "11111111111111111111111111111111", "11111111111111111111111111111111"); ok {
		t.Error("made while off")
	}

	// On: the new one is made, its copy shares them, the broken one fails.
	on, now = true, now.Add(10*time.Minute)
	if err := k.pass(ctx); err != nil {
		t.Fatal(err)
	}
	fpNew, set, ok := k.ForVersion(ctx, "11111111111111111111111111111111", "11111111111111111111111111111111")
	if !ok || set.Count < 2 {
		t.Fatalf("not made: %+v %v", set, ok)
	}
	if fp2, _, ok := k.ForVersion(ctx, "22222222222222222222222222222222", "22222222222222222222222222222222"); !ok || fp2 != fpNew {
		t.Errorf("the copy: %v %v", fp2, ok)
	}
	// The broken one failed; the one without a picture wasn't even tried.
	if failed, _ := db.PreviewsFailedSince(ctx, now.Add(-time.Hour)); len(failed) != 1 {
		t.Errorf("failed: %v", failed)
	}
	if _, _, ok := k.ForVersion(ctx, "44444444444444444444444444444444", "44444444444444444444444444444444"); ok {
		t.Error("previews of a file without video")
	}

	// The old file gone: its previews stay a week, then go.
	k.Files, now = fakeFiles(files[1:]).list, now.Add(10*time.Minute)
	if err := k.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Get(fpOld); !ok {
		t.Error("gone at once")
	}
	now = now.Add(8 * 24 * time.Hour)
	if err := k.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Get(fpOld); ok {
		t.Error("kept after a week")
	}
	if _, ok := st.Get(fpNew); !ok {
		t.Error("a file's previews deleted")
	}
}
