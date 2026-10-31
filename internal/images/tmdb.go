package images

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/upstream"
)

// TMDB's image server: the only host Overfin fetches TMDB images from
// (SECURITY.md rule 5). Browsers never contact it (rule 43). A variable
// for tests only.
var tmdbImageBase = "https://image.tmdb.org/t/p/"

// TMDBRef is a parsed TMDB image ID (ParseArtwork). The file is the image
// path TMDB gives, without its slash and extension.
type TMDBRef struct {
	Kind   string // movie, series; "" for a library title's artwork
	TMDBID int
	Type   string // poster, backdrop, profile (a cast member's photo), logo, still
	Path   string // as TMDB gives it: "/abc123.jpg"
}

// artworkPattern is a title's image: tmdbart_<type>_<file>_<ext>, a
// poster, backdrop, logo, still, or a cast member's photo (profile). The
// titles it belongs to are found from its path.
var artworkPattern = regexp.MustCompile(`^tmdbart_(poster|backdrop|logo|still|profile)_([A-Za-z0-9]{8,64})_(jpg|png)$`)

// ParseArtwork validates a library artwork ID.
func ParseArtwork(id string) (TMDBRef, error) {
	m := artworkPattern.FindStringSubmatch(id)
	if m == nil {
		return TMDBRef{}, ErrInvalid
	}
	return TMDBRef{Type: m[1], Path: "/" + m[2] + "." + m[3]}, nil
}

// ArtworkID is the image ID of a library title's artwork at path; "" when
// the path isn't one.
func ArtworkID(typ, path string) string {
	m := tmdbPath.FindStringSubmatch(path)
	if m == nil {
		return ""
	}
	return fmt.Sprintf("tmdbart_%s_%s_%s", typ, m[1], m[2])
}

var tmdbPath = regexp.MustCompile(`^/([A-Za-z0-9]{8,64})\.(jpg|png)$`)

// Overfin makes TMDB's images the sizes it wants itself (Shrink), from one
// original each: TMDB's original, made smaller on arrival when it's
// larger than any size Overfin serves. Sizes are widths, each type's
// from a few (ladders), so only those are ever made and kept.

// ladders are the widths Overfin serves of each type of TMDB image, from
// the smallest.
var ladders = map[string][]int{
	"poster":   {200, 400, 800, 1200, 2000},
	"backdrop": {320, 640, 1280, 1920, 3840},
	"logo":     {250, 500, 1000, 2000},
	"still":    {320, 640, 1280},
	"profile":  {200, 400, 800},
}

// named are the widths the size names small, medium and large stand for.
var named = map[string]map[string]int{
	"poster":   {"small": 200, "medium": 400, "large": 800},
	"backdrop": {"small": 640, "medium": 1280, "large": 1920},
	"logo":     {"small": 250, "medium": 500, "large": 1000},
	"still":    {"small": 320, "medium": 640, "large": 1280},
	"profile":  {"small": 200, "medium": 400, "large": 800},
}

// originals are what Overfin keeps of each type as its original: TMDB's
// original, at most the largest width it serves (an episode's still, a
// thumbnail, 1280); the fallback, TMDB's largest ready-made size, for an
// original Overfin doesn't take (over MaxDimension, or WebP).
var originals = map[string]struct{ fallback string }{
	"poster": {"w780"}, "backdrop": {"w1280"}, "logo": {"w500"}, "still": {"w300"}, "profile": {"h632"},
}

// Width is the width Overfin serves of a type of TMDB image for one asked
// for: the next in its ladder, or its largest; false for a type it has
// none of, or a width that isn't one.
func Width(typ string, asked int) (int, bool) {
	ladder, ok := ladders[typ]
	if !ok || asked <= 0 {
		return 0, false
	}
	for _, w := range ladder {
		if w >= asked {
			return w, true
		}
	}
	return ladder[len(ladder)-1], true
}

// NamedWidth is the width a size name (small, medium, large) stands for,
// for a type of TMDB image.
func NamedWidth(typ, size string) (int, bool) {
	w, ok := named[typ][size]
	return w, ok
}

// Largest is the largest width served of a type: an original is kept no
// wider.
func Largest(typ string) int {
	if l := ladders[typ]; len(l) > 0 {
		return l[len(l)-1]
	}
	return 0
}

// Smallest is the smallest width served of a type: the control panel's
// thumbnails.
func Smallest(typ string) int {
	if l := ladders[typ]; len(l) > 0 {
		return l[0]
	}
	return 0
}

var tmdbHTTP = &http.Client{
	Timeout: 15 * time.Second,
	// Never follow a redirect (rule 6).
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// TMDB returns a TMDB image, width pixels wide (one of Width's), made by
// Overfin from its original. check runs first, every time, even for cached
// images: it says whether the profile may see the title, and that ref's
// path is the title's real one, so invented paths never reach TMDB or the
// cache. An image whose original is kept on disk (a library title's, Keep)
// has its sizes kept there too, made the first time they're asked for.
func (p *Pipeline) TMDB(ctx context.Context, ref TMDBRef, width int, check func(ctx context.Context) error) ([]byte, string, error) {
	if !slices.Contains(ladders[ref.Type], width) {
		return nil, "", ErrInvalid
	}
	if err := check(ctx); err != nil {
		return nil, "", err
	}
	contentType := contentTypeOf(ref.Type)
	key := "tmdb/" + ref.Path + "/" + strconv.Itoa(width)
	if e, ok := p.cache.get(key); ok {
		return e.data, e.contentType, nil
	}
	made := p.keptFile(ref, strconv.Itoa(width))
	if made != "" {
		if data, err := os.ReadFile(made); err == nil {
			p.cache.put(key, entry{data: data, contentType: contentType})
			return data, contentType, nil
		}
	}
	original, kept, err := p.original(ctx, ref, p.slots)
	if err != nil {
		return nil, "", err
	}
	data, err := p.sized(ctx, original, contentType, width, p.slots)
	if err != nil {
		return nil, "", err
	}
	if kept && made != "" {
		// Not kept is only slower next time: never a failure.
		_ = writeKept(made, data)
	}
	p.cache.put(key, entry{data: data, contentType: contentType})
	return data, contentType, nil
}

// original is a TMDB image's original as Overfin keeps it: from disk when
// it's kept there (true), else from memory, else fetched now.
func (p *Pipeline) original(ctx context.Context, ref TMDBRef, slots chan struct{}) ([]byte, bool, error) {
	if file := p.keptFile(ref, "original"); file != "" {
		if data, err := os.ReadFile(file); err == nil {
			return data, true, nil
		}
	}
	key := "tmdb/" + ref.Path + "/original"
	if e, ok := p.cache.get(key); ok {
		return e.data, false, nil
	}
	data, err := p.fetchOriginal(ctx, ref, slots)
	if err != nil {
		return nil, false, err
	}
	p.cache.put(key, entry{data: data, contentType: contentTypeOf(ref.Type)})
	return data, false, nil
}

// sized makes an original width pixels wide (never wider than it is), in
// one of slots.
func (p *Pipeline) sized(ctx context.Context, original []byte, contentType string, width int, slots chan struct{}) ([]byte, error) {
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-slots }()
	// An original is Overfin's own encoding, checked when it arrived.
	img, err := decodeChecked(original)
	if err != nil {
		return nil, err
	}
	if img.Bounds().Dx() <= width {
		return original, nil
	}
	return encode(Shrink(img, width), contentType)
}

// contentTypeOf is what an image of type typ is encoded as: logos keep
// their transparency.
func contentTypeOf(typ string) string {
	if typ == "logo" {
		return "image/png"
	}
	return "image/jpeg"
}

// fetchOriginal fetches a TMDB image's original and makes it Overfin's:
// checked, no wider than the largest width served of its type, encoded
// again. TMDB's largest ready-made size stands in for an original Overfin
// doesn't take (over MaxDimension, or WebP whatever its name says).
func (p *Pipeline) fetchOriginal(ctx context.Context, ref TMDBRef, slots chan struct{}) ([]byte, error) {
	ladder := ladders[ref.Type]
	if len(ladder) == 0 {
		return nil, ErrInvalid
	}
	raw, err := p.download(ctx, ref, "original")
	if err != nil {
		return nil, err
	}
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-slots }()
	img, err := decodeChecked(raw)
	if errors.Is(err, ErrUnsafe) {
		if raw, err = p.download(ctx, ref, originals[ref.Type].fallback); err != nil {
			return nil, err
		}
		img, err = decodeChecked(raw)
	}
	if err != nil {
		return nil, err
	}
	return encode(Shrink(img, ladder[len(ladder)-1]), contentTypeOf(ref.Type))
}

// download fetches a TMDB image at one of TMDB's sizes, as it is: at most
// jellyfin.MaxImageBytes (ErrUnsafe beyond), ErrHidden when TMDB has none.
func (p *Pipeline) download(ctx context.Context, ref TMDBRef, size string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tmdbImageBase+size+ref.Path, nil)
	if err != nil {
		return nil, err
	}
	res, err := tmdbHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("images: TMDB: %w", upstream.Plain(err))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return nil, ErrHidden
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, jellyfin.MaxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > jellyfin.MaxImageBytes {
		return nil, ErrUnsafe
	}
	return raw, nil
}
