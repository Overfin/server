package wire

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The cases of vectors/v1/music.json: keys, and what a caller keeps of
// each title.
func TestMusicVectors(t *testing.T) {
	var v struct {
		Libraries []string
		Keys      []struct {
			Name, Text, Kind string
			Valid            bool
		}
		Titles []struct {
			Name, Text, Output string
			Valid              bool
		}
	}
	readVectors(t, "music.json", &v)
	if len(v.Keys) == 0 || len(v.Titles) == 0 {
		t.Fatal("no music cases")
	}
	for _, c := range v.Keys {
		k, ok := ParseMusicKey(c.Text)
		if ok != c.Valid || ok && (k.Kind != c.Kind || k.String() != c.Text) {
			t.Errorf("key %s: %+v %v, want %v %s", c.Name, k, ok, c.Valid, c.Kind)
		}
		if tk, ok := ParseKey(c.Text); ok && c.Valid && (tk.Part != PartMusic || tk.Playable != (c.Kind == "track")) {
			t.Errorf("key %s: ParseKey %+v", c.Name, tk)
		}
	}
	for _, c := range v.Titles {
		got, ok := ReadMusicTitle(json.RawMessage(c.Text))
		if ok && !slices.Contains(v.Libraries, got.Library) {
			ok = false // the caller's check: a library it asked for
		}
		if ok != c.Valid {
			t.Errorf("title %s: kept %v, want %v", c.Name, ok, c.Valid)
			continue
		}
		if !ok {
			continue
		}
		var want map[string]any
		if err := json.Unmarshal([]byte(c.Output), &want); err != nil {
			t.Fatal(err)
		}
		if have := musicMap(got); !reflect.DeepEqual(have, want) {
			t.Errorf("title %s:\n got %v\nwant %v", c.Name, have, want)
		}
	}
}

// musicMap is a kept title as the vectors write it.
func musicMap(m MusicTitle) map[string]any {
	out := map[string]any{"key": m.Key, "kind": m.Kind, "library": m.Library, "name": m.Name}
	if len(m.Artists) > 0 {
		var a []any
		for _, s := range m.Artists {
			a = append(a, s)
		}
		out["artists"] = a
	}
	set := func(name string, v any, on bool) {
		if on {
			out[name] = v
		}
	}
	set("artistNames", m.ArtistNames, m.ArtistNames != "")
	set("album", m.Album, m.Album != "")
	set("disc", float64(m.Disc), m.Disc != 0)
	set("number", float64(m.Number), m.Number != 0)
	set("year", float64(m.Year), m.Year != 0)
	set("durationMs", float64(m.DurationMs), m.HasDuration)
	if len(m.IDs) > 0 {
		ids := map[string]any{}
		for k, v := range m.IDs {
			ids[k] = v
		}
		out["ids"] = ids
	}
	return out
}

func TestWebVTTPart(t *testing.T) {
	track := "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nfirst\n\n00:01:55.000 --> 00:02:05.000\nacross\n\n" +
		"00:02:30.000 --> 00:02:31.000\n<b>second</b>\n\n00:05:00.000 --> 00:05:01.000\nthird\n"
	for _, c := range []struct {
		n    int
		want []string
	}{{0, []string{"first", "across"}}, {1, []string{"across", "second"}}, {2, []string{"third"}}, {3, nil}} {
		out, ok := WebVTTPart([]byte(track), c.n)
		if !ok {
			t.Fatalf("part %d refused", c.n)
		}
		again, ok := RebuildWebVTT(out)
		if !ok || string(again) != string(out) {
			t.Errorf("part %d isn't in its rebuilt form: %q", c.n, out)
		}
		for _, w := range c.want {
			if !strings.Contains(string(out), "\n"+w+"\n") {
				t.Errorf("part %d: no %q in %q", c.n, w, out)
			}
		}
		if got := strings.Count(string(out), "-->"); got != len(c.want) {
			t.Errorf("part %d: %d cues in %q", c.n, got, out)
		}
	}
	if _, ok := WebVTTPart([]byte("not vtt"), 0); ok {
		t.Error("a track that isn't WebVTT gave a part")
	}
}
