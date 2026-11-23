package copies

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go/describe"
	"github.com/overfin/server/internal/jellyfin"
)

// headerProbe is a kept probe as a header read has it: one run, each
// stream's record (extradata) in the stream, format.filename the file's
// path, an attachment's data left out.
func headerProbe(t *testing.T, pr *describe.Probe, path string) json.RawMessage {
	t.Helper()
	var tree map[string]any
	d := json.NewDecoder(bytes.NewReader(pr.Outputs.Streams))
	d.UseNumber()
	if err := d.Decode(&tree); err != nil {
		t.Fatal(err)
	}
	data := map[string]map[string]any{}
	for _, b := range pr.Outputs.Data {
		var o struct {
			Streams []map[string]any `json:"streams"`
		}
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		if err := d.Decode(&o); err != nil {
			t.Fatal(err)
		}
		for _, s := range o.Streams {
			data[fmt.Sprint(s["index"])] = s
		}
	}
	for _, e := range tree["streams"].([]any) {
		s := e.(map[string]any)
		if ds, ok := data[fmt.Sprint(s["index"])]; ok {
			s["extradata"], s["extradata_size"] = ds["extradata"], ds["extradata_size"]
		} else if s["codec_type"] != "attachment" {
			s["extradata"] = "\n"
		}
	}
	if f, ok := tree["format"].(map[string]any); ok {
		f["filename"] = path
	}
	b, err := json.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A header read's probe converted is the keeper's own: the same copy.
func TestConvert(t *testing.T) {
	for _, name := range []string{"h264-aac-mp4", "with-font-mkv", "handmade-dv-atmos"} {
		pr, _ := fixture(t, name)
		pr.Frames, pr.Outputs.Frames = nil, nil
		got, err := Convert(pr.MediaType, pr.Size, headerProbe(t, pr, "/media/Films/A/a.mkv"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if want, have := describe.Shape(pr, nil), describe.Shape(got, nil); !reflect.DeepEqual(want, have) {
			t.Errorf("%s: converted\n%+v\nprobed\n%+v", name, have, want)
		}
		if bytes.Contains(got.Outputs.Streams, []byte("/media")) || bytes.Contains(got.Outputs.Streams, []byte("extradata")) {
			t.Errorf("%s: the first run holds the path or data", name)
		}
	}
	if _, err := Convert("video/mp4", 1, []byte(`{"streams":[]} trailing`)); err == nil {
		t.Error("data after the probe accepted")
	}
	if _, err := Convert("video/mp4", 1, []byte(`{"streams":[{"index":0,"codec_type":"video","tags":{"x":"`+
		strings.Repeat("a", describe.DefaultMaxOutput)+`"}}]}`)); err == nil {
		t.Error("a probe past the output cap accepted")
	}
}

// mp4Head is the start of an ISO BMFF file (video/mp4 sniffed).
var mp4Head = []byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2\x00\x00\x00\x08free")

// An import without ffprobe: a record whose file is listed and unchanged
// is kept as the keeper would (its probe without paths, its copy of this
// Version), the HDR one marked for the frame read; a changed file, a
// file gone, one Jellyfin doesn't list, a link out of the libraries, a
// path climbing out, a playlist disguised as a film, a line of ffprobe's
// errors are left out; the keeper's pass after it probes nothing; a
// second import changes nothing.
func TestImport(t *testing.T) {
	ctx := context.Background()
	k, db, media := keeperSetup(t)
	root := filepath.Dir(media) // the read's paths: "media/…"
	outside := filepath.Join(t.TempDir(), "secret.mkv")
	write(t, outside, mp4Head)
	files := map[string][]byte{
		"good.mp4":     append(clone(mp4Head), "good"...),
		"hdr.mp4":      append(clone(mp4Head), "hdr"...),
		"changed.mp4":  append(clone(mp4Head), "changed"...),
		"gone.mp4":     append(clone(mp4Head), "gone"...),
		"playlist.mkv": []byte("#EXTM3U\n#EXTINF:10,\nhttp://example.com/a.ts\n"),
	}
	for name, b := range files {
		write(t, filepath.Join(media, "Films", name), b)
	}
	if err := os.Symlink(outside, filepath.Join(media, "Films", "link.mp4")); err != nil {
		t.Fatal(err)
	}
	var list fakeFiles
	for n, name := range []string{"good.mp4", "hdr.mp4", "changed.mp4", "gone.mp4", "playlist.mkv"} {
		list = append(list, jellyfin.VideoFile{ItemID: id(n), SourceID: id(n), Path: filepath.Join(media, "Films", name),
			Container: "mp4", Parts: 1})
	}
	list = append(list, jellyfin.VideoFile{ItemID: id(4), SourceID: id(5), Path: filepath.Join(media, "Films", "link.mp4"),
		Container: "mp4", Parts: 1})
	k.Files = list.list

	h264, _ := fixture(t, "h264-aac-mp4")
	hdr, _ := fixture(t, "handmade-dv-atmos")
	hdr.Frames, hdr.Outputs.Frames = nil, nil
	var corpus bytes.Buffer
	line := func(rel string, pr *describe.Probe, size int64, mtime int64, stderr string) {
		rec := map[string]any{"path": rel, "size": size, "mtime": mtime,
			"probe": headerProbe(t, pr, filepath.Join(root, rel))}
		if stderr != "" {
			rec["stderr"] = stderr
		}
		b, _ := json.Marshal(rec)
		corpus.Write(append(b, '\n'))
	}
	stat := func(name string) (int64, int64) {
		st, err := os.Stat(filepath.Join(media, "Films", name))
		if err != nil {
			t.Fatal(err)
		}
		return st.Size(), st.ModTime().Unix()
	}
	for _, name := range []string{"good.mp4", "hdr.mp4", "changed.mp4", "gone.mp4", "playlist.mkv", "link.mp4"} {
		size, mod := int64(len(mp4Head)), int64(1)
		if name != "link.mp4" {
			size, mod = stat(name)
		}
		pr := h264
		if name == "hdr.mp4" {
			pr = hdr
		}
		stderr := ""
		if name == "good.mp4" {
			stderr = "[mov,mp4] stream 1, offset 0x30: partial file, SECRET-STDERR"
		}
		line("media/Films/"+name, pr, size, mod, stderr)
	}
	line("media/Films/unlisted.mp4", h264, 3, 1, "")
	line("../etc/passwd", h264, 3, 1, "")
	corpus.WriteString("[matroska,webm @ 0x55] Error opening input\n\n")
	// After the read: one file changed, one gone.
	write(t, filepath.Join(media, "Films", "changed.mp4"), append(clone(mp4Head), "changed again"...))
	os.Remove(filepath.Join(media, "Films", "gone.mp4"))

	im := Import{Keeper: k, Root: root}
	got, err := im.Run(ctx, bytes.NewReader(corpus.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	want := ImportCounts{Records: 9, Imported: 2, Frames: 1, Changed: 1, Gone: 1, Refused: 2, Unlisted: 1, Unknown: 1, Bad: 1}
	if got != want {
		t.Errorf("counts\n%+v, want\n%+v", got, want)
	}
	for n, frames := range map[int]bool{0: false, 1: true} {
		c, ok, _ := db.CopyOf(ctx, id(n), id(n))
		if !ok || c.Minimal || c.ProbeVersion != Version || c.Facts.Video == nil || c.CopyID != CopyID(k.CopyKey, c.Fingerprint) {
			t.Fatalf("%d not imported: %+v", n, c)
		}
		served(t, c.Facts)
		p, ok, _ := db.CopyProbeOf(ctx, c.Fingerprint)
		if !ok || p.ProbeVersion != Version || p.FramesPending != frames {
			t.Errorf("%d's kept probe: %v %+v", n, ok, p.FramesPending)
		}
		for _, bad := range []string{root, "Films", "SECRET-STDERR", "attachment"} {
			if bytes.Contains(p.Raw, []byte(bad)) {
				t.Errorf("%d's kept probe holds %q", n, bad)
			}
		}
	}
	if pending, _ := db.CopyProbesPendingFrames(ctx); len(pending) != 1 {
		t.Errorf("pending frames: %v", pending)
	}

	// The keeper's pass after it: the imported copies stay as they are,
	// nothing imported is probed (this ffprobe would fail), the frame
	// read waits for a prober that makes it.
	k.Prober = &describe.Prober{FFprobe: filepath.Join(t.TempDir(), "no-ffprobe")}
	before, _, _ := db.CopyOf(ctx, id(0), id(0))
	if err := k.pass(ctx); err != nil {
		t.Fatal(err)
	}
	after, _, _ := db.CopyOf(ctx, id(0), id(0))
	if !reflect.DeepEqual(before, after) {
		t.Error("the pass after the import changed an imported copy")
	}
	failed, _ := db.CopyProbesFailedSince(ctx, time.Now().Add(-time.Hour))
	if failed[before.Fingerprint] {
		t.Error("an imported file probed again")
	}
	if c, _, _ := db.CopyOf(ctx, id(2), id(2)); !c.Minimal {
		t.Error("the changed file isn't left to the keeper")
	}

	// Again: nothing changes.
	head := changes(t, db)
	again, err := im.Run(ctx, bytes.NewReader(corpus.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if again.Imported != 0 || again.Already != 2 || changes(t, db) != head {
		t.Errorf("a second import: %+v", again)
	}
}

func clone(b []byte) []byte { return append([]byte(nil), b...) }

// A line past maxLine is dropped alone.
func TestReadLine(t *testing.T) {
	ctx := context.Background()
	k, _, media := keeperSetup(t)
	k.Files = fakeFiles{}.list
	os.MkdirAll(media, 0o700)
	in := strings.Repeat("x", maxLine+10) + "\n" + "not json\n"
	got, err := Import{Keeper: k, Root: media}.Run(ctx, strings.NewReader(in))
	if err != nil || got.Records != 2 || got.Bad != 2 {
		t.Errorf("%+v %v", got, err)
	}
}

// The rehearsal, with ffprobe and the test media (COPIES_MEDIA): a
// made-up header read of every file, made as the real one was (one run
// with data, attachments' data left out, ffprobe's errors beside, fake
// paths in a test library); after it, one file changed and one gone.
// Imported, then the keeper's pass (its frame read on the HDR pictures,
// its probe of what was left out): each copy is the one the keeper's
// own probe makes of the same file.
func TestImportRehearsal(t *testing.T) {
	src := os.Getenv("COPIES_MEDIA")
	if src == "" {
		t.Skip("COPIES_MEDIA not set")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe")
	}
	ctx := context.Background()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		switch filepath.Ext(e.Name()) {
		case ".txt", ".json", ".srt", ".ttf", ".png", "":
		default:
			names = append(names, e.Name())
		}
	}
	k, db, media := keeperSetupN(t, len(names))
	live, liveDB, liveMedia := keeperSetupN(t, len(names))
	var list, liveList fakeFiles
	var corpus bytes.Buffer
	zw := gzip.NewWriter(&corpus)
	paths := map[string]string{}
	for n, name := range names {
		b, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatal(err)
		}
		ext := filepath.Ext(name)
		rel := filepath.Join("Films", "Film "+name, "film"+ext)
		write(t, filepath.Join(media, rel), b)
		write(t, filepath.Join(liveMedia, rel), b)
		path := filepath.Join(media, rel)
		paths[name] = path
		f := jellyfin.VideoFile{ItemID: id(n), SourceID: id(n), Path: path, Container: strings.TrimPrefix(ext, "."), Parts: 1}
		list = append(list, f)
		f.Path = filepath.Join(liveMedia, rel)
		liveList = append(liveList, f)
		cmd := exec.Command(ffprobe, "-v", "error", "-of", "json", "-show_format", "-show_streams", "-show_chapters", "-show_data", path)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		probe := map[string]any{}
		if err == nil && json.Unmarshal(out, &probe) != nil {
			t.Fatalf("%s: ffprobe's output", name)
		}
		for _, s := range asList(probe["streams"]) {
			if s["codec_type"] == "attachment" || s["codec_type"] == "data" {
				delete(s, "extradata")
			}
		}
		st, _ := os.Stat(path)
		rec := map[string]any{"size": st.Size(), "mtime": st.ModTime().Unix(), "path": filepath.Join("media", rel), "probe": probe}
		if stderr.Len() > 0 {
			rec["stderr"] = stderr.String()
		}
		line, _ := json.Marshal(rec)
		zw.Write(append(line, '\n'))
	}
	zw.Write([]byte("[mov,mp4,m4a @ 0x5555] moov atom not found\n"))
	zw.Close()
	// After the read: one file changed, one gone.
	drift := map[string]bool{"mpeg4.avi": true, "tone.wav": true}
	if f, err := os.OpenFile(paths["mpeg4.avi"], os.O_APPEND|os.O_WRONLY, 0); err == nil {
		f.Write([]byte("more"))
		f.Close()
	}
	os.Remove(paths["tone.wav"])
	k.Files, live.Files = list.list, liveList.list

	zr, err := gzip.NewReader(&corpus)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := Import{Keeper: k, Root: filepath.Dir(media)}.Run(ctx, zr)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rehearsal of %d files: %s", len(names), counts)
	if counts.Changed != 1 || counts.Gone != 1 || counts.Bad != 1 || counts.Frames == 0 || counts.Unknown == 0 ||
		counts.Imported+counts.Changed+counts.Gone+counts.Unknown+counts.Bad != counts.Records {
		t.Errorf("counts: %+v", counts)
	}
	live.Prober = &describe.Prober{FFprobe: ffprobe, Frames: true}
	if err := live.pass(ctx); err != nil {
		t.Fatal(err)
	}
	k.Prober = &describe.Prober{FFprobe: ffprobe, Frames: true, Nice: 19}
	if err := k.pass(ctx); err != nil { // the frame read, and the probe of what was left out
		t.Fatal(err)
	}
	if pending, _ := db.CopyProbesPendingFrames(ctx); len(pending) != 0 {
		t.Errorf("frame reads still pending: %d", len(pending))
	}
	for n, name := range names {
		if drift[name] {
			continue
		}
		a, okA, _ := db.CopyOf(ctx, id(n), id(n))
		b, okB, _ := liveDB.CopyOf(ctx, id(n), id(n))
		if !okA || !okB || a.Minimal != b.Minimal {
			t.Errorf("%s: imported %v, probed %v", name, okA && !a.Minimal, okB && !b.Minimal)
			continue
		}
		if !reflect.DeepEqual(a.Facts, b.Facts) || !reflect.DeepEqual(a.Streams, b.Streams) || !reflect.DeepEqual(a.Languages, b.Languages) {
			ja, _ := json.Marshal(a.Facts)
			jb, _ := json.Marshal(b.Facts)
			t.Errorf("%s: imported\n%s\nprobed\n%s", name, ja, jb)
		}
	}
}

func asList(v any) []map[string]any {
	var out []map[string]any
	l, _ := v.([]any)
	for _, e := range l {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
