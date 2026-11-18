package wire

import "testing"

func TestTrackID(t *testing.T) {
	for id, want := range map[string]int{"a0": 0, "a1": 1, "a10": 10, "a999": 999} {
		if n, ok := TrackID(id, 'a'); !ok || n != want {
			t.Errorf("%s: %d %v", id, n, ok)
		}
	}
	for _, id := range []string{"", "a", "a1000", "a+1", "s1", "A1", "a-1", "a1 ", "a01", "a00", "a007"} {
		if _, ok := TrackID(id, 'a'); ok {
			t.Errorf("%q accepted", id)
		}
	}
}

// A play answer's stream URL is its stream root's, under one ticket, with
// the name its protocol gives (media section 1).
func TestStreamBase(t *testing.T) {
	root := StreamRoot("https://b.example.org")
	if root != "https://b.example.org/api/v1/stream/" {
		t.Fatal(root)
	}
	if base, ok := StreamBase(root+"T1/master.m3u8", root, "hls"); !ok || base != root+"T1/" {
		t.Errorf("%q %v", base, ok)
	}
	if _, ok := StreamBase(root+"T1/file", root, "file"); !ok {
		t.Error("file refused")
	}
	for _, c := range []struct{ url, protocol string }{
		{root + "T1/file", "hls"}, {root + "T1/master.m3u8", "file"}, {root + "T1/v0.m3u8", "hls"},
		{root + "T1/master.m3u8", "dash"}, {root + "T1/master.m3u8?x", "hls"}, {root + "T1/master.m3u8#x", "hls"},
		{root + "T%31/file", "file"}, {root + "/file", "file"}, {root + "T1//file", "file"}, {root + "../T1/file", "file"},
		{root + "T1/x/file", "file"}, {"https://c.example.org/api/v1/stream/T1/file", "file"},
		{"https://b.example.org/api/v1/stream//T1/file", "file"}, {root + "T1", "file"},
	} {
		if _, ok := StreamBase(c.url, root, c.protocol); ok {
			t.Errorf("%s (%s) accepted", c.url, c.protocol)
		}
	}
}

func TestTrickplayBounds(t *testing.T) {
	ok := Trickplay{TileWidth: 320, TileHeight: 180, Columns: 10, Rows: 10, IntervalMs: 10_000, TileCount: 250, Sheets: 3}
	if !ok.InBounds() || ok.SheetCount() != 3 {
		t.Fatal("refused", ok.SheetCount())
	}
	for _, bad := range []func(*Trickplay){
		func(t *Trickplay) { t.Sheets = 2 },
		func(t *Trickplay) { t.TileWidth = 0 },
		func(t *Trickplay) { t.TileWidth = 410 }, // 4,100 pixels across
		func(t *Trickplay) { t.Rows = 101 },
		func(t *Trickplay) { t.IntervalMs = 99 },
		func(t *Trickplay) { t.IntervalMs = 3_600_001 },
		func(t *Trickplay) { t.TileCount = 0; t.Sheets = 0 },
		func(t *Trickplay) { t.Columns, t.Rows, t.TileCount, t.Sheets = 1, 1, 1001, 1001 },
	} {
		tp := ok
		bad(&tp)
		if tp.InBounds() {
			t.Errorf("%+v accepted", tp)
		}
	}
}

func TestRelayedMediaType(t *testing.T) {
	for in, want := range map[string]string{
		"video/mp4": "video/mp4", "Video/MP2T; charset=x": "video/mp2t", "audio/mp4+x.y-z": "audio/mp4+x.y-z",
		"audio/mpegurl": "application/octet-stream", "application/vnd.apple.mpegurl": "application/octet-stream",
		"text/html": "application/octet-stream", "video/": "application/octet-stream", "video/a_b": "application/octet-stream",
		"video/a b": "application/octet-stream", "": "application/octet-stream", "video/mp4/x": "application/octet-stream",
	} {
		if got := RelayedMediaType(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
