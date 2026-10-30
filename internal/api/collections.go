package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// Collections are TMDB's series of films (catalog.Collections): a profile
// sees those it has at least one film of in a library, as it may see them
// (libraries, hidden tags, kids limits). A collection's page lists all its
// films, those no library has as any title is (titles shown, kids limits),
// and those not out yet too, marked upcoming: they can be requested once
// they're out.

// collections lists the collections the profile has films of, by name.
func (l Library) collections(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	all, err := l.Provisioner.Store.Collections(ctx)
	if err != nil {
		l.storeError(w, err)
		return
	}
	have, err := l.collectionsHave(ctx, p, nil)
	if err != nil {
		l.storeError(w, err)
		return
	}
	var shown []store.Collection
	for _, c := range all {
		if have[c.ID] > 0 {
			shown = append(shown, c)
		}
	}
	films, err := l.collectionFilms(ctx, p, shown)
	if err != nil {
		l.storeError(w, err)
		return
	}
	out := []library.Collection{}
	for _, c := range shown {
		col := collectionOf(c, have[c.ID])
		col.Films = films[c.ID]
		out = append(out, col)
	}
	writeOK(w, map[string]any{"items": out})
}

// collection serves a collection's page: what it is, and its films.
func (l Library) collection(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	id, ok := collectionNumber(r.PathValue("id"))
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	c, err := l.Provisioner.Store.CollectionByID(ctx, id)
	if err != nil {
		l.storeError(w, err)
		return
	}
	var have map[int]int
	if c != nil {
		if have, err = l.collectionsHave(ctx, p, []int{id}); err != nil {
			l.storeError(w, err)
			return
		}
	}
	if c == nil || have[id] == 0 {
		problem.Send(w, problem.NotFound, "")
		return
	}
	keys := make([]string, 0, len(c.Parts))
	for _, film := range c.Parts {
		keys = append(keys, "movie/tmdb/"+film)
	}
	films, err := l.summaries(ctx, p, keys, true)
	if err != nil {
		l.storeError(w, err)
		return
	}
	out := collectionOf(*c, have[id])
	out.Overview = c.Overview
	out.Items = films
	out.Films = len(films)
	writeOK(w, out)
}

// collectionFilms says, for each collection, how many of its films the
// profile sees on its page: its limits and the titles shown, as there.
func (l Library) collectionFilms(ctx context.Context, p *store.Profile, cs []store.Collection) (map[int]int, error) {
	var keys []string
	for _, c := range cs {
		for _, film := range c.Parts {
			keys = append(keys, "movie/tmdb/"+film)
		}
	}
	out := make(map[int]int, len(cs))
	if len(keys) == 0 {
		return out, nil
	}
	// Only what visibility looks at: a film's copies (its page reads the
	// rest).
	copies, err := l.Provisioner.Store.TitleCopies(ctx, keys)
	if err != nil {
		return nil, err
	}
	var list []*store.TitleInfo
	index := map[string]int{}
	for _, k := range keys {
		r, ok := store.ParseTitleKey(k)
		if _, seen := index[k]; seen || !ok {
			continue
		}
		index[k] = len(list)
		list = append(list, &store.TitleInfo{Ref: r, Items: copies[k]})
	}
	visible, _, err := l.visibility(ctx, p, list, true)
	if err != nil {
		return nil, err
	}
	for _, c := range cs {
		for _, film := range c.Parts {
			if i, ok := index["movie/tmdb/"+film]; ok && visible[i] {
				out[c.ID]++
			}
		}
	}
	return out, nil
}

// collectionsHave says, for collections (all of them when nil), how many of
// their films the profile has in a library: those with a copy it may see.
func (l Library) collectionsHave(ctx context.Context, p *store.Profile, collections []int) (map[int]int, error) {
	st := l.Provisioner.Store
	items, err := st.CollectionItems(ctx, collections)
	if err != nil {
		return nil, err
	}
	a, err := l.access(ctx, p)
	if err != nil {
		return nil, err
	}
	var allowed map[string]bool
	if a != nil && len(items) > 0 {
		ids := make([]string, 0, len(items))
		for _, it := range items {
			ids = append(ids, it.ItemID)
		}
		if allowed, _, err = st.AllowedItems(ctx, *a, ids); err != nil {
			return nil, err
		}
	}
	films := map[int]map[string]bool{}
	for _, it := range items {
		if a != nil && !allowed[it.ItemID] {
			continue
		}
		if films[it.Collection] == nil {
			films[it.Collection] = map[string]bool{}
		}
		films[it.Collection][it.TMDB] = true
	}
	out := make(map[int]int, len(films))
	for c, f := range films {
		out[c] = len(f)
	}
	return out, nil
}

// collectionShown says whether the profile sees a collection: it has one
// of its films in a library.
func (l Library) collectionShown(ctx context.Context, p *store.Profile, id int) (bool, error) {
	have, err := l.collectionsHave(ctx, p, []int{id})
	return have[id] > 0, err
}

func collectionOf(c store.Collection, have int) library.Collection {
	out := library.Collection{ID: collectionID(c.ID), Name: c.Name, InLibrary: have, Films: len(c.Parts)}
	var im library.Images
	if id := images.ArtworkID("poster", c.Poster); id != "" {
		im.Primary = &library.ImageRef{URL: "/api/v1/images/" + id, AspectRatio: &posterAspect}
	}
	if id := images.ArtworkID("backdrop", c.Backdrop); id != "" {
		im.Backdrops = []library.ImageRef{{URL: "/api/v1/images/" + id, AspectRatio: &wideAspect}}
	}
	if im.Primary != nil || im.Backdrops != nil {
		out.Images = &im
	}
	return out
}

func collectionID(id int) string { return library.CollectionPrefix + strconv.Itoa(id) }

// collectionNumber is TMDB's ID in a collection's ID.
func collectionNumber(id string) (int, bool) {
	s, ok := strings.CutPrefix(id, library.CollectionPrefix)
	if !ok || s == "" || s[0] == '0' || len(s) > 10 {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > 9_999_999_999 || strconv.Itoa(n) != s {
		return 0, false
	}
	return n, true
}
