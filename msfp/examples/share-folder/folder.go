package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/overfin/server/msfp"
	"github.com/overfin/server/msfp/wire"
)

// folder is a folder of films, shared: the engine's Catalog, Playback and
// Media. A film is a file named with its TMDB ID, "Title (Year)
// [tmdbid-949].mp4"; beside it, optionally, "<same name>.hls/" holding an
// HLS copy made beforehand (master.m3u8 and what it names, as ffmpeg
// writes them), and "<same name>.<language>.vtt" subtitles. Files are
// served as they are: direct when the viewer plays their container, the
// HLS copy otherwise. Nothing is transcoded.
type folder struct{ dir string }

// film is what the folder holds of one title.
type film struct {
	file, hls string
	subs      []string // .vtt files, s<n> by position
}

var tmdbID = regexp.MustCompile(`\[tmdbid-([1-9][0-9]{0,9})\]\.(mp4|m4v|mkv|webm|mov|ts)$`)

// scan reads the folder: its films by title key.
func (f folder) scan() map[string]film {
	names, _ := os.ReadDir(f.dir)
	out := map[string]film{}
	for _, n := range names {
		m := tmdbID.FindStringSubmatch(n.Name())
		if m == nil {
			continue
		}
		base := strings.TrimSuffix(n.Name(), "."+m[2])
		fl := film{file: filepath.Join(f.dir, n.Name())}
		if _, err := os.Stat(filepath.Join(f.dir, base+".hls", "master.m3u8")); err == nil {
			fl.hls = filepath.Join(f.dir, base+".hls")
		}
		for _, s := range names {
			if strings.HasPrefix(s.Name(), base+".") && strings.HasSuffix(s.Name(), ".vtt") {
				fl.subs = append(fl.subs, filepath.Join(f.dir, s.Name()))
			}
		}
		out["movie/tmdb/"+m[1]] = fl
	}
	return out
}

func (f folder) Libraries(context.Context) ([]msfp.Library, error) {
	return []msfp.Library{{ID: "films", Name: filepath.Base(f.dir), Kind: "movies"}}, nil
}

func (f folder) Titles(_ context.Context, _ []string, after string, limit int) (msfp.TitlePage, error) {
	films := f.scan()
	var page msfp.TitlePage
	for _, key := range slices.Sorted(maps.Keys(films)) {
		if key <= after {
			continue
		}
		if len(page.Titles) == limit {
			page.More = true
			break
		}
		t := &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: key, Kind: "movie", Libraries: []string{"films"}}}
		for _, s := range films[key].subs {
			t.Subtitles = append(t.Subtitles, language(s))
		}
		page.Titles = append(page.Titles, t)
	}
	return page, nil
}

// language is a subtitles file's language, from its name.
func language(path string) string {
	return strings.TrimPrefix(filepath.Ext(strings.TrimSuffix(path, ".vtt")), ".")
}

func (f folder) Copies(_ context.Context, key string, _ []string) ([]msfp.Copy, error) {
	if _, ok := f.scan()[key]; !ok {
		return nil, nil
	}
	return []msfp.Copy{{Handle: key, Name: "File", Library: "films"}}, nil
}

func (f folder) Describe(_ context.Context, key string) (msfp.CopyInfo, error) {
	info := msfp.CopyInfo{AudioTracks: []msfp.Track{{ID: "a0", Label: "Original", Default: true}}}
	for i, s := range f.scan()[key].subs {
		info.SubtitleTracks = append(info.SubtitleTracks, msfp.Track{ID: "s" + strconv.Itoa(i), Label: language(s), Language: language(s)})
	}
	return info, nil
}

// Start serves the file when the viewer plays its container, the HLS
// copy otherwise; the handle is the protocol and the key.
func (f folder) Start(_ context.Context, req msfp.StartRequest) (msfp.Started, error) {
	fl := f.scan()[req.Copy]
	ext := strings.TrimPrefix(filepath.Ext(fl.file), ".")
	method, protocol := "direct", "file"
	if !slices.Contains(req.Capabilities.Containers, ext) {
		if fl.hls == "" {
			return msfp.Started{}, msfp.ErrNotPlayable
		}
		method, protocol = "remux", "hls"
	}
	if _, err := req.Admit(method); err != nil {
		return msfp.Started{}, err
	}
	return msfp.Started{Handle: protocol + " " + req.Copy, Method: method, Protocol: protocol,
		MimeType: "video/" + strings.Replace(ext, "mkv", "x-matroska", 1), PositionMs: req.StartMs}, nil
}

func (folder) Progress(context.Context, string, int64, bool) error { return nil }
func (folder) Stop(context.Context, string, msfp.EndReason) error  { return nil }

func (f folder) Serve(_ context.Context, handle string, p msfp.MediaPath, rng *msfp.Range, out msfp.ResponseSink) error {
	_, key, _ := strings.Cut(handle, " ")
	fl, ok := f.scan()[key]
	if !ok {
		return msfp.ErrNotFound
	}
	switch p.Kind {
	case msfp.MediaFile:
		return serveFile(fl.file, rng, out)
	case msfp.MediaMaster:
		return serveFile(filepath.Join(fl.hls, "master.m3u8"), nil, out)
	case msfp.MediaPlaylist, msfp.MediaSegment:
		return serveFile(filepath.Join(fl.hls, p.Name), rng, out)
	case msfp.MediaSubtitles:
		n, _ := strconv.Atoi(strings.TrimPrefix(p.Track, "s"))
		if n >= len(fl.subs) {
			return msfp.ErrNotFound
		}
		vtt, err := os.ReadFile(fl.subs[n])
		part, ok := wire.WebVTTPart(vtt, p.Part)
		if err != nil || !ok {
			return msfp.ErrNotFound
		}
		out.Start(http.StatusOK, "text/vtt; charset=utf-8", int64(len(part)), "")
		_, err = out.Write(part)
		return err
	}
	return msfp.ErrNotFound
}

// serveFile answers a file, or the range of it asked.
func serveFile(path string, rng *msfp.Range, out msfp.ResponseSink) error {
	file, err := os.Open(path)
	if err != nil {
		return msfp.ErrNotFound
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		return err
	}
	size, first, last := st.Size(), int64(0), st.Size()-1
	types := map[string]string{".m3u8": "application/vnd.apple.mpegurl", ".ts": "video/mp2t", ".m4s": "video/iso.segment"}
	status, contentType, contentRange := http.StatusOK, types[filepath.Ext(path)], ""
	if contentType == "" {
		contentType = "video/" + strings.TrimPrefix(filepath.Ext(path), ".")
	}
	if rng != nil {
		first, last = rng.First, min(rng.Last, size-1)
		if rng.Suffix > 0 {
			first, last = max(size-rng.Suffix, 0), size-1
		} else if rng.Last < 0 {
			last = size - 1
		}
		if first >= size {
			return msfp.ErrRangeNotSatisfiable
		}
		status, contentRange = http.StatusPartialContent, fmt.Sprintf("bytes %d-%d/%d", first, last, size)
	}
	out.Start(status, contentType, last-first+1, contentRange)
	_, err = io.Copy(out, io.NewSectionReader(file, first, last-first+1))
	return err
}
