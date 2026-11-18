package msfp

import (
	"slices"

	"github.com/overfin/server/msfp/wire"
)

// The parts this engine speaks (MSFP core section 10), as Config.Parts
// says, and those on with each peer.

// allParts are the parts of MSFP version 1, each at version 1, in the
// order a part's needs come before it.
var allParts = []string{wire.PartCatalog, wire.PartMedia, wire.PartPlayback, wire.PartVideo, wire.PartMusic}

// partNeeds is what each part needs listed beside it (core section 10.1).
var partNeeds = map[string][]string{
	wire.PartPlayback: {wire.PartCatalog, wire.PartMedia},
	wire.PartVideo:    {wire.PartCatalog, wire.PartPlayback, wire.PartMedia},
	wire.PartMusic:    {wire.PartCatalog, wire.PartPlayback, wire.PartMedia},
}

// servedParts are the parts Config.Parts lists (all of them when nil),
// without those whose needs aren't listed.
func servedParts(listed []string) map[string][]int {
	out := map[string][]int{}
	for _, name := range allParts {
		if listed != nil && !slices.Contains(listed, name) {
			continue
		}
		ok := true
		for _, need := range partNeeds[name] {
			if _, on := out[need]; !on {
				ok = false
			}
		}
		if ok {
			out[name] = []int{1}
		}
	}
	return out
}

// versions are what this server speaks: core 1, and its parts.
func (e *Engine) versions() wire.Versions {
	parts := make(map[string][]int, len(e.parts))
	for name, v := range e.parts {
		parts[name] = slices.Clone(v)
	}
	return wire.Versions{Core: []int{1}, Parts: parts}
}

// on says whether part is on between this server and p.
func (e *Engine) on(p *PeerRecord, part string) bool {
	_, parts := e.agreed(p)
	_, ok := parts[part]
	return ok
}

// keyOn says whether a title key is one of a content part on with p, in
// its canonical form: what is sent to p and kept of p's.
func (e *Engine) keyOn(p *PeerRecord, key string) (wire.TitleKey, bool) {
	k, ok := wire.ParseKey(key)
	return k, ok && e.on(p, k.Part)
}
