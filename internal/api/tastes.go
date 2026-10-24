package api

import (
	"net/http"
	"slices"
	"time"

	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// What the account likes, for the title valuation: asked when the
// operator chose to, and the owner profile's to answer, like everything
// account-wide (SECURITY.md rule 25). Never shown to anyone else.

type tastesJSON struct {
	// Prefer is "series", "movies" or "both"; absent when it didn't say.
	Prefer string `json:"prefer,omitempty"`
	// Genres are TMDB genre IDs.
	Genres []int `json:"genres"`
	// Kids is whether it wants kids' content.
	Kids      bool   `json:"kids"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type tasteGenreJSON struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func tastesOf(t *store.Tastes) tastesJSON {
	out := tastesJSON{Prefer: t.Prefer, Genres: t.Genres, Kids: t.Kids}
	if out.Genres == nil {
		out.Genres = []int{}
	}
	if !t.UpdatedAt.IsZero() {
		out.UpdatedAt = t.UpdatedAt.Format(time.RFC3339)
	}
	return out
}

// maxTasteGenres is the most genres an account may say it likes.
const maxTasteGenres = 40

// tastesCaller is the current owner profile when the operator asks
// accounts what they like; otherwise it has answered.
func (a Account) tastesCaller(w http.ResponseWriter, r *http.Request) *Caller {
	c := a.Sessions.Current(w, r)
	if c == nil {
		return nil
	}
	if !c.IsOwner() {
		problem.Send(w, problem.OwnerOnly, "")
		return nil
	}
	asked, err := a.Sessions.Store.TastesAsked(r.Context())
	if err != nil {
		a.Logger.Error("reading whether tastes are asked failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return nil
	}
	if !asked {
		problem.Send(w, problem.NotFound, "")
		return nil
	}
	return c
}

// tastes (GET /api/v1/me/tastes) answers what the account said it likes,
// the genres it may choose among, and whether it has a kids profile (it
// then gets kids' content without saying so).
func (a Account) tastes(w http.ResponseWriter, r *http.Request) {
	c := a.tastesCaller(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	t, err := a.Sessions.Store.AccountTastes(ctx, c.Account.ID)
	if err != nil {
		a.Logger.Error("reading an account's tastes failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	genres, err := a.Sessions.Store.TasteGenres(ctx, c.Account.Language)
	if err != nil {
		a.Logger.Error("reading the genres failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	profiles, err := a.Sessions.Store.Profiles(ctx, c.Account.ID)
	if err != nil {
		a.Logger.Error("reading an account's profiles failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	out := struct {
		tastesJSON
		Answered    bool             `json:"answered"`
		Choices     []tasteGenreJSON `json:"choices"`
		KidsProfile bool             `json:"kidsProfile"`
	}{tastesJSON: tastesJSON{Genres: []int{}}, Answered: t != nil, Choices: []tasteGenreJSON{}}
	if t != nil {
		out.tastesJSON = tastesOf(t)
	}
	for _, g := range genres {
		out.Choices = append(out.Choices, tasteGenreJSON{g.ID, g.Name})
	}
	for _, p := range profiles {
		if p.Kind == "kids" {
			out.KidsProfile = true
		}
	}
	writeOK(w, out)
}

// setTastes (PUT /api/v1/me/tastes) keeps what the account says it likes.
// Saying nothing (no preference, no genre) records that it was asked.
func (a Account) setTastes(w http.ResponseWriter, r *http.Request) {
	c := a.tastesCaller(w, r)
	if c == nil {
		return
	}
	var req struct {
		Prefer string `json:"prefer"`
		Genres []int  `json:"genres"`
		Kids   bool   `json:"kids"`
	}
	if !decode(r, &req) || !slices.Contains([]string{"", "series", "movies", "both"}, req.Prefer) || len(req.Genres) > maxTasteGenres {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	ctx := r.Context()
	known, err := a.Sessions.Store.TasteGenres(ctx, c.Account.Language)
	if err != nil {
		a.Logger.Error("reading the genres failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	var genres []int
	for _, g := range req.Genres {
		if !slices.ContainsFunc(known, func(k store.TasteGenre) bool { return k.ID == g }) {
			problem.Send(w, problem.InvalidRequest, "not a genre to choose")
			return
		}
		if !slices.Contains(genres, g) {
			genres = append(genres, g)
		}
	}
	t := store.Tastes{Prefer: req.Prefer, Genres: genres, Kids: req.Kids}
	if err := a.Sessions.Store.SetAccountTastes(ctx, c.Account.ID, t, time.Now()); err != nil {
		a.Logger.Error("saving an account's tastes failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	saved, err := a.Sessions.Store.AccountTastes(ctx, c.Account.ID)
	if err != nil || saved == nil {
		a.Logger.Error("reading an account's tastes failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	writeOK(w, tastesOf(saved))
}
