package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// Wishes is each profile's wishlist (ROADMAP: "Wishlists, replacing
// requests"): films and seasons no library has whole yet, in the person's
// own order. A series is wished for season by season (wishing for it adds
// each of its seasons out now and not complete here); on the list it moves
// as a whole, its seasons by number. Overfin takes turns between accounts and hands the
// titles to a fetcher (internal/wishes); a title that arrives leaves the
// wishlist for the watchlist, and the person is told once. Offered when
// the operator chose where wishes go.
type Wishes struct {
	Library Library
}

func (Wishes) Name() string { return "wishes" }

func (wl Wishes) Register(r *plugin.Registrar) error {
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/v1/me/wishlist":           wl.list,
		"PUT /api/v1/me/wishlist":           wl.order,
		"PUT /api/v1/me/wishlist/{id}":      wl.add,
		"DELETE /api/v1/me/wishlist/{id}":   wl.remove,
		"POST /api/v1/me/wishlist/seen":     wl.seen,
		"GET /api/v1/profiles/{id}/wishing": wl.wishing,
		"PUT /api/v1/profiles/{id}/wishing": wl.setWishing,
	} {
		if err := r.HandleFunc(pattern, h); err != nil {
			return err
		}
	}
	return nil
}

// wishable are the kinds of titles one wishes for: never an episode (a
// series stands for its seasons).
var wishable = map[string]bool{"movie": true, "series": true, "season": true}

type wishJSON struct {
	Title library.TitleSummary `json:"title"`
	// State is waiting, requested (handed to Seerr, not started yet),
	// unreleased (with ReleaseDate, when known), notFound (with NextTry) or
	// downloading.
	State       string `json:"state"`
	ReleaseDate string `json:"releaseDate,omitempty"`
	NextTry     string `json:"nextTry,omitempty"`
}

type wishlistJSON struct {
	// On is false when the profile's owner switched its wishing off: its
	// wishes are kept, and wait.
	On    bool       `json:"on"`
	Items []wishJSON `json:"items"`
	// Arrived are wishes that came since the profile was last told: they're
	// on its watchlist now.
	Arrived []library.TitleSummary `json:"arrived"`
}

// stateOfWish says a wished title's state in the API's words.
func stateOfWish(st store.WishState, ok bool) wishJSON {
	switch {
	case !ok:
		return wishJSON{State: "waiting"}
	case st.State == store.WishUnreleased:
		return wishJSON{State: "unreleased", ReleaseDate: st.ReleaseOn}
	case st.State == store.WishRequested:
		return wishJSON{State: "requested"}
	case st.State == store.WishNotFound:
		out := wishJSON{State: "notFound"}
		if !st.NextTry.IsZero() {
			out.NextTry = st.NextTry.UTC().Format(time.RFC3339)
		}
		return out
	}
	return wishJSON{State: "downloading"}
}

func (wl Wishes) list(w http.ResponseWriter, r *http.Request) {
	l := wl.Library
	c := l.Sessions.Current(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	st := l.Provisioner.Store
	off, err := st.WishesOff(ctx, c.Profile.ID)
	if err != nil {
		l.storeError(w, err)
		return
	}
	wishes, err := st.Wishes(ctx, c.Profile.ID)
	if err != nil {
		l.storeError(w, err)
		return
	}
	keys := make([]string, len(wishes))
	for i, wi := range wishes {
		keys[i] = wi.Title
	}
	// What the profile may no longer see (its age changed) is kept, not
	// shown.
	titles, err := l.titleSummaries(ctx, c.Profile, keys)
	if err != nil {
		l.storeError(w, err)
		return
	}
	states, err := st.WishStates(ctx)
	if err != nil {
		l.storeError(w, err)
		return
	}
	out := wishlistJSON{On: !off, Items: []wishJSON{}, Arrived: []library.TitleSummary{}}
	for _, t := range titles {
		key, _ := keyOfTitleID(t.ID)
		s, ok := states[key]
		item := stateOfWish(s, ok)
		item.Title = t
		out.Items = append(out.Items, item)
	}
	arrived, err := st.UnseenArrivals(ctx, c.Profile.ID)
	if err != nil {
		l.storeError(w, err)
		return
	}
	if out.Arrived, err = l.titleSummaries(ctx, c.Profile, arrived); err != nil {
		l.storeError(w, err)
		return
	}
	writeOK(w, out)
}

// keyOfTitleID is a title's key from its API ID.
func keyOfTitleID(id string) (string, bool) {
	ref, ok := titleRef(id)
	if !ok {
		return "", false
	}
	return ref.Key(), true
}

// add puts a film or a season at the end of the profile's wishlist (a
// season beside its series' others), or for a series, each of its seasons
// out now that no library has whole; only titles the profile may see,
// known to TMDB.
func (wl Wishes) add(w http.ResponseWriter, r *http.Request) {
	l := wl.Library
	c := l.Sessions.Current(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	ref, ok := titleRef(r.PathValue("id"))
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if !wishable[ref.Kind] || ref.TMDB() == "" {
		problem.Send(w, problem.InvalidRequest, "only films, series and seasons known to TMDB can be wished for")
		return
	}
	st := l.Provisioner.Store
	if off, err := st.WishesOff(ctx, c.Profile.ID); err != nil {
		l.storeError(w, err)
		return
	} else if off {
		problem.Send(w, problem.WishesOff, "")
		return
	}
	l.describe(ctx, ref)
	t, ok := l.visibleTitleRef(w, ctx, c.Profile, ref)
	if !ok {
		return
	}
	if availability(t) == "available" {
		problem.Send(w, problem.AlreadyRequested, "")
		return
	}
	keys := []string{ref.Key()}
	if ref.Kind == "series" {
		var err error
		if keys, err = wl.missingSeasons(ctx, ref, t); err != nil {
			l.storeError(w, err)
			return
		}
		if len(keys) == 0 {
			problem.Send(w, problem.AlreadyRequested, "")
			return
		}
	}
	for _, key := range keys {
		switch err := st.AddWish(ctx, c.Profile.ID, key); {
		case errors.Is(err, store.ErrWishlistFull):
			problem.Send(w, problem.WishlistFull, "")
			return
		case err != nil:
			l.storeError(w, err)
			return
		}
	}
	if err := st.GroupWishes(ctx, c.Profile.ID); err != nil {
		l.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// missingSeasons are a series' seasons out now (specials aside) that no
// library has whole, in order: their pages are read first, so their
// episodes are counted.
func (wl Wishes) missingSeasons(ctx context.Context, series store.TitleRef, t *store.TitleInfo) ([]string, error) {
	l := wl.Library
	now := today()
	var keys []string
	for _, se := range t.Details.Seasons {
		if se.Number < 1 || se.AirDate == "" || se.AirDate > now {
			continue
		}
		season := store.TitleRef{Kind: "season", Source: series.Source, ID: series.ID, Season: se.Number}
		l.describe(ctx, season)
		keys = append(keys, season.Key())
	}
	infos, err := l.Provisioner.Store.TitleInfos(ctx, keys, now, "")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, k := range keys {
		if info := infos[k]; info == nil || availability(info) != "available" {
			out = append(out, k)
		}
	}
	return out, nil
}

// remove takes a film or a season off the wishlist, or a series' seasons.
func (wl Wishes) remove(w http.ResponseWriter, r *http.Request) {
	l := wl.Library
	c := l.Sessions.Current(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	ref, ok := titleRef(r.PathValue("id"))
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	st := l.Provisioner.Store
	keys := []string{ref.Key()}
	if ref.Kind == "series" {
		// Its seasons, and the series itself, should a wish for it be
		// kept from before seasons were the unit.
		list, err := st.Wishes(ctx, c.Profile.ID)
		if err != nil {
			l.storeError(w, err)
			return
		}
		for _, wi := range list {
			if r, ok := store.ParseTitleKey(wi.Title); ok && r.Kind == "season" && r.Series().Key() == ref.Key() {
				keys = append(keys, wi.Title)
			}
		}
	}
	for _, key := range keys {
		if err := st.RemoveWish(ctx, c.Profile.ID, key); err != nil {
			l.storeError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// order sets the wishlist's order: every title on it, each once, first
// the one wanted most; a series' seasons are kept together, by number.
func (wl Wishes) order(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Titles []string `json:"titles"`
	}
	if !decode(r, &body) || len(body.Titles) > store.MaxWishes {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	l := wl.Library
	c := l.Sessions.Current(w, r)
	if c == nil {
		return
	}
	keys := make([]string, len(body.Titles))
	for i, id := range body.Titles {
		key, ok := keyOfTitleID(id)
		if !ok {
			problem.Send(w, problem.InvalidRequest, "not a title on the wishlist")
			return
		}
		keys[i] = key
	}
	// A series' seasons stay together, where its first one is.
	switch err := l.Provisioner.Store.OrderWishes(r.Context(), c.Profile.ID, store.GroupedWishes(keys)); {
	case errors.Is(err, store.ErrWishOrder):
		problem.Send(w, problem.InvalidRequest, "every title on the wishlist, each once")
	case err != nil:
		l.storeError(w, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// seen marks what arrived as told.
func (wl Wishes) seen(w http.ResponseWriter, r *http.Request) {
	l := wl.Library
	c := l.Sessions.Current(w, r)
	if c == nil {
		return
	}
	if err := l.Provisioner.Store.SeeArrivals(r.Context(), c.Profile.ID); err != nil {
		l.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ownProfile is a profile of the caller's account, for its owner.
func (wl Wishes) ownProfile(w http.ResponseWriter, r *http.Request) (*store.Profile, bool) {
	l := wl.Library
	c := l.Sessions.Current(w, r)
	if c == nil {
		return nil, false
	}
	if !c.IsOwner() {
		problem.Send(w, problem.OwnerOnly, "")
		return nil, false
	}
	p, err := l.Provisioner.Store.Profile(r.Context(), c.Account.ID, r.PathValue("id"))
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return nil, false
	}
	return p, true
}

// wishing says whether a profile may wish (the owner's switch).
func (wl Wishes) wishing(w http.ResponseWriter, r *http.Request) {
	p, ok := wl.ownProfile(w, r)
	if !ok {
		return
	}
	off, err := wl.Library.Provisioner.Store.WishesOff(r.Context(), p.ID)
	if err != nil {
		wl.Library.storeError(w, err)
		return
	}
	writeOK(w, map[string]bool{"on": !off})
}

// setWishing switches a profile's wishing on or off; its wishes are kept,
// and wait while it's off.
func (wl Wishes) setWishing(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On *bool `json:"on"`
	}
	if !decode(r, &body) || body.On == nil {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	p, ok := wl.ownProfile(w, r)
	if !ok {
		return
	}
	if err := wl.Library.Provisioner.Store.SetWishesOff(r.Context(), p.ID, !*body.On); err != nil {
		wl.Library.storeError(w, err)
		return
	}
	writeOK(w, map[string]bool{"on": *body.On})
}

// WishArrivals settles the wishes that arrived: a wished title a library
// now has whole leaves every wishlist it was on, onto those profiles'
// watchlists, and each is told once. Run after every catalog pass.
type WishArrivals struct {
	Store *store.Store
	// Watch puts a title on a profile's watchlist; nil without one.
	Watch  func(ctx context.Context, profileID, titleID string) error
	Logger *slog.Logger

	mu sync.Mutex // one pass at a time
}

// Settle moves what arrived.
func (a *WishArrivals) Settle(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	titles, err := a.Store.WishedTitles(ctx)
	if err != nil || len(titles) == 0 {
		return err
	}
	infos, err := a.Store.TitleInfos(ctx, titles, today(), "")
	if err != nil {
		return err
	}
	for _, key := range titles {
		t := infos[key]
		if t == nil || availability(t) != "available" {
			continue
		}
		profiles, err := a.Store.ArriveWish(ctx, key)
		if err != nil {
			return err
		}
		for _, p := range profiles {
			if a.Watch == nil {
				break
			}
			if err := a.Watch(ctx, p, titleID(key)); err != nil {
				a.Logger.Error("putting an arrived wish on a watchlist failed", "err", err)
			}
		}
	}
	return nil
}
