package api_test

import (
	"io"
	"net/http"
	"testing"
	"time"
)

// A file already flowing ends with its playback: stopping one, or a
// takeover, mustn't leave it going on, past the account's limits (rules 45
// and 49).
func TestStreamEndsWithItsPlayback(t *testing.T) {
	srv := playedServer(t, options{maxStreams: 2, fileSize: 64 << 20}).srv
	code, s, body := play(t, srv, `{"titleId":"title_movie_tmdb_949","versionId":"`+heatCopy+`","delivery":"file","device":"Laptop"}`)
	if code != http.StatusCreated {
		t.Fatalf("start: %d %s", code, body)
	}

	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/stream/"+s.Ticket+"/file", nil)
	req.Header.Set("Remote-User", subject)
	res, err := client(srv, subject).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("stream: %d", res.StatusCode)
	}
	if _, err := io.ReadFull(res.Body, make([]byte, 1000)); err != nil {
		t.Fatalf("first part: %v", err)
	}
	ended := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, res.Body)
		ended <- err
	}()
	select {
	case err := <-ended:
		t.Fatalf("ended before the stop: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if code, body := post(t, srv, "/api/v1/playback/"+s.ID+"/stop", `{}`); code >= 300 {
		t.Fatalf("stop: %d %s", code, body)
	}
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Error("the file still flows after its playback stopped")
	}
}
