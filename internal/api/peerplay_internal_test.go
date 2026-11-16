package api

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A playback started on a paired server right after one stopped there
// waits for that stop (at most a few seconds): the other server would
// count both against the pairing's limit.
func TestPendingStops(t *testing.T) {
	p := pendingStops{byPeer: map[string]int{}}
	done := p.begin("peer_a")
	go func() { time.Sleep(200 * time.Millisecond); done(); done() }() // twice: counted once
	start := time.Now()
	p.wait(context.Background(), "peer_a", 3*time.Second)
	if waited := time.Since(start); waited < 150*time.Millisecond || waited > time.Second {
		t.Errorf("waited %v", waited)
	}
	start = time.Now()
	p.wait(context.Background(), "peer_b", 3*time.Second) // nothing on its way there
	if time.Since(start) > 50*time.Millisecond {
		t.Error("waited with nothing to wait for")
	}
	stuck := p.begin("peer_c")
	defer stuck()
	start = time.Now()
	p.wait(context.Background(), "peer_c", 300*time.Millisecond)
	if waited := time.Since(start); waited < 250*time.Millisecond || waited > time.Second {
		t.Errorf("a stop that never ends: waited %v", waited)
	}
}

// A paired server's previews: its sheets' URLs must be its stream's, one
// for each sheet in order, or the answer is refused; a layout outside
// MSFP's bounds is dropped, the playback going on without it.
func TestPeerPreviewsChecked(t *testing.T) {
	const address = "https://b.example.org"
	base := address + "/api/v1/stream/tkt/"
	good := func() sessionJSON {
		tp := &trickplayJSON{TileWidth: 320, TileHeight: 180, Columns: 10, Rows: 10, IntervalMs: 10000, TileCount: 150,
			Sheets: []string{base + "trickplay/0", base + "trickplay/1"}}
		return sessionJSON{ID: "play_" + strings.Repeat("a", 26), Method: "direct",
			Stream: streamJSON{URL: base + "file", Protocol: "file"}, Trickplay: tp}
	}
	out := good()
	if _, ok := keepPeerURLs(&out, address); !ok || !cleanPeerText(&out) || out.Trickplay == nil {
		t.Fatalf("good previews: %+v", out.Trickplay)
	}
	for name, sheets := range map[string][]string{
		"another server": {base + "trickplay/0", "https://evil.example.org/api/v1/stream/tkt/trickplay/1"},
		"another ticket": {base + "trickplay/0", address + "/api/v1/stream/other/trickplay/1"},
		"out of order":   {base + "trickplay/1", base + "trickplay/0"},
		"another file":   {base + "trickplay/0", base + "file"},
		"a query":        {base + "trickplay/0", base + "trickplay/1?x=1"},
		"relative":       {"trickplay/0", "trickplay/1"},
	} {
		out := good()
		out.Trickplay.Sheets = sheets
		if _, ok := keepPeerURLs(&out, address); ok {
			t.Errorf("%s: kept", name)
		}
	}
	for name, change := range map[string]func(*trickplayJSON){
		"no tile width":     func(tp *trickplayJSON) { tp.TileWidth = 0 },
		"a huge tile":       func(tp *trickplayJSON) { tp.TileHeight = 2049 },
		"no columns":        func(tp *trickplayJSON) { tp.Columns = 0 },
		"101 rows":          func(tp *trickplayJSON) { tp.Rows, tp.TileHeight = 101, 1 },
		"a wide sheet":      func(tp *trickplayJSON) { tp.TileWidth = 410 },
		"a tall sheet":      func(tp *trickplayJSON) { tp.TileHeight = 410 },
		"a short interval":  func(tp *trickplayJSON) { tp.IntervalMs = 99 },
		"a long interval":   func(tp *trickplayJSON) { tp.IntervalMs = 3_600_001 },
		"no tiles":          func(tp *trickplayJSON) { tp.TileCount = 0 },
		"too few sheets":    func(tp *trickplayJSON) { tp.TileCount = 201 },
		"too many sheets":   func(tp *trickplayJSON) { tp.TileCount = 100 },
		"over 1,000 sheets": func(tp *trickplayJSON) { tp.TileCount = 100_001 },
		"huge numbers":      func(tp *trickplayJSON) { tp.TileWidth, tp.Columns = 1<<40, 1<<40 },
	} {
		out := good()
		change(out.Trickplay)
		if _, ok := keepPeerURLs(&out, address); !ok {
			t.Fatalf("%s: refused for its URLs", name)
		}
		if !cleanPeerText(&out) || out.Trickplay != nil {
			t.Errorf("%s: kept", name)
		}
	}
}
