package msfp

import (
	"errors"
	"maps"
	"testing"
)

// A hello's versions (core section 10.2) and what two servers agree on
// (section 10.3).
func TestVersions(t *testing.T) {
	ours := Versions{Core: []int{1}, Parts: map[string][]int{PartCatalog: {1}, PartPlayback: {1}, PartMedia: {1}, PartVideo: {1}}}
	read := func(body string) Versions {
		t.Helper()
		h, err := ReadHello([]byte(body), false)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		return h.Versions
	}
	cases := []struct {
		body  string
		core  bool
		parts map[string]int
	}{
		// Without core and parts: all of version 0, nothing in common.
		{`{"state":"paired"}`, false, map[string]int{}},
		{`{"state":"paired","core":[1],"parts":{"catalog":[1],"playback":[1],"media":[1],"video":[1],"music":[1]}}`, true,
			map[string]int{PartCatalog: 1, PartPlayback: 1, PartMedia: 1, PartVideo: 1}},
		// media off: playback and video with it.
		{`{"state":"paired","core":[0,1],"parts":{"catalog":[1],"playback":[1],"video":[1]}}`, true,
			map[string]int{PartCatalog: 1}},
		// Entries past 8, and those that aren't non-negative integers, ignored.
		{`{"state":"paired","core":[2,3,4,5,6,7,8,9,1],"parts":{}}`, false, map[string]int{}},
		{`{"state":"paired","core":[-1,"1",1,1],"parts":{"catalog":[1],"Media":[1],"x_y":"x"}}`, true,
			map[string]int{PartCatalog: 1}},
		// An empty list: that part not spoken.
		{`{"state":"paired","core":[1],"parts":{"catalog":[],"playback":[1],"media":[1],"video":[1]}}`, true,
			map[string]int{PartMedia: 1}},
	}
	for _, c := range cases {
		_, parts, ok := Agreed(ours, read(c.body))
		if ok != c.core || !maps.Equal(parts, c.parts) {
			t.Errorf("%s: core %v, parts %v; want %v, %v", c.body, ok, parts, c.core, c.parts)
		}
	}
	// The wrong types: a bad body, in a request; in an answer as well, but
	// certificates, which count as none there.
	for _, body := range []string{`{}`, `{"state":1}`, `{"state":"paired","core":1}`, `{"state":"paired","parts":[]}`,
		`{"state":"paired","parts":{"catalog":1}}`, `{"state":"paired","parts":{"video":{"x":1}}}`,
		`{"state":"paired","certificates":"x"}`, `{"state":"paired","address":1}`,
		`{"state":"paired","inbound":"no"}`, `{"state":"paired","nextCommitment":true}`} {
		if _, err := ReadHello([]byte(body), false); !errors.Is(err, ErrJSON) {
			t.Errorf("%s: %v", body, err)
		}
	}
	// A part this server doesn't know: ignored, whatever its value, a
	// list or not (section 10.2), so a later version may change its shape.
	for _, body := range []string{`{"state":"paired","core":[1],"parts":{"catalog":[1],"games":{"v":[1]}}}`,
		`{"state":"paired","core":[1],"parts":{"catalog":[1],"games":"x","x_y":7,"Catalog":{}}}`} {
		h, err := ReadHello([]byte(body), false)
		if err != nil {
			t.Errorf("%s: %v", body, err)
			continue
		}
		if _, kept := h.Versions.Parts["games"]; kept || len(h.Versions.Parts) != 1 {
			t.Errorf("%s: parts %v", body, h.Versions.Parts)
		}
	}
	if h, err := ReadHello([]byte(`{"state":"paired","certificates":"x"}`), true); err != nil || h.Certificates != nil {
		t.Errorf("an answer's certificates that aren't a list: %+v, %v", h, err)
	}
}

// In an answer, null inside a list of versions is an entry to ignore, and
// at the top, a bad answer.
func TestVersionsNullInAnswer(t *testing.T) {
	h, err := ReadHello([]byte(`{"state":"paired","core":[null,1],"parts":{"catalog":[null]}}`), true)
	if err != nil || len(h.Versions.Core) != 1 || h.Versions.Core[0] != 1 || len(h.Versions.Parts["catalog"]) != 0 {
		t.Errorf("%+v, %v", h, err)
	}
	if _, err := ReadHello([]byte(`{"state":"paired","core":null}`), true); err == nil {
		t.Error("core null at the top of an answer accepted")
	}
	if _, err := ReadHello([]byte(`{"state":"paired","core":[1,null]}`), false); err == nil {
		t.Error("null in a request accepted")
	}
}

// knownKey and rotation in a request (core section 9.2): type-checked
// only from a server that can't be called, which this package doesn't
// read yet; from any other, ignored whatever their value, null included.
// The main table's members stay type-checked.
func TestHelloOneWayMembers(t *testing.T) {
	for _, body := range []string{`{"state":"paired","knownKey":7}`, `{"state":"paired","knownKey":null}`,
		`{"state":"paired","rotation":{"newKey":1}}`, `{"state":"paired","rotation":null,"knownKey":[]}`} {
		if h, err := ReadHello([]byte(body), false); err != nil || h.State != "paired" {
			t.Errorf("%s: %+v, %v", body, h, err)
		}
	}
	for _, body := range []string{`{"state":"paired","knownKey":7,"address":null}`, `{"state":"paired","rotation":"x","core":[null]}`} {
		if _, err := ReadHello([]byte(body), false); !errors.Is(err, ErrJSON) {
			t.Errorf("%s: %v", body, err)
		}
	}
}
