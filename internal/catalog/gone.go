package catalog

import (
	"context"
	"errors"
	"strconv"

	"github.com/overfin/server/internal/identify"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// retire handles a library title TMDB no longer has (kind/tmdb old): two
// entries for one film or series merged into the other. It finds the one
// that took over, by the title's IMDb ID (one request), or else by
// matching its files again without the old ID; moves the library's items
// to it, an admin's choice too when the IMDb ID names exactly one title
// (a merge doesn't change which film it is); then moves what people did
// and asked under the old ID (Store.MoveGone), history included. Nobody is
// told: the log says it. Without a sure new ID, the old one is kept as
// gone, and the Identification page says so.
func (a *Titles) retire(ctx context.Context, src TitleSource, kind, old string) error {
	items, err := a.Store.GoneItems(ctx, kind, old)
	if err != nil || len(items) == 0 {
		// Only a paired server's: its own to correct.
		return err
	}
	gone, err := a.Store.Gone(ctx)
	if err != nil {
		return err
	}
	gone[kind+"/"+old] = "" // the folder's tag is the old ID: not to be taken
	now, byIMDb := "", false
	for _, it := range items {
		if it.IMDb == "" {
			continue
		}
		named, err := a.FindByID(ctx, "imdb", it.IMDb, nil)
		if err != nil {
			return err
		}
		var ids []string
		for _, n := range named {
			if n.Kind == kind {
				ids = append(ids, strconv.Itoa(n.TMDB))
			}
		}
		if len(ids) == 1 && ids[0] != old {
			now, byIMDb = ids[0], true
			break
		}
	}
	at := a.clock()
	if now == "" {
		// Matched again by the files, the old ID set aside; an admin's
		// choice isn't: it usually exists because the names misled.
		for _, it := range items {
			if it.Overridden {
				continue
			}
			r, err := identify.Match(ctx, src, identify.Files{Kind: it.Kind, Folder: it.FolderName, File: it.FileName,
				RuntimeMs: it.RuntimeMs, Episodes: it.Episodes, Audio: it.Audio, Gone: gone})
			if err != nil {
				return err
			}
			if r.Sure && r.TMDB != "" && r.TMDB != old {
				now = r.TMDB
				break
			}
		}
	}
	if now == "" {
		a.Logger.Info("catalog: TMDB removed a title, none found to take over", "kind", kind, "tmdb", old)
		return a.Store.SetGone(ctx, kind, old, "", at)
	}
	for _, it := range items {
		if it.Overridden {
			if !byIMDb {
				continue
			}
			err = a.Store.SetOverride(ctx, it.ItemID, now, at)
		} else {
			err = a.Store.SetMatch(ctx, it.ItemID, store.Match{TMDB: now, Sure: true}, it.FolderName, it.FileName, at)
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	if err := a.Store.MoveGone(ctx, kind, old, now, at); err != nil {
		return err
	}
	a.Logger.Info("catalog: TMDB merged a title into another; moved to it with its history", "kind", kind, "from", old, "to", now)
	a.Wake()
	return nil
}

// retired is FetchTitle's answer to a title TMDB doesn't know
// (any more): retire, its own failures logged rather than returned, so
// the title is left as it is until its next refresh.
func (a *Titles) retired(ctx context.Context, src TitleSource, kind, old string) {
	if err := a.retire(ctx, src, kind, old); err != nil && ctx.Err() == nil && !errors.Is(err, tmdb.ErrUnauthorized) {
		a.Logger.Warn("catalog: moving a title TMDB removed", "kind", kind, "tmdb", old, "err", err)
	}
}
