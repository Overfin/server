// Package wishes takes turns between the people who wish for titles
// (ROADMAP: "Wishlists, replacing requests"). The account whose turn is
// oldest goes first, then the next, and so on, round after round; inside
// an account, its profiles take turns too, so more profiles don't mean
// more turns. Each turn takes the highest wish of that profile that can be
// tried now. A fetcher (Overtide or Seerr) gets the titles in that order,
// and never who wished them.
package wishes

import (
	"slices"
	"strings"
	"time"

	"github.com/overfin/server/internal/store"
)

// Next is a title to fetch, and whose turn it is: the account and profile
// whose wish it is (the first in line, when several wished it).
type Next struct {
	Title     string
	AccountID string
	ProfileID string
}

// Order lists the titles to fetch, next first, at most limit of them.
// Titles downloading, not out yet, or not found and not due again are
// left out: they keep their place in their wishlists, and cost no turn.
func Order(wishes []store.Wish, states map[string]store.WishState, turns map[string]store.WishTurn, now time.Time, limit int) []Next {
	type profile struct {
		id     string
		titles []string // in the person's order
		next   int      // the first title not looked at yet
	}
	type account struct {
		id       string
		turn     time.Time
		profiles []*profile
		next     int // the profile whose turn is next
	}
	byAccount := map[string]*account{}
	byProfile := map[string]*profile{}
	for _, w := range wishes { // in profile, then position order
		a := byAccount[w.AccountID]
		if a == nil {
			a = &account{id: w.AccountID, turn: turns[w.AccountID].At}
			byAccount[w.AccountID] = a
		}
		p := byProfile[w.ProfileID]
		if p == nil {
			p = &profile{id: w.ProfileID}
			byProfile[w.ProfileID] = p
			a.profiles = append(a.profiles, p)
		}
		p.titles = append(p.titles, w.Title)
	}
	accounts := make([]*account, 0, len(byAccount))
	for _, a := range byAccount {
		slices.SortFunc(a.profiles, func(x, y *profile) int { return strings.Compare(x.id, y.id) })
		// The profile after the one whose wish went last goes first.
		if last := turns[a.id].ProfileID; last != "" {
			for i, p := range a.profiles {
				if p.id > last {
					a.next = i
					break
				}
				a.next = (i + 1) % len(a.profiles)
			}
		}
		accounts = append(accounts, a)
	}
	// The oldest turn first; accounts that never had one before any.
	slices.SortFunc(accounts, func(x, y *account) int {
		if c := x.turn.Compare(y.turn); c != 0 {
			return c
		}
		return strings.Compare(x.id, y.id)
	})

	given := map[string]bool{}
	tryable := func(title string) bool {
		if given[title] {
			return false
		}
		st, ok := states[title]
		switch {
		case !ok:
			return true
		case st.State == store.WishNotFound:
			return !st.NextTry.After(now)
		}
		return false // downloading, or not out yet
	}
	// take gives a profile's highest wish that can be tried, if any.
	take := func(p *profile) (string, bool) {
		for ; p.next < len(p.titles); p.next++ {
			if t := p.titles[p.next]; tryable(t) {
				p.next++
				return t, true
			}
		}
		return "", false
	}

	var out []Next
	for len(out) < limit {
		gave := false
		for _, a := range accounts {
			if len(out) >= limit {
				break
			}
			// This account's profiles in turn, from the one whose turn it
			// is: the first with a wish to try takes the turn.
			for k := range a.profiles {
				p := a.profiles[(a.next+k)%len(a.profiles)]
				if t, ok := take(p); ok {
					given[t] = true
					out = append(out, Next{Title: t, AccountID: a.id, ProfileID: p.id})
					a.next = (a.next + k + 1) % len(a.profiles)
					gave = true
					break
				}
			}
		}
		if !gave {
			break
		}
	}
	return out
}
