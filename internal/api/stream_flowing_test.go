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
	// Nothing more is read before the stop: the stream waits on this
	// reader (it holds the rest), however fast the machine, so it can't
	// have ended on its own.
	time.Sleep(200 * time.Millisecond)
	if code, body := post(t, srv, "/api/v1/playback/"+s.ID+"/stop", `{}`); code >= 300 {
		t.Fatalf("stop: %d %s", code, body)
	}
	ended := make(chan int64, 1)
	go func() {
		n, _ := io.Copy(io.Discard, res.Body)
		ended <- n
	}()
	select {
	case n := <-ended:
		// What was on its way when it stopped, at most: never the rest.
		if rest := res.ContentLength - 1000; n >= rest {
			t.Errorf("the whole file came after the stop (%d bytes)", n)
		}
	case <-time.After(5 * time.Second):
		t.Error("the file still flows after its playback stopped")
	}
}
