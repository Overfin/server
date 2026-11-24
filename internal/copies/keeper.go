package copies

import (
	"bytes"
	"context"
	"encoding/json"
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
	// retry is failedWait's wait, for tests.
	retry time.Duration
	// refused is how many files the last pass left out (said once).
	refused int
}

// Run keeps the index until ctx ends: a pass now, then every Every (a
// minute after a failed one), or at once when woken (the catalog read
// new items).
func (k *Keeper) Run(ctx context.Context) {
	for {
		wait := k.Every
		if err := k.pass(ctx); err != nil && ctx.Err() == nil {
			k.Logger.Warn("copy index", "err", err)
			wait = min(wait, k.failedWait())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
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

// failedWait is how long a failed pass waits for the next: a minute, not
// Every (Jellyfin still starting answers late, and the index stays empty
// until a pass gets through).
func (k *Keeper) failedWait() time.Duration {
	if k.retry > 0 {
		return k.retry
	}
	return time.Minute
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
	// real is the file's real path, in the pass's memory only: a file
	// listed twice is one version (distinct), and the places of one copy
	// are ranked by it (places). place is its rank among them, as kept.
	real      string
	place     int
	size, mod int64
	added     bool // a new arrival
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
		v.copyID, v.real = CopyID(k.CopyKey, v.fingerprint), file.Real
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
	listed = places(distinct(listed))
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
	if err := k.probe(ctx, libs, queue); err != nil {
		return err
	}
	return k.frames(ctx, libs, listed, failed)
}

// distinct keeps one version of each file: a file Jellyfin lists twice
// (an alternate version, under its own item and its film's) is kept
// under its own item (item and source the same), else the first by IDs.
// The same bytes at two paths stay two places of one copy (places).
func distinct(listed []version) []version {
	best := map[string]int{}
	for i, v := range listed {
		j, ok := best[v.real]
		if !ok || better(v, listed[j]) {
			best[v.real] = i
		}
	}
	var out []version
	for i, v := range listed {
		if best[v.real] == i {
			out = append(out, v)
		}
	}
	return out
}

// places ranks the places of each copy (the same file at several paths)
// by their real paths: 0 for the first. Players and peers get a copy
// through one place (store.ItemsCopies): the first library in the
// operator's order they may see, then this rank.
func places(listed []version) []version {
	byCopy := map[string][]int{}
	for i, v := range listed {
		byCopy[v.copyID] = append(byCopy[v.copyID], i)
	}
	for _, at := range byCopy {
		slices.SortFunc(at, func(a, b int) int { return strings.Compare(listed[a].real, listed[b].real) })
		for rank, i := range at {
			listed[i].place = rank
		}
	}
	return listed
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
	same := ok && old.CopyID == v.copyID && old.Fingerprint == v.fingerprint && old.Position == v.position && old.Place == v.place &&
		old.Externals == extHash && old.Parts == max(v.f.Parts, 1)
	// A minimal copy is made again when Jellyfin's data says more of it
	// (an older Overfin's, without its dynamic range).
	m := k.minimal(v, layout, extHash)
	stale := ok && old.Minimal && !sameFacts(old.Facts, m.Facts)
	if same && !stale && (!old.Minimal && old.ProbeVersion >= Version || old.Minimal && !canProbe) {
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
	if !same || stale {
		if err := k.DB.PutCopy(ctx, m); err != nil {
			return false, err
		}
	} else if !equalLayout(old.PreviewLayout, layout) {
		if err := k.DB.SetCopyLayout(ctx, v.f.ItemID, v.f.SourceID, layout); err != nil {
			return false, err
		}
	}
	return canProbe, nil
}

// sameFacts says whether two copies' facts are the same, as kept.
func sameFacts(a, b wire.Copy) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
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
		Place: v.place, Fingerprint: v.fingerprint, ETag: ETag(k.EtagKey, v.fingerprint), Size: v.size, Modified: v.mod, Facts: facts,
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
		Place: v.place, Fingerprint: v.fingerprint, ETag: ETag(k.EtagKey, v.fingerprint), Size: v.size, Modified: v.mod, Facts: facts,
		Streams: streams, Externals: extHash, Parts: max(v.f.Parts, 1), Languages: languages, PreviewLayout: layout,
		ProbeVersion: probeVersion, ProbedAt: probedAt})
}

// probe reads the files of queue, the new arrivals first, then by value
// (store.ItemRanks), one at a time; a file that fails waits a week.
func (k *Keeper) probe(ctx context.Context, libs files.Libraries, queue []version) error {
	if k.Prober == nil || len(queue) == 0 {
		return nil
	}
	if err := k.order(ctx, queue); err != nil {
		return err
	}
	done, failures := 0, 0
	for _, v := range queue {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Read meanwhile (an import running beside the pass): shaped from
		// it, not probed again.
		if shaped, err := k.shapeKept(ctx, v); err != nil {
			return err
		} else if shaped {
			continue
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

// order sorts versions as they are read: the new arrivals first, then by
// value (store.ItemRanks).
func (k *Keeper) order(ctx context.Context, queue []version) error {
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
	return nil
}

// shapeKept shapes a version's copy from a probe of this Version kept
// for its file since it was queued; false when there is none.
func (k *Keeper) shapeKept(ctx context.Context, v version) (bool, error) {
	kept, found, err := k.DB.CopyProbeOf(ctx, v.fingerprint)
	if err != nil || !found || kept.ProbeVersion < Version {
		return false, err
	}
	pr, err := parseKept(kept.Raw)
	if err != nil {
		return false, nil
	}
	var layout *wire.PreviewLayout
	if k.Layout != nil {
		layout, _ = k.Layout(v.fingerprint)
	}
	if err := k.put(ctx, v, pr, layout, kept.ProbeVersion, time.Time{}); err != nil && !errors.Is(err, store.ErrNoItem) {
		return false, err
	}
	return true, nil
}

// frames makes the frame read (describe.Prober.ReadFrames: HDR10+, the
// alternative transfer SEI) that kept probes still need, those imported
// from a header read made without it: last, after every probe, in the
// same order, one file at a time; the copies of its twins are shaped
// again with it. A file whose read fails waits a week.
func (k *Keeper) frames(ctx context.Context, libs files.Libraries, listed []version, failed map[string]bool) error {
	if k.Prober == nil || !k.Prober.Frames {
		return nil
	}
	pending, err := k.DB.CopyProbesPendingFrames(ctx)
	if err != nil || len(pending) == 0 {
		return err
	}
	twins := map[string][]version{}
	var queue []version
	for _, v := range listed {
		if !pending[v.fingerprint] || failed[v.fingerprint] {
			continue
		}
		if twins[v.fingerprint] == nil {
			queue = append(queue, v)
		}
		twins[v.fingerprint] = append(twins[v.fingerprint], v)
	}
	if err := k.order(ctx, queue); err != nil {
		return err
	}
	done, failures := 0, 0
	for _, v := range queue {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := k.readFrames(ctx, libs, twins[v.fingerprint])
		if errors.Is(err, errChanged) || errors.Is(err, files.ErrUnreadable) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failures++
			k.Logger.Warn("copy index: reading a file's first frames failed; tried again in a week", "item", v.f.ItemID, "err", err)
			if err := k.DB.CopyProbeFailed(ctx, v.fingerprint, k.clock()); err != nil {
				return err
			}
			continue
		}
		done++
	}
	if done > 0 || failures > 0 {
		k.Logger.Info("copy index: first frames read", "files", done, "failed", failures)
	}
	return nil
}

// readFrames makes the frame read of a file whose probe is kept without
// it, and keeps it, with the copies of its versions (twins: the same
// fingerprint) shaped again.
func (k *Keeper) readFrames(ctx context.Context, libs files.Libraries, twins []version) error {
	v := twins[0]
	kept, found, err := k.DB.CopyProbeOf(ctx, v.fingerprint)
	if err != nil || !found || kept.ProbeVersion < Version {
		return err // probed again meanwhile, or by an older Version: the probe reads them
	}
	pr, err := parseKept(kept.Raw)
	if err != nil {
		return err
	}
	file, err := libs.Open(v.f.Path)
	if err != nil {
		return err
	}
	real := file.Real
	dir := file.Root
	changed := file.Info.Size() != v.size || file.Info.ModTime().Unix() != v.mod
	file.Close()
	if changed {
		return errChanged
	}
	if err := k.Prober.ReadFrames(ctx, real, pr); err != nil {
		return err
	}
	b, err := keepable(pr, dir)
	if err != nil {
		return err
	}
	now := k.clock()
	if err := k.DB.PutCopyProbe(ctx, store.CopyProbe{Fingerprint: v.fingerprint, ProbeVersion: kept.ProbeVersion,
		MediaType: pr.MediaType, Size: pr.Size, Raw: b}, now); err != nil {
		return err
	}
	for _, t := range twins {
		var layout *wire.PreviewLayout
		if k.Layout != nil {
			layout, _ = k.Layout(t.fingerprint)
		}
		if err := k.put(ctx, t, pr, layout, kept.ProbeVersion, now); err != nil && !errors.Is(err, store.ErrNoItem) {
			return err
		}
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
	return pr, file.Root, nil
}

// keep keeps a probe (raw, without paths) and the copy it shapes.
func (k *Keeper) keep(ctx context.Context, v version, pr *describe.Probe, dir string) error {
	b, err := keepable(pr, dir)
	if err != nil {
		return err
	}
	now := k.clock()
	if err := k.DB.PutCopyProbe(ctx, store.CopyProbe{Fingerprint: v.fingerprint, ProbeVersion: Version, MediaType: pr.MediaType,
		Size: pr.Size, Raw: b, FramesPending: describe.NeedsFrames(pr)}, now); err != nil {
		return err
	}
	var layout *wire.PreviewLayout
	if k.Layout != nil {
		layout, _ = k.Layout(v.fingerprint)
	}
	return k.put(ctx, v, pr, layout, Version, now)
}
