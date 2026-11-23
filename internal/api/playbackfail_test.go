package api_test

import (
	"database/sql"
	"net/http"
	"testing"
)

// A playback that fails to start leaves nothing registered: nothing to
// restore, nothing counted against the stream limit.
func TestPlaybackStartFailsCleanly(t *testing.T) {
	var path string
	p := playedServer(t, options{maxStreams: 1, dbPath: &path})
	srv := p.srv
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
		`{"titleId":"title_movie_tmdb_949","versionId":"`+heatCopy+`","delivery":"transcode","transcode":{"videoCodec":"h264"},"device":"Laptop"}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("start, the database broken: %d %s", code, body)
	}
	if code, body := getAs(t, srv, subject, "/api/v1/playback/current"); code != http.StatusNotFound {
		t.Errorf("a failed start left a playback behind: %d %s", code, body)
	}
	if n := len(p.maker.orders); n != 0 {
		t.Errorf("a failed start ordered %d deliveries", n)
	}
}
