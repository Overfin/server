package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

// videoLibrary answers /Items as Jellyfin does, total items paged, each
// with the media sources of source(i); it records the queries.
func videoLibrary(t *testing.T, total int, sources func(i int) []map[string]any) (*Client, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		asked = append(asked, q.Get("startIndex")+" "+q.Get("ids"))
		mu.Unlock()
		start, _ := strconv.Atoi(q.Get("startIndex"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		var items []map[string]any
		for i := start; i < total && i < start+limit; i++ {
			if ids := q.Get("ids"); ids != "" && ids != fmt.Sprintf("%032x", i+1) {
				continue
			}
			it := map[string]any{"Id": fmt.Sprintf("%032x", i+1), "MediaSources": sources(i)}
			if i == 2 {
				it["PartCount"] = 2
				it["Trickplay"] = map[string]any{
					fmt.Sprintf("%032x", 0x300): map[string]any{
						"320":  map[string]int{"Width": 320, "Height": 180, "TileWidth": 10, "TileHeight": 10, "ThumbnailCount": 50, "Interval": 10000},
						"9999": map[string]int{"Width": 9999, "Height": 180, "TileWidth": 10, "TileHeight": 10, "ThumbnailCount": 50, "Interval": 10000},
					},
					fmt.Sprintf("%032x", 0x301): map[string]any{ // another source's
						"320": map[string]int{"Width": 320, "Height": 180, "TileWidth": 10, "TileHeight": 10, "ThumbnailCount": 50, "Interval": 10000},
					},
				}
			}
			items = append(items, it)
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": total})
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	return c, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := asked
		asked = nil
		return out
	}
}

func TestVideoFiles(t *testing.T) {
	ctx := context.Background()
	src := func(id int, extra map[string]any) map[string]any {
		m := map[string]any{"Id": fmt.Sprintf("%032x", id), "Path": fmt.Sprintf("/media/%d.mkv", id), "Protocol": "File", "Size": 100,
			"MediaStreams": []map[string]any{{"Type": "Video", "Index": 0}}}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	c, asked := videoLibrary(t, 1201, func(i int) []map[string]any {
		switch i {
		case 0: // remote, a stream over HTTP, no path: none listed; one real
			return []map[string]any{src(0x100, map[string]any{"IsRemote": true}), src(0x101, map[string]any{"Protocol": "Http"}),
				src(0x102, map[string]any{"Path": ""}), src(0x103, nil)}
		case 1: // sound only; streams not read yet
			return []map[string]any{src(0x200, map[string]any{"MediaStreams": []map[string]any{{"Type": "Audio"}}}),
				src(0x201, map[string]any{"MediaStreams": []map[string]any{}})}
		case 2:
			return []map[string]any{src(0x300, nil)}
		}
		return []map[string]any{src(0x1000+i, nil)}
	})
	var all []VideoFile
	if err := c.VideoFiles(ctx, Auth{Token: "admin"}, func(f VideoFile) bool { all = append(all, f); return true }); err != nil {
		t.Fatal(err)
	}
	if got := asked(); len(got) != 3 || got[0] != "0 " || got[2] != "1000 " {
		t.Errorf("pages: %q", got)
	}
	if len(all) != 1+2+1+1198 {
		t.Fatalf("files: %d", len(all))
	}
	if all[0].SourceID != fmt.Sprintf("%032x", 0x103) {
		t.Errorf("the one real file: %+v", all[0])
	}
	if !all[1].NoVideo || all[2].NoVideo {
		t.Errorf("sound only %v, streams not read %v", all[1].NoVideo, all[2].NoVideo)
	}
	if f := all[3]; f.Parts != 2 || len(f.Trickplay) != 1 || f.Trickplay[0] != (Trickplay{Width: 320, Height: 180, Columns: 10, Rows: 10, Count: 50, IntervalMs: 10000}) {
		t.Errorf("a stacked film with previews: %+v", f)
	}
	// Stopping early: no more pages.
	n := 0
	c.VideoFiles(ctx, Auth{Token: "admin"}, func(VideoFile) bool { n++; return n < 2 })
	if got := asked(); n != 2 || len(got) != 1 {
		t.Errorf("stopped: %d files, %d pages", n, len(got))
	}

	// One version's file: asked by its item; another source, a 404.
	f, err := c.VideoFileOf(ctx, Auth{Token: "admin"}, fmt.Sprintf("%032X", 3), fmt.Sprintf("%032x", 0x300))
	if err != nil || f.Path != "/media/768.mkv" {
		t.Errorf("one file: %+v %v", f, err)
	}
	if got := asked(); len(got) != 1 || got[0] != "0 "+fmt.Sprintf("%032x", 3) {
		t.Errorf("asked: %q", got)
	}
	if _, err := c.VideoFileOf(ctx, Auth{Token: "admin"}, fmt.Sprintf("%032x", 3), fmt.Sprintf("%032x", 0x301)); !IsStatus(err, 404) {
		t.Errorf("a source it hasn't: %v", err)
	}
	asked()
	if _, err := c.VideoFileOf(ctx, Auth{Token: "admin"}, "../x", fmt.Sprintf("%032x", 1)); err == nil || len(asked()) != 0 {
		t.Errorf("a malformed item: %v", err)
	}
}
