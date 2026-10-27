package api_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"testing"
)

// A playback that fails to start leaves nothing registered: nothing to
// restore, nothing counted against the stream limit.
func TestPlaybackStartFailsCleanly(t *testing.T) {
	var path string
	srv, fake := newServerWith(t, options{maxStreams: 1, dbPath: &path})
	// The database breaks where a playback's title is looked up.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`ALTER TABLE catalog_items RENAME COLUMN title_key TO broken`); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, srv, "/api/v1/playback",
		`{"itemId":"item_`+fmt.Sprintf("%032x", 1)+`","capabilities":{},"device":"Laptop"}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("start, the database broken: %d %s", code, body)
	}
	if code, body := getAs(t, srv, subject, "/api/v1/playback/current"); code != http.StatusNotFound {
		t.Errorf("a failed start left a playback behind: %d %s", code, body)
	}
	if fake.called("POST /Sessions/Playing") != 0 {
		t.Error("a failed start was reported to Jellyfin")
	}
}
