package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/seerr"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/wishes"
)

// WishesToSeerr hands wishes to Seerr, when that's where the operator says
// they go: Overfin is then their fetcher. Seerr has no pace of its own, so
// Overfin sends at most so many a day (wishes.per_day), in its turns,
// through one Seerr user of its own: Seerr never learns who wished what.
// Each send spends the turn of the account whose wish it was. What Seerr
// does with each request is read back as the wish's state.
type WishesToSeerr struct {
	Store *store.Store
	// Setup says, as things are now, where wishes go, how many a day, and
	// the Seerr connected (nil when none).
	Setup  func() (to string, perDay int, client *seerr.Client)
	Logger *slog.Logger
	// now replaces time.Now in tests.
	now func() time.Time
}

const (
	// wishSeerrEvery is how often wishes are handed over and read back.
	wishSeerrEvery = 10 * time.Minute
	// declinedAgain is when a wish Seerr declined, or that failed there, is
	// handed over again.
	declinedAgain = 30 * 24 * time.Hour
)

// Run hands wishes over and reads them back every wishSeerrEvery, until
// ctx ends.
func (s *WishesToSeerr) Run(ctx context.Context) {
	t := time.NewTicker(wishSeerrEvery)
	defer t.Stop()
	for {
		if err := s.Pass(ctx); err != nil && ctx.Err() == nil {
			s.Logger.Error("handing wishes to Seerr failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *WishesToSeerr) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Pass reads back what Seerr did with the wishes handed over, then hands
// over the next ones, within the day's count.
func (s *WishesToSeerr) Pass(ctx context.Context) error {
	to, perDay, c := s.Setup()
	if to != config.WishesToSeerr || c == nil {
		return nil
	}
	if err := s.readBack(ctx, c); err != nil {
		return err
	}
	now := s.clock()
	sent, err := s.Store.WishSendsSince(ctx, now.Add(-24*time.Hour))
	if err != nil || sent >= perDay {
		return err
	}
	all, err := s.Store.AllWishes(ctx)
	if err != nil {
		return err
	}
	states, err := s.Store.WishStates(ctx)
	if err != nil {
		return err
	}
	turns, err := s.Store.WishTurns(ctx)
	if err != nil {
		return err
	}
	order := wishes.Order(all, states, turns, now, perDay-sent)
	if len(order) == 0 {
		return nil
	}
	user, err := s.user(ctx, c)
	if err != nil {
		return err
	}
	for _, next := range order {
		ref, ok := store.ParseTitleKey(next.Title)
		tmdbID, err := strconv.Atoi(ref.TMDB())
		if !ok || err != nil {
			continue
		}
		kind, seasons := "movie", []int(nil)
		if ref.Kind == "season" {
			kind, seasons = "series", []int{ref.Season}
		}
		st := store.WishState{Title: next.Title, State: store.WishRequested}
		made, err := c.RequestTitle(ctx, user, kind, tmdbID, seasons)
		switch {
		case errors.Is(err, seerr.ErrExists):
			// Asked for in Seerr already (by someone there, say): it
			// comes when it comes, and arrives here like any title.
		case errors.Is(err, seerr.ErrQuota), errors.Is(err, seerr.ErrRefused):
			// Overfin's Seerr user may not request (its permissions, its
			// quota): nothing more goes until the operator sees to it.
			s.Logger.Warn("Seerr refused a wish: Overfin's user there needs the Request permission and no quota", "err", err)
			return nil
		case err != nil:
			return err
		default:
			st.SeerrRequest = made.ID
			st.State = wishStateOf(made)
		}
		// A request, made or found, spends the turn of the account whose
		// wish it was.
		if err := s.Store.SpendWishTurn(ctx, next.AccountID, next.ProfileID); err != nil {
			return err
		}
		if err := s.Store.RecordWishSend(ctx, next.Title); err != nil {
			return err
		}
		if err := s.Store.SetWishState(ctx, st); err != nil {
			return err
		}
	}
	return nil
}

// wishStateOf is a wish's state from its Seerr request: waiting for
// approval, requested; on its way, downloading; declined or failed, not
// found (handed over again later).
func wishStateOf(r seerr.Request) string {
	switch statusOf(r) {
	case "pending", "approved":
		return store.WishRequested
	case "declined", "failed":
		return store.WishNotFound
	}
	return store.WishDownloading
}

// readBack reads what Seerr did with each wish handed over: a request gone
// from Seerr makes the wish wait again; one declined or failed is handed
// over again after declinedAgain.
func (s *WishesToSeerr) readBack(ctx context.Context, c *seerr.Client) error {
	states, err := s.Store.WishStates(ctx)
	if err != nil {
		return err
	}
	for _, st := range states {
		if st.SeerrRequest == 0 || (st.State != store.WishRequested && st.State != store.WishDownloading) {
			continue
		}
		r, err := c.RequestByID(ctx, st.SeerrRequest)
		now := store.WishState{Title: st.Title, SeerrRequest: st.SeerrRequest}
		switch {
		case errors.Is(err, seerr.ErrNotFound):
			now = store.WishState{Title: st.Title} // waiting again
		case err != nil:
			return err
		default:
			now.State = wishStateOf(r)
			if now.State == store.WishNotFound {
				now.SeerrRequest, now.NextTry = 0, s.clock().Add(declinedAgain)
			}
		}
		if now.State != st.State || now.SeerrRequest != st.SeerrRequest {
			if err := s.Store.SetWishState(ctx, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// user is the Seerr user Overfin hands wishes over as, made once: its own,
// named "Overfin", with a placeholder address no one can write to and a
// password no one keeps. Its permissions in Seerr (request, auto-approve)
// are the operator's to set.
func (s *WishesToSeerr) user(ctx context.Context, c *seerr.Client) (int, error) {
	id, ok, err := s.Store.SeerrWishUser(ctx)
	if err != nil {
		return 0, err
	}
	if ok {
		if _, exists, err := c.Username(ctx, id); err != nil {
			return 0, err
		} else if exists {
			return id, nil
		}
	}
	if id, err = c.CreateUser(ctx, "Overfin", "overfin-wishes-"+random(9)+"@overfin.invalid", random(24)); err != nil {
		return 0, err
	}
	return id, s.Store.SetSeerrWishUser(ctx, id)
}

// statusOf is a Seerr request's status in Overfin's words: Seerr's request
// status first (pending, declined, failed), then the title's.
func statusOf(r seerr.Request) string {
	switch r.Status {
	case 1:
		return "pending"
	case 3:
		return "declined"
	case 4:
		return "failed"
	}
	switch r.MediaStatus {
	case 3:
		return "processing"
	case 4:
		return "partiallyAvailable"
	case 5:
		return "available"
	}
	return "approved"
}

// random is n random bytes, as letters and digits.
func random(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return strings.ToLower(strings.NewReplacer("-", "a", "_", "b").Replace(base64.RawURLEncoding.EncodeToString(b)))
}
