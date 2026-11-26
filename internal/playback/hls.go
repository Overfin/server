package playback

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/msfp-io/msfp-go/wire"
)

// A delivery's media playlist, made by Jellyfin through Overfin's plugin,
// is parsed, never searched and replaced (SECURITY.md rule 14). Every URI
// in it may carry Jellyfin's token; each one is replaced by a neutral name,
// and the real URL stays on the server. Tags outside MSFP's media subset
// are dropped (alternate renditions, subtitles, keys, I-frame playlists).

// ErrBadPlaylist: a playlist Overfin can't vouch for.
var ErrBadPlaylist = errors.New("playback: unexpected playlist content")

var (
	// URIs as Jellyfin writes them, relative to the playlist.
	segmentURI = regexp.MustCompile(`^hls1/([A-Za-z0-9_-]{1,32})/(-?[0-9]{1,6})\.(mp4|m4s|ts|aac|mp3)$`)
	mapURI     = regexp.MustCompile(`^#EXT-X-MAP:URI="([^"]*)"$`)
	// Where those URIs may lead on Jellyfin.
	allowedRoute = regexp.MustCompile(`(?i)^/(videos|audio)/[0-9a-f-]{32,36}/([A-Za-z0-9_-]{1,32}\.m3u8|hls1/[A-Za-z0-9_-]{1,32}/-?[0-9]{1,6}\.(mp4|m4s|ts|aac|mp3))$`)
)

// Rewritten is a playlist safe to send, and the real URL behind each
// neutral name in it.
type Rewritten struct {
	Body  []byte
	Files map[string]Upstream
}

// RewriteVariant rewrites a variant playlist. Segments become seg0.mp4,
// seg1.mp4... and the initialization segment init.mp4.
func RewriteVariant(body []byte, upstream Upstream) (*Rewritten, error) {
	return rewrite(body, upstream, wire.MediaSubset, func(uri string, n int) (string, *regexp.Regexp) {
		m := segmentURI.FindStringSubmatch(stripQuery(uri))
		if m == nil {
			return "", segmentURI
		}
		if m[2] == "-1" {
			return "init." + m[3], segmentURI
		}
		return "seg" + m[2] + "." + m[3], segmentURI
	})
}

func stripQuery(uri string) string {
	p, _, _ := strings.Cut(uri, "?")
	return p
}

// rewrite does the work for both kinds. name gives the neutral name for the
// nth URI and the pattern the URI's path must match. Overfin only renames
// here: what is sent is then checked as MSFP checks a paired server's
// media playlist (wire.FilterPlaylist), the one check of playlists, for
// this server's players as for its peers.
func rewrite(body []byte, upstream Upstream, tags []string,
	name func(uri string, n int) (string, *regexp.Regexp)) (*Rewritten, error) {

	// As read, before any token is taken out.
	if len(body) > wire.MaxPlaylist {
		return nil, ErrBadPlaylist
	}
	out := Rewritten{Files: map[string]Upstream{}}
	var b bytes.Buffer
	count := 0
	// resolve turns a URI from the playlist into a checked upstream URL and
	// records it under its neutral name.
	resolve := func(uri string) (string, error) {
		neutral, pattern := name(uri, count)
		u, err := url.Parse(uri)
		if err != nil || neutral == "" || u.IsAbs() || u.Host != "" || u.User != nil || u.Fragment != "" ||
			!pattern.MatchString(u.Path) {
			return "", ErrBadPlaylist
		}
		target := path.Join(path.Dir(upstream.Path), u.Path)
		if !allowedRoute.MatchString(target) || strings.Contains(u.Path, "..") {
			return "", ErrBadPlaylist
		}
		out.Files[neutral] = Upstream{Path: target, Query: u.Query()}
		count++
		return neutral, nil
	}

	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#"):
			tag, _, _ := strings.Cut(line, ":")
			if !slices.Contains(tags, tag) {
				continue // comments and tags outside the subset are dropped
			}
			switch tag {
			case "#EXT-X-MAP":
				mm := mapURI.FindStringSubmatch(line)
				if mm == nil {
					return nil, ErrBadPlaylist
				}
				neutral, err := resolve(mm[1])
				if err != nil {
					return nil, err
				}
				line = `#EXT-X-MAP:URI="` + neutral + `"`
			case "#EXTINF":
				// The duration only (checked below): a segment's title is
				// free text, Jellyfin's to write.
				d, _, _ := strings.Cut(line, ",")
				line = d + ","
			}
			b.WriteString(line + "\n")
		default:
			neutral, err := resolve(line)
			if err != nil {
				return nil, err
			}
			b.WriteString(neutral + "\n")
		}
	}
	checked, err := wire.FilterPlaylist(b.Bytes(), tags, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadPlaylist, err)
	}
	out.Body = checked
	return &out, nil
}
