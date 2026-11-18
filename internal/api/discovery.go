package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/overfin/server/internal/cursor"
	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// Discovery serves the catalog's discovery: what's trending, popular or
// upcoming, as titles. Discovery is for what isn't available yet (the
// owner's decision, 2026-11-18): without an availability asked for, only
// the titles no library here has and no paired server holds for us; what
// is available is in the libraries' pages. Home still asks for the
// available ones ("library") or all of them for its trending rows. Lists are
// answered from Overfin's database, which keeps them from TMDB in the
// background (catalog.Lists); a title's page is /titles/{id}.
//
// Kids profiles see only the titles their ceilings allow (rule 41).
type Discovery struct {
	Library Library
	Store   *store.Store
}

func (Discovery) Name() string { return "discovery" }

func (d Discovery) Register(r *plugin.Registrar) error {
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/v1/discover": d.discover,
	} {
		if err := r.HandleFunc(pattern, h); err != nil {
			return err
		}
	}
	return nil
}

func (d Discovery) discover(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r, "list", "mediaKind", "availability", "limit", "cursor") {
		return
	}
	q := r.URL.Query()
	list, kind, avail := q.Get("list"), q.Get("mediaKind"), q.Get("availability")
	if avail == "" {
		avail = "missing"
	}
	limit, ok := parseLimit(q.Get("limit"))
	if !ok || !slices.Contains(tmdb.Lists, list) || (kind != "movie" && kind != "series") ||
		(avail != "all" && avail != "library" && avail != "missing") {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	p := d.Library.active(w, r)
	if p == nil {
		return
	}
	out := titlePage{Items: []library.TitleSummary{}}
	ceiling, err := d.Library.ceiling(r.Context(), p)
	if err != nil {
		d.Library.storeError(w, err)
		return
	}
	binding := cursorBinding(r, p.ID)
	offset := 0
	if c := q.Get("cursor"); c != "" {
		payload, err := cursor.Decode(d.Library.CursorKey, binding, c)
		if err != nil {
			problem.Send(w, problem.InvalidCursor, "")
			return
		}
		if offset, err = strconv.Atoi(string(payload)); err != nil || offset < 0 {
			problem.Send(w, problem.InvalidCursor, "")
			return
		}
	}
	// The titles the profile may not see (visibility) are left out once
	// read: the page reads further, a few times at most, to be full. The
	// cursor is the list's position after the last title looked at.
	next, more, left := offset, true, false
	for round := 0; round < 4 && more && len(out.Items) < limit; round++ {
		// One more than asked for, to know whether there are more.
		titles, err := d.Store.ListTitles(r.Context(), list, kind, avail, ceiling, next, limit+1)
		if errors.Is(err, context.Canceled) {
			return // the client left: nobody to answer, nothing wrong
		}
		if err != nil {
			d.Library.Logger.Error("reading the catalog failed", "err", err)
			problem.Send(w, problem.Internal, "")
			return
		}
		keys := make([]string, 0, len(titles))
		for _, t := range titles {
			keys = append(keys, t.Kind+"/tmdb/"+t.TMDB)
		}
		// The upcoming list keeps what isn't out yet as the panel shows
		// titles, marked "coming soon" like a collection's films; other
		// lists don't.
		shown, err := d.Library.summaries(r.Context(), p, keys, list == "upcoming")
		if err != nil {
			d.Library.storeError(w, err)
			return
		}
		byID := make(map[string]library.TitleSummary, len(shown))
		for _, t := range shown {
			byID[t.ID] = t
		}
		more = len(titles) > limit
		looked := 0
		for _, k := range keys {
			if len(out.Items) == limit {
				more, left = true, true // titles left in this read
				break
			}
			looked++
			t, ok := byID[titleID(k)]
			switch {
			case !ok:
				continue
			// The library has it, but not for this profile: not "in the
			// library" here.
			case avail == "library" && t.Availability == "missing":
				continue
			// Here or at a paired server, as its page counts it (some of
			// a series' episodes, say): available.
			case avail == "missing" && t.Availability != "missing":
				continue
			}
			out.Items = append(out.Items, t)
		}
		next += looked
	}
	// Full on the read's last title: whether the list goes on is known
	// only by looking (a next page with nothing in it otherwise).
	if more && !left {
		rest, err := d.Store.ListTitles(r.Context(), list, kind, avail, ceiling, next, 1)
		if err != nil && !errors.Is(err, context.Canceled) {
			d.Library.Logger.Error("reading the catalog failed", "err", err)
		}
		more = len(rest) > 0 || err != nil
	}
	if more {
		if c, err := cursor.Encode(d.Library.CursorKey, binding, []byte(strconv.Itoa(next))); err == nil {
			out.NextCursor = c
		}
	}
	writeOK(w, out)
}
