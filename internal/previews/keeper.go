package previews

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/store"
)

// unusedFor is how long previews no file has are kept: a file being moved
// isn't caught halfway.
const unusedFor = 7 * 24 * time.Hour

// retryAfter is how long a file ffmpeg failed on waits to be tried again.
const retryAfter = 7 * 24 * time.Hour

// newFor is how long a file counts as a new arrival: made before the rest.
const newFor = 7 * 24 * time.Hour

// Keeper keeps every file's previews: it fingerprints the libraries'
// files, takes over the previews Jellyfin made (moved into the store),
// makes those missing, the new arrivals first then the most valued, while
// the operator has seek-bar previews on, and deletes those no file has had
// for a week.
type Keeper struct {
	Store  *Store
	DB     *store.Store
	Maker  Maker
	Format Format
	// Files lists the libraries' files (jellyfin.Client.VideoFiles, with
	// an administrator's key).
	Files func(ctx context.Context, each func(jellyfin.VideoFile) bool) error
	// JellyfinData is Jellyfin's data folder, whose data/trickplay holds the
	// previews it made.
	JellyfinData string
	// On says whether the operator has seek-bar previews on.
	On func(ctx context.Context) (bool, error)
	// Hardware are ffmpeg's options for the GPU the operator chose, asked
	// at each pass (HardwareFor); nil decodes on the processor.
	Hardware func(ctx context.Context) []string
	Every    time.Duration
	Logger   *slog.Logger
	now      func() time.Time
}

// Run keeps previews until ctx ends: a pass now, then every Every.
func (k *Keeper) Run(ctx context.Context) {
	k.Store.clearMaking()
	for {
		if err := k.pass(ctx); err != nil && ctx.Err() == nil {
			k.Logger.Warn("seek-bar previews", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(k.Every):
		}
	}
}

func (k *Keeper) clock() time.Time {
	if k.now != nil {
		return k.now()
	}
	return time.Now()
}

// todo is a file whose previews are to be made.
type todo struct {
	fp, path string
	itemID   string
	added    bool // a new arrival
}

// pass fingerprints every file, takes Jellyfin's previews over, makes the
// missing ones (while on), and deletes the long unused.
func (k *Keeper) pass(ctx context.Context) error {
	start := k.clock()
	on, err := k.On(ctx)
	if err != nil {
		return err
	}
	if k.Hardware != nil {
		k.Maker.Hardware = k.Hardware(ctx)
	}
	failed, err := k.DB.PreviewsFailedSince(ctx, start.Add(-retryAfter))
	if err != nil {
		return err
	}
	var queue []todo
	seen := map[string]bool{}        // fingerprints
	versions := map[[2]string]bool{} // item and source IDs
	adopted, listed := 0, 0
	err = k.Files(ctx, func(f jellyfin.VideoFile) bool {
		if ctx.Err() != nil {
			return false
		}
		listed++
		versions[[2]string{f.ItemID, f.SourceID}] = true
		fp, newFile, err := k.fingerprint(ctx, f, start)
		if err != nil {
			return true // unreadable now: next pass
		}
		if seen[fp] {
			return true // another copy of the same file
		}
		seen[fp] = true
		if _, ok := k.Store.Get(fp); ok {
			return true
		}
		if k.adopt(fp, f) {
			adopted++
			return true
		}
		// A file without a picture (sound only, a broken release) has
		// nothing to show: passed over, quietly.
		if !failed[fp] && !f.NoVideo {
			queue = append(queue, todo{fp: fp, path: f.Path, itemID: f.ItemID, added: newFile})
		}
		return true
	})
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if listed > 0 {
		// Versions not listed are gone; their previews, once unused a week.
		if err := k.DB.ForgetPreviewFiles(ctx, versions); err != nil {
			return err
		}
		if err := k.sweep(ctx, start); err != nil {
			return err
		}
	}
	if adopted > 0 {
		k.Logger.Info("seek-bar previews taken over from Jellyfin", "sets", adopted)
	}
	if !on || len(queue) == 0 {
		return nil
	}
	ranks, err := k.DB.ItemRanks(ctx)
	if err != nil {
		return err
	}
	slices.SortStableFunc(queue, func(a, b todo) int {
		if a.added != b.added {
			if a.added {
				return -1
			}
			return 1
		}
		ra, rb := ranks[a.itemID], ranks[b.itemID]
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
	made := 0
	for _, t := range queue {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if on, err := k.On(ctx); err != nil || !on {
			break
		}
		if _, err := k.Maker.Make(ctx, k.Store, t.fp, t.path, k.Format); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			k.Logger.Warn("seek-bar previews of a file failed; tried again in a week", "item", t.itemID, "err", err)
			k.DB.PreviewFailed(ctx, t.fp, k.clock())
			continue
		}
		made++
		if made%100 == 0 {
			k.Logger.Info("seek-bar previews made", "files", made, "of", len(queue))
		}
	}
	if made > 0 {
		k.Logger.Info("seek-bar previews made", "files", made)
	}
	return nil
}

// fingerprint is a version's fingerprint: the one kept while the file's
// size and time haven't changed, else taken again. newFile says the file
// arrived lately.
func (k *Keeper) fingerprint(ctx context.Context, f jellyfin.VideoFile, now time.Time) (string, bool, error) {
	st, err := os.Stat(f.Path)
	if err != nil {
		return "", false, err
	}
	newFile := now.Sub(st.ModTime()) < newFor
	kept, ok, err := k.DB.PreviewFileOf(ctx, f.ItemID, f.SourceID)
	if err != nil {
		return "", false, err
	}
	if ok && kept.Size == st.Size() && kept.Modified == st.ModTime().Unix() {
		return kept.Fingerprint, newFile, nil // unchanged: nothing written
	}
	fp, err := Fingerprint(f.Path)
	if err != nil {
		return "", false, err
	}
	pf := store.PreviewFile{ItemID: f.ItemID, SourceID: f.SourceID, Size: st.Size(), Modified: st.ModTime().Unix(), Fingerprint: fp}
	return fp, newFile, k.DB.SetPreviewFile(ctx, pf, now)
}

// adopt takes over the previews Jellyfin made of a version, in the format
// Overfin makes them (same width), moving them into the store.
func (k *Keeper) adopt(fp string, f jellyfin.VideoFile) bool {
	if k.JellyfinData == "" {
		return false
	}
	for _, tp := range f.Trickplay {
		if tp.Width != k.Format.Width {
			continue
		}
		set := Set{Width: tp.Width, Height: tp.Height, Columns: tp.Columns, Rows: tp.Rows, Count: tp.Count, IntervalMs: tp.IntervalMs}
		if !set.valid() {
			continue
		}
		from := filepath.Join(k.JellyfinData, "data", "trickplay", f.ItemID[:2], dashed(f.ItemID),
			fmt.Sprintf("%d - %dx%d", tp.Width, tp.Columns, tp.Rows))
		if _, err := os.Stat(filepath.Join(from, "0.jpg")); err != nil {
			continue
		}
		if err := k.Store.adopt(fp, set, from); err != nil {
			k.Logger.Warn("taking over Jellyfin's seek-bar previews failed", "item", f.ItemID, "err", err)
			continue
		}
		return true
	}
	return false
}

// dashed is a 32-character Jellyfin ID as Jellyfin names folders.
func dashed(id string) string {
	if len(id) != 32 {
		return id
	}
	return id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
}

// sweep deletes previews no file has had for a week.
func (k *Keeper) sweep(ctx context.Context, now time.Time) error {
	all, err := k.Store.All()
	if err != nil {
		return err
	}
	used, err := k.DB.PreviewFingerprints(ctx)
	if err != nil {
		return err
	}
	var unused []string
	for _, fp := range all {
		if !used[fp] {
			unused = append(unused, fp)
		}
	}
	gone, err := k.DB.UnusedPreviews(ctx, unused, now, now.Add(-unusedFor))
	if err != nil {
		return err
	}
	for _, fp := range gone {
		if err := k.Store.Remove(fp); err != nil {
			return err
		}
	}
	if len(gone) > 0 {
		k.Logger.Info("seek-bar previews of files gone for a week deleted", "sets", len(gone))
	}
	return nil
}

// ForVersion is a version's previews in the store: its fingerprint and
// set; ok is false when the store has none (yet).
func (k *Keeper) ForVersion(ctx context.Context, itemID, sourceID string) (fp string, set Set, ok bool) {
	if k == nil {
		return "", Set{}, false
	}
	pf, found, err := k.DB.PreviewFileOf(ctx, itemID, sourceID)
	if err != nil || !found {
		return "", Set{}, false
	}
	set, ok = k.Store.Get(pf.Fingerprint)
	return pf.Fingerprint, set, ok
}
