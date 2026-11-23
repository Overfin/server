package overfin

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/keys"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// -import-probes: a gzipped header read imported through Overfin's
// Jellyfin (its libraries' folders, its files), the counts said, never
// a path; run again, nothing is imported twice.
func TestImportProbes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("JELLYFIN_FFMPEG", "/usr/lib/jellyfin-ffmpeg/ffmpeg")
	t.Setenv("JELLYFIN_DATA_DIR", dir)
	media := filepath.Join(dir, "media")
	film := filepath.Join(media, "Films", "Secret Title (2001)", "secret.mp4")
	if err := os.MkdirAll(filepath.Dir(film), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(film, []byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2 film"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(film)
	item := strings.Repeat("ab", 16)
	jf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Library/VirtualFolders":
			json.NewEncoder(w).Encode([]map[string]any{{"Name": "Films", "Locations": []string{filepath.Join(media, "Films")}}})
		case "/Items":
			json.NewEncoder(w).Encode(map[string]any{"TotalRecordCount": 1, "Items": []map[string]any{{"Id": item,
				"MediaSources": []map[string]any{{"Id": item, "Path": film, "Protocol": "File", "Container": "mp4"}}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer jf.Close()
	box, _ := seal.New(bytes.Repeat([]byte{7}, 32))
	db, err := store.Open(ctx, filepath.Join(dir, "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetJellyfin(ctx, jf.URL, "admin-key", "server-1"); err != nil {
		t.Fatal(err)
	}
	k, _ := keys.Derive(bytes.Repeat([]byte{3}, 32))
	rec, _ := json.Marshal(map[string]any{"size": st.Size(), "mtime": st.ModTime().Unix(), "path": "Films/Secret Title (2001)/secret.mp4",
		"stderr": "secret.mp4: partial file", "probe": map[string]any{
			"format":  map[string]any{"filename": film, "format_name": "mov,mp4,m4a,3gp,3g2,mj2", "duration": "10.0"},
			"streams": []map[string]any{{"index": 0, "codec_type": "video", "codec_name": "h264", "width": 640, "height": 360}}}})
	var read bytes.Buffer
	zw := gzip.NewWriter(&read)
	zw.Write(append(rec, '\n'))
	zw.Close()
	path := filepath.Join(dir, "probe.jsonl.gz")
	os.WriteFile(path, read.Bytes(), 0o600)

	var logs, out bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	if err := importProbes(&config.Config{}, db, k, path, media, &out, logger); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "records 1, imported 1") {
		t.Errorf("said: %s", out.String())
	}
	if err := importProbes(&config.Config{}, db, k, path, media, &out, logger); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "imported 0 (frame read to make: 0), already kept 1") {
		t.Errorf("said again: %s", out.String())
	}
	for _, bad := range []string{"Secret", "secret", media} {
		if strings.Contains(out.String()+logs.String(), bad) {
			t.Errorf("a path said: %s%s", out.String(), logs.String())
		}
	}
	if err := importProbes(&config.Config{}, db, k, filepath.Join(dir, "Secret missing.gz"), media, io.Discard, logger); err == nil ||
		strings.Contains(err.Error(), "Secret") {
		t.Errorf("a missing read: %v", err)
	}
}
