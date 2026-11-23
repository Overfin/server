package copies

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/msfp-io/msfp-go/describe"
	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/files"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/store"
)

// A header read is every file's ffprobe output, read once before the
// copy index existed (a whole library takes days to probe), as JSON
// lines: one record a file,
//
//	{"size": 123, "mtime": 1700000000, "path": "Films/A (2001)/a.mkv",
//	 "probe": {…}, "stderr": "…"}
//
// path relative to the libraries' mount (Import.Root), mtime in seconds,
// probe the JSON of one run of -show_format -show_streams -show_chapters
// -show_data (attachments' and data streams' data left out), stderr
// ffprobe's error text, never kept.
//
// Import turns each record into what the keeper's own probe of the file
// would have kept, and keeps it, so the keeper doesn't probe the file
// again: the probe raw by fingerprint (no path), the copy shaped from
// it, both of this Version. A record is used only if its file is listed
// by Jellyfin, passes the checks every read does (package files), and
// still has the size and time of the read; anything else is left to the
// keeper's own probe. HDR pictures are marked for the frame read, which
// the keeper makes last (Keeper.frames). Running it again skips what it
// imported: it can be stopped and started again.
type Import struct {
	// Keeper is the copy index's keeper: its database, keys, Files,
	// Libraries and Layout. Its Prober isn't used.
	*Keeper
	// Root is the folder the read's paths are relative to (the libraries'
	// mount: /media in the backend's container).
	Root string
}

// ImportCounts say what an import did with the records it read. Never a
// path: counts only.
type ImportCounts struct {
	Records  int `json:"records"`  // lines read
	Imported int `json:"imported"` // probes kept, copies shaped
	Already  int `json:"already"`  // kept before (an earlier run, a twin, the keeper)
	Frames   int `json:"frames"`   // imported, marked for the frame read
	Changed  int `json:"changed"`  // the file's size or time changed since the read
	Gone     int `json:"gone"`     // the file can't be opened now
	Refused  int `json:"refused"`  // outside the libraries' folders, or not a regular file
	Unlisted int `json:"unlisted"` // not a file Jellyfin lists
	Unknown  int `json:"unknown"`  // a media type that isn't probed (sniffed from its first bytes)
	Bad      int `json:"bad"`      // a line that isn't a record, a probe that doesn't parse or is too big
}

func (c ImportCounts) String() string {
	return fmt.Sprintf("records %d, imported %d (frame read to make: %d), already kept %d, changed since %d, gone %d, "+
		"refused %d, not listed by Jellyfin %d, unknown type %d, unreadable %d", c.Records, c.Imported, c.Frames, c.Already,
		c.Changed, c.Gone, c.Refused, c.Unlisted, c.Unknown, c.Bad)
}

// maxLine bounds a record's line: past it, the record is dropped.
const maxLine = 32 << 20

// record is one line of a header read.
type record struct {
	Size  int64           `json:"size"`
	Mtime int64           `json:"mtime"`
	Path  string          `json:"path"`
	Probe json.RawMessage `json:"probe"`
}

// Run imports the header read r (uncompressed), record by record, until
// it ends or ctx does; progress is logged as counts.
func (im Import) Run(ctx context.Context, r io.Reader) (ImportCounts, error) {
	var c ImportCounts
	if !filepath.IsAbs(im.Root) {
		return c, errors.New("copies: the import's root must be an absolute path")
	}
	libs, err := im.Libraries(ctx)
	if err != nil {
		return c, err
	}
	if libs.Empty() {
		return c, errors.New("copies: no library folder: nothing can be read")
	}
	// Every version, by the path Jellyfin gives, in the keeper's order
	// (its position among its item's versions).
	byPath := map[string][]version{}
	positions := map[string]int{}
	err = im.Files(ctx, func(f jellyfin.VideoFile) bool {
		v := version{f: f, position: positions[f.ItemID]}
		positions[f.ItemID]++
		p := filepath.Clean(f.Path)
		byPath[p] = append(byPath[p], v)
		return ctx.Err() == nil
	})
	if err != nil {
		return c, err
	}
	if err := ctx.Err(); err != nil {
		return c, err
	}
	im.Logger.Info("copy import: versions listed", "versions", len(positions))
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return c, err
		}
		line, err := readLine(br)
		if errors.Is(err, io.EOF) {
			break
		}
		if errors.Is(err, errLongLine) {
			c.Records++
			c.Bad++
			continue
		}
		if err != nil {
			return c, err
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		c.Records++
		if err := im.one(ctx, libs, byPath, line, &c); err != nil {
			return c, err
		}
		if c.Records%1000 == 0 {
			im.Logger.Info("copy import: records read", "records", c.Records, "imported", c.Imported, "already", c.Already)
		}
	}
	return c, nil
}

var errLongLine = errors.New("copies: a line too long")

// readLine is the next line without its end; errLongLine (the line
// skipped) past maxLine.
func readLine(br *bufio.Reader) ([]byte, error) {
	var line []byte
	long := false
	for {
		part, err := br.ReadSlice('\n')
		if !long {
			if len(line)+len(part) > maxLine {
				long, line = true, nil
			} else {
				line = append(line, part...)
			}
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && (len(line) > 0 || long):
			err = nil
		case err != nil:
			return nil, err
		}
		if long {
			return nil, errLongLine
		}
		return bytes.TrimRight(line, "\r\n"), nil
	}
}

// one imports a record; only the database's errors stop the import.
func (im Import) one(ctx context.Context, libs files.Libraries, byPath map[string][]version, line []byte, c *ImportCounts) error {
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil || rec.Size < 0 || len(rec.Probe) == 0 {
		c.Bad++
		return nil
	}
	rel := filepath.FromSlash(rec.Path)
	if !filepath.IsLocal(rel) {
		c.Refused++
		return nil
	}
	listed := byPath[filepath.Join(im.Root, rel)]
	if len(listed) == 0 {
		c.Unlisted++
		return nil
	}
	file, err := libs.Open(listed[0].f.Path)
	if err != nil {
		if errors.Is(err, files.ErrOutside) || errors.Is(err, files.ErrNotRegular) {
			c.Refused++
		} else {
			c.Gone++
		}
		return nil
	}
	defer file.Close()
	size, mod := file.Info.Size(), file.Info.ModTime().Unix()
	if size != rec.Size || mod != rec.Mtime {
		c.Changed++
		return nil
	}
	// A file Jellyfin lists twice (an alternate version) is one copy, as
	// the keeper keeps it (distinct).
	copyID := CopyID(im.CopyKey, file.Root, file.Rel)
	for i := range listed {
		listed[i].copyID, listed[i].size, listed[i].mod = copyID, size, mod
	}
	v := distinct(listed)[0]
	now := im.clock()
	v.fingerprint, err = files.Cache{DB: im.DB}.Fingerprint(ctx, v.f.ItemID, v.f.SourceID, file, now)
	if errors.Is(err, files.ErrUnreadable) {
		c.Gone++
		return nil
	} else if err != nil {
		return err
	}
	var layout *wire.PreviewLayout
	if im.Layout != nil {
		layout, _ = im.Layout(v.fingerprint)
	}
	// Kept already (an earlier run, a twin, the keeper's own probe): its
	// copy shaped from it, if it isn't yet.
	if kept, found, err := im.DB.CopyProbeOf(ctx, v.fingerprint); err != nil {
		return err
	} else if found && kept.ProbeVersion >= Version {
		c.Already++
		old, ok, err := im.DB.CopyOf(ctx, v.f.ItemID, v.f.SourceID)
		if err != nil || ok && !old.Minimal && old.Fingerprint == v.fingerprint && old.CopyID == v.copyID {
			return err
		}
		if pr, err := parseKept(kept.Raw); err == nil {
			if err := im.put(ctx, v, pr, layout, kept.ProbeVersion, now); err != nil && !errors.Is(err, store.ErrNoItem) {
				return err
			}
		}
		return nil
	}
	// The media type from the file's first bytes, as a probe sniffs it.
	head := make([]byte, describe.SniffSize)
	n, err := file.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		c.Gone++
		return nil
	}
	mediaType := describe.Sniff(head[:n])
	if describe.Demuxer(mediaType) == "" {
		c.Unknown++
		return nil
	}
	pr, err := Convert(mediaType, size, rec.Probe)
	if err != nil {
		c.Bad++
		return nil
	}
	b, err := keepable(pr, strings.TrimSuffix(file.Real, "/"+file.Rel))
	if err != nil {
		c.Bad++
		return nil
	}
	frames := describe.NeedsFrames(pr)
	if err := im.DB.PutCopyProbe(ctx, store.CopyProbe{Fingerprint: v.fingerprint, ProbeVersion: Version, MediaType: pr.MediaType,
		Size: pr.Size, Raw: b, FramesPending: frames}, now); err != nil {
		return err
	}
	// Not in the catalog's index yet: the keeper shapes it from the kept
	// probe once it is.
	if err := im.put(ctx, v, pr, layout, Version, now); err != nil && !errors.Is(err, store.ErrNoItem) {
		return err
	}
	c.Imported++
	if frames {
		c.Frames++
	}
	return nil
}

// Convert is a header read's probe (one ffprobe run of -show_format
// -show_streams -show_chapters -show_data) as the keeper's own probe
// makes it (describe.Prober.Probe): the first run without data, the
// second the records of the video streams (attached pictures left out),
// and of the audio streams when one is AAC; format.filename dropped;
// the output cap applied to each.
func Convert(mediaType string, size int64, probe []byte) (*describe.Probe, error) {
	var tree map[string]any
	d := json.NewDecoder(bytes.NewReader(probe))
	d.UseNumber()
	if err := d.Decode(&tree); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("copies: data after the probe")
	}
	if f, ok := tree["format"].(map[string]any); ok {
		delete(f, "filename")
	}
	list, _ := tree["streams"].([]any)
	var video, audio []any
	aac := false
	for _, e := range list {
		s, ok := e.(map[string]any)
		if !ok {
			continue
		}
		data := map[string]any{"index": s["index"]}
		for _, k := range []string{"extradata", "extradata_size", "extradata_hash"} {
			if v, ok := s[k]; ok {
				if k != "extradata_hash" {
					data[k] = v
				}
				delete(s, k)
			}
		}
		switch s["codec_type"] {
		case "video":
			if disp, _ := s["disposition"].(map[string]any); fmt.Sprint(disp["attached_pic"]) != "1" {
				video = append(video, data)
			}
		case "audio":
			audio = append(audio, data)
			aac = aac || s["codec_name"] == "aac"
		}
	}
	first, err := json.Marshal(tree)
	if err != nil {
		return nil, err
	}
	out := describe.Outputs{Streams: first}
	for _, run := range []struct {
		streams []any
		want    bool
	}{{video, len(video) > 0}, {audio, aac}} {
		if !run.want {
			continue
		}
		b, err := json.Marshal(map[string]any{"streams": run.streams})
		if err != nil {
			return nil, err
		}
		out.Data = append(out.Data, b)
	}
	for _, b := range append([]json.RawMessage{out.Streams}, out.Data...) {
		if len(b) > describe.DefaultMaxOutput {
			return nil, fmt.Errorf("copies: a probe past %d bytes", describe.DefaultMaxOutput)
		}
	}
	return describe.Parse(mediaType, size, out)
}
