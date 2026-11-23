package overfin

import (
	"bufio"
	"cmp"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/copies"
	"github.com/overfin/server/internal/files"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/keys"
	"github.com/overfin/server/internal/store"
)

// libraryFolders are the libraries' folders as Jellyfin lists them: the
// only files the backend reads (rule 78).
func libraryFolders(jf *jellyfin.Client, auth func() jellyfin.Auth) func(context.Context) (files.Libraries, error) {
	return func(ctx context.Context) (files.Libraries, error) {
		libs, err := jf.AllLibraries(ctx, auth())
		if err != nil {
			return files.Libraries{}, err
		}
		var folders []string
		for _, l := range libs {
			folders = append(folders, l.Folders...)
		}
		return files.NewLibraries(folders), nil
	}
}

// importProbes runs -import-probes: a header read (path, gzipped or not)
// imported into the copy index (copies.Import), beside the running
// Overfin, whose Jellyfin lists the files. It can be stopped (Ctrl+C) and
// run again: what it imported is skipped. Counts only are said, never a
// path.
func importProbes(cfg *config.Config, db *store.Store, k *keys.Keys, path, root string, w io.Writer, logger *slog.Logger) error {
	if !ownPreviews() {
		return errors.New("-import-probes runs in the backend's container, beside its Jellyfin")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	savedURL, key, saved, err := db.Jellyfin(ctx)
	if err != nil {
		return err
	}
	url := cmp.Or(cfg.Jellyfin.URL, savedURL)
	if !saved || url == "" || key == "" {
		return errors.New("no Jellyfin of Overfin's own yet: set Overfin up first")
	}
	jf, err := jellyfin.New(url, Version)
	if err != nil {
		return err
	}
	auth := func() jellyfin.Auth { return jellyfin.Auth{Token: key, DeviceID: "overfin-admin"} }
	f, err := os.Open(path)
	if err != nil {
		return errors.New("the header read can't be opened") // its name isn't said
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<20)
	var r io.Reader = br
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return errors.New("the header read isn't readable gzip")
		}
		defer zr.Close()
		r = zr
	}
	im := copies.Import{Root: root, Keeper: &copies.Keeper{DB: db, CopyKey: k.CopyID, EtagKey: k.Etag, Logger: logger,
		Files: func(ctx context.Context, each func(jellyfin.VideoFile) bool) error {
			return jf.VideoFiles(ctx, auth(), each)
		},
		Libraries: libraryFolders(jf, auth)}}
	start := time.Now()
	counts, err := im.Run(ctx, r)
	fmt.Fprintf(w, "Header read imported in %s: %s.\n", time.Since(start).Round(time.Second), counts)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "Files left out are read by the copy index as it goes; the frame read of HDR pictures follows, at its lowest priority.")
	return nil
}
