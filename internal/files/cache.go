package files

import (
	"context"
	"time"

	"github.com/overfin/server/internal/store"
)

// Cache keeps each Jellyfin version's fingerprint (store.PreviewFile, the
// table the seek-bar previews started), taken again only when the file's
// size or time changes. The previews and the copies share it: a file is
// fingerprinted once for both.
type Cache struct {
	DB *store.Store
}

// Fingerprint is the fingerprint of f, the file of a version (item and
// source IDs): the one kept while its size and time (seconds) haven't
// changed, else taken again and kept, seen now.
func (c Cache) Fingerprint(ctx context.Context, itemID, sourceID string, f *File, now time.Time) (string, error) {
	kept, ok, err := c.DB.PreviewFileOf(ctx, itemID, sourceID)
	if err != nil {
		return "", err
	}
	size, mod := f.Info.Size(), f.Info.ModTime().Unix()
	if ok && kept.Size == size && kept.Modified == mod {
		return kept.Fingerprint, nil // unchanged: nothing written
	}
	fp, err := Fingerprint(f)
	if err != nil {
		return "", err
	}
	pf := store.PreviewFile{ItemID: itemID, SourceID: sourceID, Size: size, Modified: mod, Fingerprint: fp}
	return fp, c.DB.SetPreviewFile(ctx, pf, now)
}
