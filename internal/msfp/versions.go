package msfp

import (
	"encoding/json"
	"slices"
)

// MSFP is a core and parts, each with its own version (core section 10).
// Two servers say which versions they speak in every hello, and use, for
// the core and for each part, the highest version both list.

// The parts this package knows (core section 10.1). music is a content
// part too, which a server that has no music doesn't list.
const (
	PartCatalog  = "catalog"
	PartPlayback = "playback"
	PartMedia    = "media"
	PartVideo    = "video"
	PartMusic    = "music"
)

// needs is what each part needs on between two servers to be on itself
// (core section 10.1): the core is needed by all.
var needs = map[string][]string{
	PartCatalog:  nil,
	PartMedia:    nil,
	PartPlayback: {PartCatalog, PartMedia},
	PartVideo:    {PartCatalog, PartPlayback, PartMedia},
	PartMusic:    {PartCatalog, PartPlayback, PartMedia},
}

// contentParts don't work with version 0 of the parts they need (core
// section 10.1).
var contentParts = []string{PartVideo, PartMusic}

// Versions is what a hello lists: the core versions its sender speaks,
// and each part's.
type Versions struct {
	Core  []int            `json:"core"`
	Parts map[string][]int `json:"parts"`
}

// maxVersions is how many entries of each list are read (core section
// 10.2).
const maxVersions = 8

// readVersions reads a hello's core and parts members, raw (nil when
// absent), as core section 10.2 says: absent means none, so a hello
// without core leaves no core in common, and one without parts, no part.
// Entries beyond 8 and entries
// that aren't non-negative integers are ignored, duplicates count once.
// A part this package doesn't know is ignored, whatever its value, so
// that a later version may announce one in another shape. A core or a
// known part's list that isn't a list, or parts that isn't an object, is
// a bad body: ErrJSON.
func readVersions(core, parts json.RawMessage) (Versions, error) {
	v := Versions{Core: []int{}, Parts: map[string][]int{}}
	if core != nil {
		list, err := readList(core)
		if err != nil {
			return Versions{}, err
		}
		v.Core = list
	}
	if parts != nil {
		var obj map[string]json.RawMessage
		if json.Unmarshal(parts, &obj) != nil || obj == nil {
			return Versions{}, ErrJSON
		}
		for name, raw := range obj {
			if _, known := needs[name]; !known {
				continue // not a part we know: ignored, whatever it holds
			}
			list, err := readList(raw)
			if err != nil {
				return Versions{}, err
			}
			v.Parts[name] = list
		}
	}
	return v, nil
}

// readList reads one list of versions.
func readList(raw json.RawMessage) ([]int, error) {
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || entries == nil {
		return nil, ErrJSON
	}
	list := []int{}
	for i, e := range entries {
		if i == maxVersions {
			break
		}
		var n int
		if isInteger(string(e)) && json.Unmarshal(e, &n) == nil && n >= 0 && !slices.Contains(list, n) {
			list = append(list, n)
		}
	}
	return list, nil
}

// highest is the highest version both lists hold; false for none.
func highest(a, b []int) (int, bool) {
	best, ok := 0, false
	for _, n := range a {
		if slices.Contains(b, n) && (!ok || n > best) {
			best, ok = n, true
		}
	}
	return best, ok
}

// Agreed is what two servers use between them (core section 10.3): the
// core version, and each part that is on with its version. A part is on
// when both list a version of it, and every part it needs is on; a
// content part needs those at version 1 or more. ok is false when the two
// have no core version in common: they can't talk, and no part is on.
func Agreed(ours, theirs Versions) (core int, parts map[string]int, ok bool) {
	parts = map[string]int{}
	core, ok = highest(ours.Core, theirs.Core)
	if !ok {
		return 0, parts, false
	}
	for name, list := range ours.Parts {
		if n, both := highest(list, theirs.Parts[name]); both {
			parts[name] = n
		}
	}
	// A part whose need is off is off too; each pass may turn one off
	// that a later part needed, so until nothing changes.
	for changed := true; changed; {
		changed = false
		for name := range parts {
			for _, need := range needs[name] {
				n, on := parts[need]
				if !on || (slices.Contains(contentParts, name) && n < 1) {
					delete(parts, name)
					changed = true
					break
				}
			}
		}
	}
	return core, parts, true
}
