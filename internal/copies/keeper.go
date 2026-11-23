package copies

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/msfp-io/msfp-go/describe"
	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/files"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/store"
)

// retryAfter is how long a file whose probe failed waits to be tried
// again.
const retryAfter = 7 * 24 * time.Hour

// newFor is how long a file counts as a new arrival: probed before the
// rest.
const newFor = 7 * 24 * time.Hour

// Keeper keeps the copy index in step with the libraries' files, in its
// own pass, apart from the seek-bar previews' and never paused for
// transcodes (it reads headers only): every version gets a copy at once
// (minimal while its file isn't probed), and the files new or changed
// since their last probe, or probed by an older Version, are probed, the
// new arrivals first, then the most valued, one at a time.
type Keeper struct {
	wake     chan struct{}
	wakeOnce sync.Once

	DB *store.Store
	// CopyKey and EtagKey are keys.Keys' CopyID and Etag.
	CopyKey, EtagKey []byte
	// Prober reads the files; nil keeps minimal copies only.
	Prober *describe.Prober
	// Files lists the libraries' files (jellyfin.Client.VideoFiles, with
	// an administrator's key).
	Files func(ctx context.Context, each func(jellyfin.VideoFile) bool) error
	// Libraries are the libraries' folders: no file outside is read.
	Libraries func(ctx context.Context) (files.Libraries, error)
	// Layout is a file's seek-bar previews' layout, by fingerprint; nil
	// when Overfin makes none.
	Layout func(fingerprint string) (*wire.PreviewLayout, bool)
	Every  time.Duration
	Logger *slog.Logger
	now    func() time.Time
	// refused is how many files the last pass left out (said once).
	refused int
}

// Run keeps the index until ctx ends: a pass now, then every Every, or at
// once when woken (the catalog read new items).
func (k *Keeper) Run(ctx context.Context) {
	for {
		if err := k.pass(ctx); err != nil && ctx.Err() == nil {
			k.Logger.Warn("copy index", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(k.Every):
		case <-k.woken():
		}
	}
}

// Wake starts a pass now rather than at the next one; a pass under way is
// followed by another.
func (k *Keeper) Wake() {
	if k == nil {
		return
	}
	select {
	case k.woken() <- struct{}{}:
	default:
	}
}

func (k *Keeper) woken() chan struct{} {
	k.wakeOnce.Do(func() { k.wake = make(chan struct{}, 1) })
	return k.wake
}

func (k *Keeper) clock() time.Time {
	if k.now != nil {
		return k.now()
	}
	return time.Now()
}

// version is a listed version whose file could be read.
type version struct {
	f           jellyfin.VideoFile
	fingerprint string
	copyID      string
	position    int
	size, mod   int64
	added       bool // a new arrival
}

// pass lists every version, keeps a copy of each (shaped from its kept
// probe when there is one), forgets those gone, and probes what needs it.
func (k *Keeper) pass(ctx context.Context) error {
	start := k.clock()
	libs, err := k.Libraries(ctx)
	if err != nil {
		return err
	}
	if libs.Empty() {
		return nil // no library folder (yet): nothing read, nothing forgotten
	}
	failed, err := k.DB.CopyProbesFailedSince(ctx, start.Add(-retryAfter))
	if err != nil {
		return err
	}
	var listed []version
	seen := map[[2]string]bool{}
	positions := map[string]int{}
	refused, listedAny := 0, false
	err = k.Files(ctx, func(f jellyfin.VideoFile) bool {
		if ctx.Err() != nil {
			return false
		}
		listedAny = true
		key := [2]string{f.ItemID, f.SourceID}
		v := version{f: f, position: positions[f.ItemID]}
		positions[f.ItemID]++
		file, err := libs.Open(f.Path)
		if err != nil {
			if errors.Is(err, files.ErrOutside) || errors.Is(err, files.ErrNotRegular) {
				refused++ // never read: its copy, if it had one, goes
			} else {
				seen[key] = true // unreadable now: its copy stays as it is
			}
			return true
		}
		defer file.Close()
		v.fingerprint, err = files.Cache{DB: k.DB}.Fingerprint(ctx, f.ItemID, f.SourceID, file, start)
		if err != nil {
			seen[key] = true
			return true
		}
		v.copyID = CopyID(k.CopyKey, file.Rel)
		v.size, v.mod = file.Info.Size(), file.Info.ModTime().Unix()
		v.added = start.Sub(file.Info.ModTime()) < newFor
		listed = append(listed, v)
		return true
	})
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if refused > 0 && refused != k.refused {
		k.Logger.Warn("copy index: files outside the libraries' folders, or not regular files, left out", "files", refused)
	}
	k.refused = refused
	listed = distinct(listed)
	for _, v := range listed {
		seen[[2]string{v.f.ItemID, v.f.SourceID}] = true
	}
	var queue []version
	for _, v := range listed {
		probe, err := k.refresh(ctx, v, failed)
		if err != nil {
			if errors.Is(err, store.ErrNoItem) {
				continue // not in the index yet: the next pass
			}
			return err
		}
		if probe {
			queue = append(queue, v)
		}
	}
	if listedAny {
		if err := k.DB.ForgetIndexedCopies(ctx, seen); err != nil {
			return err
		}
		if err := k.DB.ForgetCopyProbes(ctx, start.Add(-retryAfter)); err != nil {
			return err
		}
	}
	return k.probe(ctx, libs, queue)
}

// distinct keeps one version of each copy ID: a file Jellyfin lists
// twice (an alternate version, under its own item and its film's) is
// one copy, kept under its own item (item and source the same), else
// the first by IDs.
func distinct(listed []version) []version {
	best := map[string]int{}
	for i, v := range listed {
		j, ok := best[v.copyID]
		if !ok || better(v, listed[j]) {
			best[v.copyID] = i
		}
	}
	var out []version
	for i, v := range listed {
		if best[v.copyID] == i {
			out = append(out, v)
		}
	}
	return out
}

func better(a, b version) bool {
	ownA, ownB := a.f.ItemID == a.f.SourceID, b.f.ItemID == b.f.SourceID
	if ownA != ownB {
		return ownA
	}
	if a.f.ItemID != b.f.ItemID {
		return a.f.ItemID < b.f.ItemID
	}
	return a.f.SourceID < b.f.SourceID
}

// refresh keeps a version's copy up to date without probing: new,
// moved, changed, or its subtitle files changed, it is shaped from the
// kept probe of its file when there is one, else made minimal; its
// previews' layout follows the previews. It says whether the file needs
// a probe: none of this Version kept, and none failed lately.
func (k *Keeper) refresh(ctx context.Context, v version, failed map[string]bool) (bool, error) {
	old, ok, err := k.DB.CopyOf(ctx, v.f.ItemID, v.f.SourceID)
	if err != nil {
		return false, err
	}
	_, extHash := externals(v.f)
	var layout *wire.PreviewLayout
	if k.Layout != nil {
		layout, _ = k.Layout(v.fingerprint)
	}
	canProbe := k.Prober != nil && !failed[v.fingerprint]
	same := ok && old.CopyID == v.copyID && old.Fingerprint == v.fingerprint && old.Position == v.position &&
		old.Externals == extHash && old.Parts == max(v.f.Parts, 1)
	if same && (!old.Minimal && old.ProbeVersion >= Version || old.Minimal && !canProbe) {
		if !equalLayout(old.PreviewLayout, layout) {
			return false, k.DB.SetCopyLayout(ctx, v.f.ItemID, v.f.SourceID, layout)
		}
		return false, nil
	}
	// A probe of the same file already kept (a twin, an import, the
	// subtitle files beside it changed): shaped again; probed again only
	// when an older Version made it.
	kept, found, err := k.DB.CopyProbeOf(ctx, v.fingerprint)
	if err != nil {
		return false, err
	}
	if found {
		if pr, err := parseKept(kept.Raw); err == nil {
			if !same || kept.ProbeVersion >= Version || old.Minimal {
				if err := k.put(ctx, v, pr, layout, kept.ProbeVersion, time.Time{}); err != nil {
					return false, err
				}
			}
			return kept.ProbeVersion < Version && canProbe, nil
		}
	}
	if !same {
		if err := k.DB.PutCopy(ctx, k.minimal(v, layout, extHash)); err != nil {
			return false, err
		}
	} else if !equalLayout(old.PreviewLayout, layout) {
		if err := k.DB.SetCopyLayout(ctx, v.f.ItemID, v.f.SourceID, layout); err != nil {
			return false, err
		}
	}
	return canProbe, nil
}

func equalLayout(a, b *wire.PreviewLayout) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// minimal is a version's minimal copy (Minimal).
func (k *Keeper) minimal(v version, layout *wire.PreviewLayout, extHash string) store.IndexedCopy {
	facts, streams := Minimal(v.f)
	facts.Size = v.size
	return store.IndexedCopy{CopyID: v.copyID, ItemID: v.f.ItemID, SourceID: v.f.SourceID, Position: v.position,
		Fingerprint: v.fingerprint, ETag: ETag(k.EtagKey, v.fingerprint), Size: v.size, Modified: v.mod, Facts: facts,
		Streams: streams, Externals: extHash, Minimal: true, Parts: max(v.f.Parts, 1), PreviewLayout: layout}
}

// put keeps a version's copy as a probe (of probeVersion, made at
// probedAt; zero: when it was, as kept) shapes it.
func (k *Keeper) put(ctx context.Context, v version, pr *describe.Probe, layout *wire.PreviewLayout, probeVersion int, probedAt time.Time) error {
	facts, streams, languages := Shape(pr, v.f)
	_, extHash := externals(v.f)
	if probedAt.IsZero() {
		if old, ok, err := k.DB.CopyOf(ctx, v.f.ItemID, v.f.SourceID); err != nil {
			return err
		} else if ok {
			probedAt = old.ProbedAt
		}
	}
	return k.DB.PutCopy(ctx, store.IndexedCopy{CopyID: v.copyID, ItemID: v.f.ItemID, SourceID: v.f.SourceID, Position: v.position,
		Fingerprint: v.fingerprint, ETag: ETag(k.EtagKey, v.fingerprint), Size: v.size, Modified: v.mod, Facts: facts,
		Streams: streams, Externals: extHash, Parts: max(v.f.Parts, 1), Languages: languages, PreviewLayout: layout,
		ProbeVersion: probeVersion, ProbedAt: probedAt})
}

// probe reads the files of queue, the new arrivals first, then by value
// (store.ItemRanks), one at a time; a file that fails waits a week.
func (k *Keeper) probe(ctx context.Context, libs files.Libraries, queue []version) error {
	if k.Prober == nil || len(queue) == 0 {
		return nil
	}
	ranks, err := k.DB.ItemRanks(ctx)
	if err != nil {
		return err
	}
	slices.SortStableFunc(queue, func(a, b version) int {
		if a.added != b.added {
			if a.added {
				return -1
			}
			return 1
		}
		ra, rb := ranks[a.f.ItemID], ranks[b.f.ItemID]
		switch {
		case ra == rb:
			return 0
		case ra == 0:
			return 1
		case rb == 0:
			return -1
		}
		return ra - rb
	})
	done, failures := 0, 0
	for _, v := range queue {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		pr, dir, err := k.read(ctx, libs, v)
		if errors.Is(err, errChanged) || errors.Is(err, files.ErrUnreadable) {
			continue // changed or gone meanwhile: the next pass
		}
		if err == nil {
			err = k.keep(ctx, v, pr, dir)
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, store.ErrNoItem) {
				continue
			}
			failures++
			k.Logger.Warn("copy index: reading a file failed; tried again in a week", "item", v.f.ItemID, "err", err)
			if err := k.DB.CopyProbeFailed(ctx, v.fingerprint, k.clock()); err != nil {
				return err
			}
			continue
		}
		done++
		if done%500 == 0 {
			k.Logger.Info("copy index: files read", "files", done, "of", len(queue))
		}
	}
	if done > 0 || failures > 0 {
		k.Logger.Info("copy index: files read", "files", done, "failed", failures)
	}
	return nil
}

var errChanged = errors.New("copies: the file changed since it was listed")

// read probes a version's file, checked again (files.Libraries.Open)
// and unchanged since it was listed; dir is its folder, which the kept
// probe mustn't name.
func (k *Keeper) read(ctx context.Context, libs files.Libraries, v version) (*describe.Probe, string, error) {
	file, err := libs.Open(v.f.Path)
	if err != nil {
		return nil, "", err
	}
	real := file.Real
	changed := file.Info.Size() != v.size || file.Info.ModTime().Unix() != v.mod
	file.Close()
	if changed {
		return nil, "", errChanged
	}
	pr, err := k.Prober.Probe(ctx, real)
	if err != nil {
		return nil, "", err
	}
	// The library folder the file is in, which the kept probe mustn't name.
	return pr, strings.TrimSuffix(real, "/"+file.Rel), nil
}

// keep keeps a probe (raw, without paths) and the copy it shapes.
func (k *Keeper) keep(ctx context.Context, v version, pr *describe.Probe, dir string) error {
	b, err := keepable(pr, dir)
	if err != nil {
		return err
	}
	now := k.clock()
	if err := k.DB.PutCopyProbe(ctx, store.CopyProbe{Fingerprint: v.fingerprint, ProbeVersion: Version, MediaType: pr.MediaType,
		Size: pr.Size, Raw: b}, now); err != nil {
		return err
	}
	var layout *wire.PreviewLayout
	if k.Layout != nil {
		layout, _ = k.Layout(v.fingerprint)
	}
	return k.put(ctx, v, pr, layout, Version, now)
}
