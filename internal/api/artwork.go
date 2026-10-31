package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// Library items' artwork is Overfin's own, from TMDB (catalog.Titles),
// wherever it has some: a title looks the same wherever it's stored.
// Jellyfin's own images stay for what TMDB has none of (titles it doesn't
// know, images placed next to the files).

var (
	posterAspect = 2.0 / 3
	wideAspect   = 16.0 / 9
)

// artworkImage serves a title's artwork, to profiles that may see a title
// it belongs to (in a library or not), and only at a path Overfin keeps.
func (l Library) artworkImage(w http.ResponseWriter, r *http.Request, p *store.Profile, id, size string) {
	ref, err := images.ParseArtwork(id)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	data, contentType, err := l.Images.TMDB(r.Context(), ref, size, func(ctx context.Context) error {
		st := l.Provisioner.Store
		ids, err := st.ArtworkItems(ctx, ref.Type, ref.Path, 50)
		if err != nil {
			return err
		}
		a, err := l.access(ctx, p)
		if err != nil {
			return err
		}
		// A person's own photo: people carry no rating, library or tag.
		if ref.Type == "profile" {
			if ok, err := st.IsPersonPhoto(ctx, ref.Path); err != nil || ok {
				return err
			}
		}
		if len(ids) > 0 {
			if a == nil {
				return nil
			}
			allowed, _, err := st.AllowedItems(ctx, *a, ids)
			if err != nil || len(allowed) > 0 {
				return err
			}
		}
		// The titles no library has that it's one of.
		keys, err := st.ImageTitles(ctx, ref.Type, ref.Path, 50)
		if err != nil {
			return err
		}
		// The collections it's the picture of.
		collections, err := st.ImageCollections(ctx, ref.Type, ref.Path)
		if err != nil {
			return err
		}
		if len(ids) == 0 && len(keys) == 0 && len(collections) == 0 {
			return images.ErrInvalid
		}
		for _, c := range collections {
			if shown, err := l.collectionShown(ctx, p, c); err != nil || shown {
				return err
			}
		}
		infos, err := st.TitleInfos(ctx, keys, today(), "")
		if err != nil {
			return err
		}
		titles := make([]*store.TitleInfo, 0, len(infos))
		for _, t := range infos {
			titles = append(titles, t)
		}
		// Those not out yet too, shown on a collection's page it shows.
		visible, soon, err := l.visibility(ctx, p, titles, true)
		if err != nil {
			return err
		}
		for i, v := range visible {
			if v && !soon[i] {
				return nil
			}
		}
		for i, v := range visible {
			if !v || titles[i].Ref.Kind != "movie" {
				continue
			}
			c, err := st.FilmCollection(ctx, titles[i].Ref.TMDB())
			if err != nil {
				return err
			}
			// Only one its page lists: none other shows it.
			if c == nil || !slices.Contains(c.Parts, titles[i].Ref.TMDB()) {
				continue
			}
			if shown, err := l.collectionShown(ctx, p, c.ID); err != nil || shown {
				return err
			}
		}
		return images.ErrHidden
	})
	switch {
	case err == nil:
	case errors.Is(err, images.ErrInvalid), errors.Is(err, images.ErrHidden):
		problem.Send(w, problem.NotFound, "")
		return
	case errors.Is(err, images.ErrUnsafe):
		l.Logger.Warn("refused an unsafe image from TMDB", "image", id)
		problem.Send(w, problem.NotFound, "")
		return
	case errors.Is(err, context.Canceled):
		return // the client left: nobody to answer, nothing wrong
	default:
		l.Logger.Error("TMDB image failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=604800, immutable")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// uploadImage starts the image ID of artwork an admin uploaded for a title
// (the metadata editor): uploadImage and the upload's ID.
const uploadImage = "titleup_"

// imageID is the image ID of a title's artwork at path: TMDB's, or an
// upload; "" when the path is neither.
func imageID(typ, path string) string {
	if id, ok := store.UploadID(path); ok {
		return uploadImage + id
	}
	return images.ArtworkID(typ, path)
}

// uploadedImage serves artwork an admin uploaded, to profiles that may see
// a title it's the artwork of. It was re-encoded when uploaded, and one
// size serves all.
func (l Library) uploadedImage(w http.ResponseWriter, r *http.Request, p *store.Profile, id string) {
	if _, ok := store.UploadID(store.UploadValue(id)); !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	ctx := r.Context()
	st := l.Provisioner.Store
	keys, err := st.UploadTitles(ctx, id)
	if err != nil {
		l.storeError(w, err)
		return
	}
	infos, err := st.TitleInfos(ctx, keys, today(), "")
	if err != nil {
		l.storeError(w, err)
		return
	}
	titles := make([]*store.TitleInfo, 0, len(infos))
	for _, t := range infos {
		titles = append(titles, t)
	}
	visible, err := l.visibleTitles(ctx, p, titles)
	if err != nil {
		l.storeError(w, err)
		return
	}
	if !slices.Contains(visible, true) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	u, ok, err := st.TitleUploadOf(ctx, id)
	if err != nil {
		l.storeError(w, err)
		return
	}
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	// An upload never changes: a new one has a new ID.
	w.Header().Set("Cache-Control", "private, max-age=604800, immutable")
	w.Header().Set("Content-Type", u.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(u.Data)))
	_, _ = w.Write(u.Data)
}
