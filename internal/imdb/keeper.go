package imdb

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/overfin/server/internal/upstream"
)

// Where IMDb publishes its datasets.
const DatasetsURL = "https://datasets.imdbws.com/"

const (
	// fileName is the compact file, in the keeper's folder.
	fileName = "titles"
	// Refresh is how old the file may get before the datasets are
	// downloaded again: IMDb updates them daily.
	Refresh = 24 * time.Hour
	// retryAfter is how long a failed download waits before the next try.
	retryAfter = 6 * time.Hour
	// maxDownload bounds each dataset's size (the largest is about 230 MB).
	maxDownload = 2 << 30
	// minTitles is the fewest titles a real build has: fewer means IMDb
	// sent something else, and the file in use stays.
	minTitles = 1_000_000
)

// Keeper keeps the compact file up to date while the operator has IMDb's
// data on, in its own folder, and removes it when they switch it off.
type Keeper struct {
	// Dir is the keeper's own folder.
	Dir string
	// On says whether the operator has IMDb's data on.
	On     func(ctx context.Context) (bool, error)
	Logger *slog.Logger
	// Every is how often it checks; BaseURL and Client are where the
	// datasets come from, and how (DatasetsURL and a client of its own by
	// default).
	Every   time.Duration
	BaseURL string
	Client  *http.Client

	mu      sync.Mutex
	data    *Data
	failed  time.Time // the last failed refresh, zero after one works
	problem string    // why it failed
	working bool
	wake    chan struct{}
	now     func() time.Time
	least   int // minTitles, but for tests
}

// NewKeeper returns a keeper of the folder dir.
func NewKeeper(dir string, on func(ctx context.Context) (bool, error), logger *slog.Logger) *Keeper {
	return &Keeper{Dir: dir, On: on, Logger: logger, Every: time.Hour, BaseURL: DatasetsURL,
		// From IMDb's own address only (rule 115): a redirect is a failed
		// refresh, the file in use stays.
		Client: &http.Client{Timeout: 30 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}, wake: make(chan struct{}, 1), least: minTitles}
}

func (k *Keeper) clock() time.Time {
	if k.now != nil {
		return k.now()
	}
	return time.Now()
}

// Data is the file in use, nil when there's none.
func (k *Keeper) Data() *Data {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.data
}

// Status is what the control panel says of IMDb's data.
type Status struct {
	// Titles and Built are the file's: how many titles, and when it was
	// made; 0 and zero when there's none.
	Titles int
	Built  time.Time
	// Working: the datasets are being downloaded and read now.
	Working bool
	// Problem is why the last try failed; "" when it didn't.
	Problem string
}

// Status returns where the keeper is.
func (k *Keeper) Status() Status {
	k.mu.Lock()
	defer k.mu.Unlock()
	s := Status{Working: k.working, Problem: k.problem}
	if k.data != nil {
		s.Titles, s.Built = k.data.Titles, time.Unix(k.data.Built, 0)
	}
	return s
}

// Wake asks for a check now, for example right after the operator
// switched IMDb's data on or off.
func (k *Keeper) Wake() {
	select {
	case k.wake <- struct{}{}:
	default:
	}
}

// Run checks now, then every Every and whenever woken, until ctx ends.
func (k *Keeper) Run(ctx context.Context) {
	for {
		if err := k.Once(ctx); err != nil && ctx.Err() == nil {
			k.Logger.Error("imdb: keeping IMDb's data failed", "err", err)
		}
		select {
		case <-ctx.Done():
			k.close()
			return
		case <-k.wake:
		case <-time.After(k.Every):
		}
	}
}

// Once removes the file if IMDb's data is off; otherwise opens it, and
// downloads the datasets again when it's missing or a day old.
func (k *Keeper) Once(ctx context.Context) error {
	on, err := k.On(ctx)
	if err != nil {
		return err
	}
	if !on {
		k.close()
		k.mu.Lock()
		k.failed, k.problem = time.Time{}, ""
		k.mu.Unlock()
		if err := os.RemoveAll(k.Dir); err != nil {
			return fmt.Errorf("imdb: removing IMDb's data: %w", err)
		}
		return nil
	}
	path := filepath.Join(k.Dir, fileName)
	if k.Data() == nil {
		if d, err := Open(path); err == nil {
			k.swap(d)
		} else if !errors.Is(err, os.ErrNotExist) {
			k.Logger.Warn("imdb: the file of IMDb's data can't be read; downloading it again", "err", err)
		}
	}
	now := k.clock()
	k.mu.Lock()
	fresh := k.data != nil && now.Sub(time.Unix(k.data.Built, 0)) < Refresh
	waiting := !k.failed.IsZero() && now.Sub(k.failed) < retryAfter
	k.mu.Unlock()
	if fresh || waiting {
		return nil
	}

	k.setWorking(true)
	defer k.setWorking(false)
	start := k.clock()
	n, err := k.refresh(ctx, path)
	if err != nil {
		if ctx.Err() == nil {
			k.mu.Lock()
			k.failed, k.problem = k.clock(), err.Error()
			k.mu.Unlock()
		}
		return err
	}
	k.mu.Lock()
	k.failed, k.problem = time.Time{}, ""
	k.mu.Unlock()
	k.Logger.Info("imdb: IMDb's data downloaded", "titles", n, "took", k.clock().Sub(start).Round(time.Second).String())
	return nil
}

// refresh downloads the three datasets, builds the file from them, and
// puts it in use; the datasets are removed after.
func (k *Keeper) refresh(ctx context.Context, path string) (int, error) {
	if err := os.MkdirAll(k.Dir, 0o700); err != nil {
		return 0, err
	}
	names := []string{BasicsFile, EpisodesFile, RatingsFile}
	local := make([]string, len(names))
	defer func() {
		for _, p := range local {
			if p != "" {
				os.Remove(p)
			}
		}
	}()
	for i, name := range names {
		local[i] = filepath.Join(k.Dir, name)
		if err := k.download(ctx, name, local[i]); err != nil {
			return 0, err
		}
	}
	// Each day's votes were kept for trending and popular lists of IMDb's
	// (2026-11-07 to 11-13): no longer made, they go.
	if old, _ := filepath.Glob(filepath.Join(k.Dir, "votes-*")); len(old) > 0 {
		for _, f := range old {
			os.Remove(f)
		}
	}
	tmp := path + ".new"
	n, err := BuildFiles(tmp, local[0], local[1], local[2], k.clock())
	if err != nil {
		return 0, err
	}
	if n < k.least {
		os.Remove(tmp)
		return 0, fmt.Errorf("imdb: only %d titles in IMDb's datasets", n)
	}
	// Opened before it takes the old one's place, so a broken file never
	// does.
	d, err := Open(tmp)
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		d.Close()
		os.Remove(tmp)
		return 0, err
	}
	k.swap(d)
	return n, nil
}

// download saves one dataset to path.
func (k *Keeper) download(ctx context.Context, name, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.BaseURL+name, nil)
	if err != nil {
		return err
	}
	res, err := k.Client.Do(req)
	if err != nil {
		return fmt.Errorf("imdb: downloading %s: %w", name, upstream.Plain(err))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("imdb: downloading %s: %s", name, res.Status)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(res.Body, maxDownload+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxDownload {
		err = errors.New("too large")
	}
	if err != nil {
		return fmt.Errorf("imdb: downloading %s: %w", name, upstream.Plain(err))
	}
	return nil
}

// maxInflated bounds a dataset once decompressed: IMDb's largest is about
// 1 GB; a file that inflates past this is no dataset.
const maxInflated = 16 << 30

// inflated reads a decompressed dataset, failing past maxInflated.
func inflated(r io.Reader) io.Reader { return &capped{r: r, left: maxInflated} }

type capped struct {
	r    io.Reader
	left int64
}

func (c *capped) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errors.New("imdb: too large once decompressed")
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// BuildFiles builds the file path from the three datasets as IMDb
// publishes them (gzipped), safely: what was at path stays until the new
// file is whole on disk.
func BuildFiles(path, basics, episodes, ratings string, now time.Time) (int, error) {
	var readers []io.Reader
	for _, p := range []string{basics, episodes, ratings} {
		f, err := os.Open(p)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		z, err := gzip.NewReader(f)
		if err != nil {
			return 0, fmt.Errorf("imdb: %s: %w", filepath.Base(p), err)
		}
		readers = append(readers, inflated(z))
	}
	tmp := path + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := Build(out, readers[0], readers[1], readers[2], now)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	return n, nil
}

// swap puts d in use, closing the file used before.
func (k *Keeper) swap(d *Data) {
	k.mu.Lock()
	old := k.data
	k.data = d
	k.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

func (k *Keeper) close() { k.swap(nil) }

func (k *Keeper) setWorking(on bool) {
	k.mu.Lock()
	k.working = on
	k.mu.Unlock()
}
