package api

import (
	"context"
	"slices"

	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/store"
)

// A film's or an episode's copies, as its page gives them to this
// server's players (Overfin's plan, section 4.1): MSFP's copy objects as
// they are, those the profile may see, in Overfin's order (its own first;
// paired servers' come back with federation), and Overfin's wrapper
// beside them: the limits a play goes by, the tracks the profile wishes
// for, what the server can convert to. The player chooses the copy, its
// audio and its delivery from them (the SDK), and builds every label
// from their facts: no name comes from the server (rule 77).

// playLimitsJSON are the limits a play of this server's goes by: the
// bitrate cap (the operator's; none when absent), and whether its players
// may get a copy's file itself (the operator's switch).
type playLimitsJSON struct {
	MaxBitrateKbps int  `json:"maxBitrateKbps,omitempty"`
	OriginalFile   bool `json:"originalFile"`
}

// wishesJSON are the audio and subtitle languages a play starts with: the
// title's choice while watching it, else the profile's preferences. Audio
// absent: the app's own language; subtitles absent: none.
type wishesJSON struct {
	Audio     string `json:"audio,omitempty"`
	Subtitles string `json:"subtitles,omitempty"`
}

// encodersJSON are what the server converts to now (registry tokens),
// the ranges of a converted picture it makes (sdr; keep, where HDR is
// kept), and whether it tone maps (an HDR picture is made SDR only so).
type encodersJSON struct {
	Video       []string `json:"video"`
	Audio       []string `json:"audio"`
	Ranges      []string `json:"ranges"`
	ToneMapping bool     `json:"toneMapping"`
}

// playable says whether a title of kind ref has copies: a film, an
// episode, an item no title was found for.
func playable(ref store.TitleRef) bool {
	return ref.Kind == "movie" || ref.Kind == "episode" || ref.Kind == "item"
}

// titleCopies are the copies of t the profile may see (a title it may
// see: the caller checked), in Overfin's order: its items' (the title's
// own order), each item's by their place; a file in several places
// once, through the first the profile may see. Paired servers' come after
// (peerCopies). A copy MSFP's bounds leave out
// (past 16) isn't offered.
func (l Library) titleCopies(ctx context.Context, p *store.Profile, t *store.TitleInfo) ([]store.IndexedCopy, error) {
	if t == nil || len(t.Items) == 0 {
		return nil, nil
	}
	items := t.Items
	st := l.Provisioner.Store
	a, err := l.access(ctx, p)
	if err != nil {
		return nil, err
	}
	if a != nil {
		allowed, _, err := st.AllowedItems(ctx, *a, items)
		if err != nil {
			return nil, err
		}
		items = slices.DeleteFunc(slices.Clone(items), func(id string) bool { return !allowed[id] })
	}
	// The same file in several places is one copy, through the place
	// the profile may see (store.ItemsCopies): only its allowed items are
	// asked.
	out, err := st.ItemsCopies(ctx, items)
	if err != nil {
		return nil, err
	}
	if len(out) > wire.MaxCopies {
		out = out[:wire.MaxCopies]
	}
	return out, nil
}

// wireCopy is a copy as MSFP's copy object: its facts, ID, etag, previews'
// layout and skip markers.
func (l Library) wireCopy(ctx context.Context, c store.IndexedCopy) (wire.Copy, error) {
	w := c.Facts
	w.ID, w.ETag, w.PreviewLayout = c.CopyID, c.ETag, c.PreviewLayout
	if w.AudioTracks == nil {
		w.AudioTracks = []wire.CopyAudio{} // required, empty for a copy without audio
	}
	segs, err := l.Provisioner.Store.CopySegments(ctx, c.ItemID, c.SourceID)
	if err != nil {
		return wire.Copy{}, err
	}
	if len(segs) > wire.MaxSegments {
		segs = segs[:wire.MaxSegments]
	}
	w.Segments = segs
	return w, nil
}

// copiesPage fills a film's or an episode's page with its copies and the
// wrapper: limits, wishes, encoders (a bonus: absent when the server
// can't say).
func (l Library) copiesPage(ctx context.Context, p *store.Profile, t *store.TitleInfo, out *titleDetail) error {
	copies, err := l.titleCopies(ctx, p, t)
	if err != nil {
		return err
	}
	out.Copies = []wire.Copy{}
	for _, c := range copies {
		w, err := l.wireCopy(ctx, c)
		if err != nil {
			return err
		}
		out.Copies = append(out.Copies, w)
	}
	// Then the paired servers' copies, as they sent them, under Overfin's
	// IDs (a bonus: none when they can't be read).
	theirs, err := l.peerCopies(ctx, p, t)
	if err != nil {
		l.Logger.Warn("could not list paired servers' copies", "err", err)
	}
	for _, pc := range theirs {
		out.Copies = append(out.Copies, pc.playerCopy())
		out.Peers = append(out.Peers, pc.ID)
	}
	out.Limits = &playLimitsJSON{MaxBitrateKbps: l.MaxBitrateKbps, OriginalFile: true}
	if e := l.Delivery; e != nil && e.OriginalFile != nil {
		out.Limits.OriginalFile = e.OriginalFile()
	}
	key := t.Ref.Key()
	if t.Ref.Kind == "episode" {
		key = t.Ref.Series().Key()
	}
	if t.Ref.Kind == "item" {
		if key, err = l.trackKey(ctx, first(t.Items)); err != nil {
			return err
		}
	}
	audio, subtitles := l.wishesFor(ctx, p, key, "")
	out.Wishes = &wishesJSON{Audio: audio, Subtitles: subtitles}
	if e := l.Delivery; e != nil && e.Maker != nil {
		if enc, err := e.Maker.Encoders(ctx); err == nil {
			w := enc.Wire()
			out.Encoders = &encodersJSON{Video: append([]string{}, w.Video...), Audio: append([]string{}, w.Audio...),
				Ranges: append([]string{wire.RangeSDR}, w.Ranges...), ToneMapping: enc.ToneMapping}
		} else {
			l.Logger.Warn("could not read what the server converts to", "err", err)
		}
	}
	return nil
}
