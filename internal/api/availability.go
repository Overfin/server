package api

import (
	"context"
	"slices"

	"github.com/overfin/server/internal/lang"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/textclean"
	"github.com/overfin/server/msfp"
)

// Where a film or an episode is available, for its page: this server
// and the paired servers holding it, in the order playing picks them
// (sourcesOf), so the first is where Play plays it from. Each says what
// plays there: its picture and its languages. Names only: never a
// paired server's address, and nothing of other people.

// sourceJSON is one place a film or an episode plays from, as its page
// shows it: this server (local) or a paired server, by the name the menu
// gives it; its best copy's picture, and its copies' languages, named in
// themselves as the player names them.
type sourceJSON struct {
	Name      string       `json:"name,omitempty"`
	Local     bool         `json:"local,omitempty"`
	Video     *pictureJSON `json:"video,omitempty"`
	Audio     []string     `json:"audio,omitempty"`
	Subtitles []string     `json:"subtitles,omitempty"`
}

// pictureJSON is a picture: its height (480, 720, 1080, 1440, 2160 or
// 4320) and its HDR formats ("hdr10", "hlg", "dv"; none for standard
// range).
type pictureJSON struct {
	Height int      `json:"height"`
	HDR    []string `json:"hdr,omitempty"`
}

func toPicture(p *msfp.Picture) *pictureJSON {
	if p == nil {
		return nil
	}
	return &pictureJSON{Height: p.Height, HDR: p.HDR}
}

// languageNames names tags in themselves, each once, as copiesFor does.
func languageNames(out []string, tags []string) []string {
	for _, t := range tags {
		if n := lang.Name(t); n != "?" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// availability lists where a film or an episode (id) plays from for p,
// each place once, at the place of its first source; and whether the
// page shows the list (the control panel's setting). When it doesn't,
// only the first is given, without its name: what the page describes.
func (l Library) availability(ctx context.Context, p *store.Profile, id string, ref store.TitleRef) ([]sourceJSON, bool, error) {
	st := l.Provisioner.Store
	shown, err := st.TitleAvailability(ctx)
	if err != nil {
		return nil, false, err
	}
	tracksKey := ref.Key()
	if ref.Kind == "episode" {
		tracksKey = ref.Series().Key()
	}
	language, _ := l.wishesFor(ctx, p, tracksKey, p.TextLanguage)
	sources, err := l.sourcesOf(ctx, p, id, language)
	if err != nil || len(sources) == 0 {
		return nil, shown, err
	}
	var items []string
	for _, s := range sources {
		if s.item != "" {
			items = append(items, s.item)
		}
	}
	copies, err := st.Copies(ctx, items)
	if err != nil {
		return nil, false, err
	}
	br, err := st.Branding(ctx)
	if err != nil {
		return nil, false, err
	}
	var out []sourceJSON
	local := -1
	seen := map[string]bool{}
	for _, s := range sources {
		switch {
		case s.item != "" && local < 0:
			// This server: its copies' languages, its best copy's picture.
			here := sourceJSON{Name: BrandName(br), Local: true}
			var pictures []msfp.Picture
			for _, c := range copies {
				here.Audio = languageNames(here.Audio, c.Audio)
				here.Subtitles = languageNames(here.Subtitles, c.Subtitles)
				if c.Picture != nil {
					pictures = append(pictures, *c.Picture)
				}
			}
			if best, ok := msfp.BestPicture(pictures); ok {
				here.Video = toPicture(&best)
			}
			local = len(out)
			out = append(out, here)
		case s.theirs != nil && !seen[s.peer]:
			seen[s.peer] = true
			out = append(out, sourceJSON{Name: textclean.Plain(s.theirs.Name, 80), Video: toPicture(s.theirs.Video),
				Audio: languageNames(nil, s.theirs.Audio), Subtitles: languageNames(nil, s.theirs.Subtitles)})
		}
	}
	if !shown && len(out) > 0 {
		out = out[:1]
		out[0].Name = ""
	}
	return out, shown, nil
}
