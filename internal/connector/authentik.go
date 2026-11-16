// Package connector talks to an identity provider's own administration API,
// for what OpenID Connect doesn't cover: who the people behind accounts
// are, and (later) inviting new ones. Connectors are optional and
// provider-specific; Overfin works without them.
//
// What a connector reads is shown live in the control panel and never
// stored (SECURITY.md rule 60).
package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/overfin/server/internal/upstream"
)

// Person is what the identity provider knows about someone.
type Person struct {
	Username string `json:"username"`
	Name     string `json:"name,omitempty"`
	Email    string `json:"email,omitempty"`
	// Active is false when the provider disabled them.
	Active bool `json:"active"`
}

// Groups is a connector that can tell who is in one of the provider's
// groups, by the group's name (as its logins carry it).
type Groups interface {
	// Members returns the user IDs in the group; none when the provider
	// has no such group. Someone whose groups the provider didn't say is
	// left out.
	Members(ctx context.Context, group string) (map[string]bool, error)
}

// Authentik reads people from Authentik's API (/api/v3). Its token needs
// only permission to view users.
type Authentik struct {
	// BaseURL is Authentik's address, e.g. "https://auth.example.org".
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// maxPeople caps how many users one listing reads.
const maxPeople = 20_000

// authentikUser is a user as Authentik's listing gives them.
type authentikUser struct {
	UID      string `json:"uid"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	IsActive bool   `json:"is_active"`
	// GroupsObj is nil when Authentik didn't list the user's groups.
	GroupsObj []struct {
		Name string `json:"name"`
	} `json:"groups_obj"`
}

// People returns everyone the provider knows, by the user ID it gives in
// logins (OpenID Connect's subject, Authentik's hashed "uid").
func (a *Authentik) People(ctx context.Context) (map[string]Person, error) {
	out := map[string]Person{}
	err := a.users(ctx, url.Values{}, func(r authentikUser) {
		out[r.UID] = Person{Username: r.Username, Name: r.Name, Email: r.Email, Active: r.IsActive}
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Members returns the users in the group of that name, from each user's
// groups in the listing.
func (a *Authentik) Members(ctx context.Context, group string) (map[string]bool, error) {
	out := map[string]bool{}
	err := a.users(ctx, url.Values{"include_groups": {"true"}}, func(r authentikUser) {
		for _, g := range r.GroupsObj {
			if g.Name == group {
				out[r.UID] = true
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// users calls fn for each user with an ID, page by page, up to maxPeople;
// q adds to the listing's query.
func (a *Authentik) users(ctx context.Context, q url.Values, fn func(authentikUser)) error {
	seen := 0
	for page := 1; seen < maxPeople; page++ {
		q.Set("page", strconv.Itoa(page))
		q.Set("page_size", "100")
		var res struct {
			Pagination struct {
				Next int `json:"next"`
			} `json:"pagination"`
			Results []authentikUser `json:"results"`
		}
		if err := a.get(ctx, strings.TrimSuffix(a.BaseURL, "/")+"/api/v3/core/users/?"+q.Encode(), &res); err != nil {
			return err
		}
		for _, r := range res.Results {
			if r.UID != "" {
				fn(r)
				seen++
			}
		}
		if res.Pagination.Next == 0 || len(res.Results) == 0 {
			break
		}
	}
	return nil
}

// ErrUnauthorized: the token is wrong, expired, or lacks permission.
var ErrUnauthorized = errors.New("connector: Authentik refused the token: it must be an API Token " +
	"(intent \"API\", not an app password), not expired, for an account allowed to view users")

// Check reads one user, to tell whether the token works.
func (a *Authentik) Check(ctx context.Context) error {
	var res struct{}
	return a.get(ctx, strings.TrimSuffix(a.BaseURL, "/")+"/api/v3/core/users/?page_size=1", &res)
}

func (a *Authentik) get(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	req.Header.Set("Accept", "application/json")
	res, err := a.HTTP.Do(req)
	if err != nil {
		return upstream.Plain(err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case res.StatusCode != http.StatusOK:
		return fmt.Errorf("connector: Authentik answered %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
}
