package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/store"
)

// EpisodeFrames gives library episodes TMDB has no picture for a frame of
// their video, taken by Overfin's plugin, one at a time, in the
// background.
//
// The moment: 30 seconds after the intro ends, when the intro is marked,
// a fifth of the way in otherwise. A frame almost black, or almost one
// flat colour (a fade, a title card), is passed over for another moment,
// a third of the way in, then halfway; the first good one is kept, else
// the best of the three. An episode none could be taken of is tried again
// after a day.
type EpisodeFrames struct {
	Store    *store.Store
	Jellyfin *jellyfin.Client
	Images   *images.Pipeline
	// Admin is Overfin's key on its Jellyfin, which the plugin answers;
	// Service, the account that reads the intro's place.
	Admin   func() jellyfin.Auth
	Service func(context.Context) (jellyfin.Auth, error)
	Logger  *slog.Logger
	// Every is how often it looks for episodes to do.
	Every time.Duration
}

const (
	framesPerPass = 200
	frameRetry    = 24 * time.Hour
	// A frame is good when bright enough and not one flat colour.
	frameDark = 0.08
	frameFlat = 0.05
)

// Run makes a pass now, then every Every, until ctx ends.
func (f *EpisodeFrames) Run(ctx context.Context) {
	for {
		if made, err := f.Once(ctx); err != nil && ctx.Err() == nil {
			f.Logger.Error("catalog: episode frames failed", "err", err)
		} else if made > 0 {
			f.Logger.Info("catalog: episode frames made", "frames", made)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(f.Every):
		}
	}
}

// Once makes the frames of up to framesPerPass episodes that need one.
func (f *EpisodeFrames) Once(ctx context.Context) (made int, err error) {
	candidates, err := f.Store.FrameCandidates(ctx, time.Now().Add(-frameRetry), framesPerPass*4)
	if err != nil {
		return 0, err
	}
	for start := 0; start < len(candidates) && made < framesPerPass; start += 100 {
		batch := candidates[start:min(start+100, len(candidates))]
		keys := make([]string, len(batch))
		for i, c := range batch {
			keys[i] = c.TitleKey
		}
		infos, err := f.Store.TitleInfos(ctx, keys, time.Now().Format(time.DateOnly), "")
		if err != nil {
			return made, err
		}
		for _, c := range batch {
			if t := infos[c.TitleKey]; t == nil || t.Still != "" {
				// TMDB has a picture for it: looked at again in a month.
				if err := f.Store.SkipFrame(ctx, c.TitleKey, c.Item, time.Now().Add(30*24*time.Hour), frameRetry); err != nil {
					return made, err
				}
				continue
			}
			ok, err := f.one(ctx, c)
			if err != nil {
				return made, err
			}
			if ok {
				made++
			}
			if made >= framesPerPass {
				break
			}
		}
	}
	return made, nil
}

// one takes an episode's frame and keeps it; false when none could be
// taken (noted, to try again later).
func (f *EpisodeFrames) one(ctx context.Context, c store.FrameCandidate) (bool, error) {
	var best []byte
	bestContrast := -1.0
	for _, at := range f.moments(ctx, c) {
		raw, err := f.Jellyfin.Frame(ctx, f.Admin(), c.Item, at.Seconds())
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			continue
		}
		data, brightness, contrast, err := f.Images.Frame(ctx, raw)
		if err != nil {
			continue
		}
		if brightness >= frameDark && contrast >= frameFlat {
			best = data
			break
		}
		if contrast > bestContrast {
			best, bestContrast = data, contrast
		}
	}
	if best == nil {
		return false, f.Store.PutFrame(ctx, c.TitleKey, c.Item, "", nil, time.Now())
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return true, f.Store.PutFrame(ctx, c.TitleKey, c.Item, hex.EncodeToString(id), best, time.Now())
}

// moments are when to take the frame, in order: after the intro when
// it's marked, else a fifth of the way; then a third, then half.
func (f *EpisodeFrames) moments(ctx context.Context, c store.FrameCandidate) []time.Duration {
	length := c.Runtime
	if length <= 0 {
		length = 20 * time.Minute // unknown: a short episode's
	}
	first := length / 5
	if auth, err := f.Service(ctx); err == nil {
		if segments, err := f.Jellyfin.MediaSegments(ctx, auth, c.Item); err == nil {
			for _, s := range segments {
				if end := time.Duration(s.EndTicks) * 100; s.Type == "Intro" && end+30*time.Second < length {
					first = end + 30*time.Second
					break
				}
			}
		}
	}
	return []time.Duration{first, length / 3, length / 2}
}
