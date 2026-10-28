package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/overfin/server/internal/cookie"
	"github.com/overfin/server/internal/ids"
	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/pin"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/provision"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/textclean"
)

// Profiles serves profiles ("Who's watching?") and sessions.
type Profiles struct {
	Sessions    *Sessions
	Provisioner *provision.Provisioner
	Store       *store.Store
	PINs        *pin.Checker
	// Images, when set, forgets a profile's image access when its limits
	// change.
	Images *images.Pipeline
	// Service is Overfin's one Jellyfin account, which lists the libraries.
	Service *provision.Service
	// LogoutURL is where the site sends a user to also leave the identity
	// provider; empty when there is none.
	LogoutURL string
	Logger    *slog.Logger
}

func (Profiles) Name() string { return "profiles" }

func (p Profiles) Register(r *plugin.Registrar) error {
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/v1/profiles":                p.list,
		"POST /api/v1/profiles":               p.create,
		"GET /api/v1/profiles/{id}":           p.get,
		"PATCH /api/v1/profiles/{id}":         p.update,
		"DELETE /api/v1/profiles/{id}":        p.remove,
		"POST /api/v1/profiles/{id}/activate": p.activate,
		"PUT /api/v1/profiles/{id}/pin":       p.setPIN,
		"DELETE /api/v1/profiles/{id}/pin":    p.removePIN,
		"GET /api/v1/tags":                    p.tags,
		"GET /api/v1/sessions":                p.sessions,
		"DELETE /api/v1/sessions":             p.endOtherSessions,
		"DELETE /api/v1/sessions/{id}":        p.endSession,
	} {
		if err := r.HandleFunc(pattern, h); err != nil {
			return err
		}
	}
	// Signing out needs no identity: it ends only what this browser's own
	// cookies hold, and someone signed in who must still choose a password
	// has none yet.
	return r.HandlePublic("POST /api/v1/logout", http.HandlerFunc(p.logout))
}

func (p Profiles) list(w http.ResponseWriter, r *http.Request) {
	c := p.Sessions.CurrentAccount(w, r)
	if c == nil {
		return
	}
	all, err := p.Store.Profiles(r.Context(), c.Account.ID)
	if err != nil {
		p.Logger.Error("listing profiles failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	out := []profileJSON{}
	for _, pr := range all {
		out = append(out, toProfileJSON(pr))
	}
	writeOK(w, map[string]any{"items": out})
}

func (p Profiles) get(w http.ResponseWriter, r *http.Request) {
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	pr, err := p.Store.Profile(r.Context(), c.Account.ID, r.PathValue("id"))
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	writeOK(w, toProfileJSON(pr))
}

type profileRequest struct {
	Name     *string `json:"name"`
	Kind     *string `json:"kind"`
	Language *string `json:"language"`
	// Theme is the profile's look: "light", "dark", or "" to follow the
	// device. Each profile may change its own.
	Theme *string `json:"theme"`
	// MaxAge is a kids profile's age (0 to 21): it sees titles for that
	// age at most, films and series alike.
	MaxAge *int `json:"maxAge"`
	// Libraries are the only libraries the profile gets (null: every
	// library); BlockedTags, the tags hidden from it (null: none).
	Libraries   optionalList `json:"libraries"`
	BlockedTags optionalList `json:"blockedTags"`
	// Categories are the only categories the profile sees (null: every
	// title, as far as its other limits go).
	Categories optionalList `json:"categories"`
	// KidsTitles is whether the profile wants kids titles in its
	// recommendations; null, as its account does. Each profile may change
	// its own.
	KidsTitles optionalBool `json:"kidsTitles"`
	// PIN is the owner's PIN, needed to change a profile's limits when the
	// owner has one.
	PIN string `json:"pin"`
}

// optionalList is a list a request may leave out (Set false), set to null
// (Set, Value nil) or set to a list.
// optionalBool is a field that may be absent, null or a boolean.
type optionalBool struct {
	Set   bool
	Value *bool
}

func (o *optionalBool) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var v bool
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

type optionalList struct {
	Set   bool
	Value []string
}

func (o *optionalList) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	o.Value = []string{}
	return json.Unmarshal(b, &o.Value)
}

// accessFrom checks the libraries and hidden tags a request gives, from
// pr's (nil for a new profile), and returns them as the store keeps them;
// ok is false when they're invalid. The libraries must be among those the
// owner gets.
func (p Profiles) accessFrom(ctx context.Context, c *Caller, pr *store.Profile, req profileRequest) (libs, tags []string, ok bool, err error) {
	if pr != nil {
		libs, tags = pr.Libraries, pr.BlockedTags
	}
	if req.Libraries.Set {
		if req.Libraries.Value == nil {
			libs = nil
		} else {
			if len(req.Libraries.Value) > 100 {
				return nil, nil, false, nil
			}
			raw, err := libraryItems(ctx, p.Store, p.Service, p.Provisioner.Jellyfin)
			if err != nil {
				return nil, nil, false, err
			}
			known := map[string]bool{}
			for _, l := range raw {
				if id, err := jellyfin.NormalizeID(l.Id); err == nil {
					known[id] = true
				}
			}
			libs = []string{}
			for _, v := range req.Libraries.Value {
				id, good := library.Unwrap(library.LibraryPrefix, v)
				if !good || !known[id] {
					return nil, nil, false, nil
				}
				if !slices.Contains(libs, id) {
					libs = append(libs, id)
				}
			}
		}
	}
	if req.BlockedTags.Set {
		tags = nil
		if req.BlockedTags.Value != nil {
			if len(req.BlockedTags.Value) > 50 {
				return nil, nil, false, nil
			}
			tags = []string{}
			for _, t := range req.BlockedTags.Value {
				if t == "" || len(t) > 128 || textclean.Plain(t, 128) != t {
					return nil, nil, false, nil
				}
				if !slices.ContainsFunc(tags, func(o string) bool { return store.FoldTag(o) == store.FoldTag(t) }) {
					tags = append(tags, t)
				}
			}
		}
	}
	return libs, tags, true, nil
}

var languageTagRe = regexp.MustCompile(`^[a-zA-Z]{2,3}(-[a-zA-Z0-9]{2,8})*$`)

// validName accepts 1 to 32 bytes of plain text that is already clean.
func validName(s string) bool {
	return s != "" && len(s) <= 32 && utf8.ValidString(s) && textclean.Plain(s, 32) == s
}

// categoriesFrom reads the categories a profile is to see, from a
// request, else as they are (pr; nil for a new profile). ok is false when
// one isn't the operator's.
func (p Profiles) categoriesFrom(ctx context.Context, pr *store.Profile, req profileRequest) (cats []string, ok bool, err error) {
	if pr != nil {
		cats = pr.Categories
	}
	if !req.Categories.Set {
		return cats, true, nil
	}
	if req.Categories.Value == nil {
		return nil, true, nil
	}
	if len(req.Categories.Value) > 50 {
		return nil, false, nil
	}
	cats = []string{}
	for _, id := range req.Categories.Value {
		if !ids.Valid("cat", id) {
			return nil, false, nil
		}
		if _, err := p.Store.CategoryByID(ctx, id); errors.Is(err, store.ErrNotFound) {
			return nil, false, nil
		} else if err != nil {
			return nil, false, err
		}
		if !slices.Contains(cats, id) {
			cats = append(cats, id)
		}
	}
	return cats, true, nil
}

// validAge checks a kids profile's age, when one is given.
func validAge(age *int) bool { return age == nil || (*age >= 0 && *age <= 21) }

// tags lists the library's tags, for the owner choosing which to hide from
// a profile.
func (p Profiles) tags(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	if !c.IsOwner() {
		problem.Send(w, problem.OwnerOnly, "")
		return
	}
	tags, err := p.Store.CatalogTags(r.Context())
	if err != nil {
		p.Logger.Error("listing tags failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	if tags == nil {
		tags = []string{}
	}
	writeOK(w, map[string]any{"items": tags})
}

func (p Profiles) create(w http.ResponseWriter, r *http.Request) {
	var req profileRequest
	if !decode(r, &req) || req.Name == nil || !validName(*req.Name) || req.PIN != "" ||
		(req.Language != nil && !languageTagRe.MatchString(*req.Language)) ||
		(req.Theme != nil && *req.Theme != "" && *req.Theme != "light" && *req.Theme != "dark") {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	kind := "standard"
	if req.Kind != nil {
		kind = *req.Kind
	}
	// A kids profile needs an age; any other profile can't have one.
	kids := kind == "kids"
	if (kind != "standard" && !kids) || kids != (req.MaxAge != nil) || !validAge(req.MaxAge) {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	if !c.IsOwner() {
		problem.Send(w, problem.OwnerOnly, "")
		return
	}
	np := store.NewProfile{Name: *req.Name, Kind: kind}
	if req.Language != nil {
		np.Language = *req.Language
	}
	if libs, tags, ok, err := p.accessFrom(r.Context(), c, nil, req); err != nil {
		p.Logger.Error("listing libraries failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	} else if !ok {
		problem.Send(w, problem.InvalidRequest, "unknown library or invalid tag")
		return
	} else {
		np.Libraries, np.BlockedTags = libs, tags
	}
	if kids {
		np.MaxAge = req.MaxAge
	}
	if cats, ok, err := p.categoriesFrom(r.Context(), nil, req); err != nil {
		p.Logger.Error("listing categories failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	} else if !ok {
		problem.Send(w, problem.InvalidRequest, "unknown category")
		return
	} else {
		np.Categories = cats
	}
	prof, err := p.Provisioner.AddProfile(r.Context(), c.Account.ID, np, c.Account.Template.ProfileLimit())
	switch {
	case errors.Is(err, store.ErrNameTaken):
		problem.Send(w, problem.NameTaken, "")
	case errors.Is(err, store.ErrProfileLimit):
		problem.Send(w, problem.ProfileLimitReached, "")
	case err != nil:
		p.Logger.Error("creating a profile failed", "err", err)
		problem.Send(w, problem.Internal, "")
	default:
		// Its look, if one was chosen: kept like a change made later.
		if req.Theme != nil && *req.Theme != "" {
			if err := p.Store.SetProfileTheme(r.Context(), c.Account.ID, prof.ID, *req.Theme); err != nil {
				p.Logger.Error("setting a new profile's look failed", "err", err)
			} else {
				prof.Theme = *req.Theme
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		writeOK(w, toProfileJSON(prof))
	}
}

func (p Profiles) update(w http.ResponseWriter, r *http.Request) {
	var req profileRequest
	if !decode(r, &req) ||
		(req.Name == nil && req.Language == nil && req.Theme == nil && req.Kind == nil && req.MaxAge == nil &&
			!req.Libraries.Set && !req.BlockedTags.Set && !req.Categories.Set && !req.KidsTitles.Set) || !validAge(req.MaxAge) ||
		(req.Theme != nil && *req.Theme != "" && *req.Theme != "light" && *req.Theme != "dark") ||
		(req.Name != nil && !validName(*req.Name)) ||
		(req.Language != nil && *req.Language != "" && !languageTagRe.MatchString(*req.Language)) ||
		(req.Kind != nil && *req.Kind != "standard" && *req.Kind != "kids") {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	pr, err := p.Store.Profile(ctx, c.Account.ID, id)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	// A profile's own look is its own business; the rest is the owner's.
	ownLook := (req.Theme != nil || req.KidsTitles.Set) && req.Name == nil && req.Language == nil && req.Kind == nil &&
		req.MaxAge == nil && !req.Libraries.Set && !req.BlockedTags.Set && !req.Categories.Set &&
		c.Profile != nil && c.Profile.ID == pr.ID
	if !c.IsOwner() && !ownLook {
		problem.Send(w, problem.OwnerOnly, "")
		return
	}
	// Limits first: they can fail on the PIN, and nothing else should
	// change then.
	if req.Kind != nil || req.MaxAge != nil || req.Libraries.Set || req.BlockedTags.Set || req.Categories.Set {
		if !p.changeLimits(w, r, c, pr, req) {
			return
		}
	}
	if req.Name != nil {
		if err := p.Store.RenameProfile(ctx, c.Account.ID, id, *req.Name); errors.Is(err, store.ErrNameTaken) {
			problem.Send(w, problem.NameTaken, "")
			return
		} else if err != nil {
			problem.Send(w, problem.NotFound, "")
			return
		}
	}
	if req.Language != nil {
		if err := p.Store.SetProfileLanguage(ctx, c.Account.ID, id, *req.Language); err != nil {
			problem.Send(w, problem.NotFound, "")
			return
		}
	}
	if req.Theme != nil {
		if err := p.Store.SetProfileTheme(ctx, c.Account.ID, id, *req.Theme); err != nil {
			problem.Send(w, problem.NotFound, "")
			return
		}
	}
	if req.KidsTitles.Set {
		if err := p.Store.SetProfileKidsTitles(ctx, c.Account.ID, id, req.KidsTitles.Value); err != nil {
			problem.Send(w, problem.NotFound, "")
			return
		}
	}
	if pr, err = p.Store.Profile(ctx, c.Account.ID, id); err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	writeOK(w, toProfileJSON(pr))
}

// changeLimits makes a profile a kids profile, changes its age, or
// makes it a standard profile again, and changes the libraries it gets and
// the tags hidden from it. Only the owner can, with the owner's PIN if
// there is one. It writes the problem and returns false on failure.
// Overfin holds kids profiles to their age itself: Jellyfin isn't told.
func (p Profiles) changeLimits(w http.ResponseWriter, r *http.Request, c *Caller, pr *store.Profile, req profileRequest) bool {
	ctx := r.Context()
	if pr.Owner {
		problem.Send(w, problem.InvalidRequest, "the owner profile has no limits")
		return false
	}
	kind := pr.Kind
	if req.Kind != nil {
		kind = *req.Kind
	}
	age := pr.MaxAge
	if req.MaxAge != nil {
		age = req.MaxAge
	}
	if kind == "standard" {
		if req.MaxAge != nil {
			problem.Send(w, problem.InvalidRequest, "a standard profile has no age")
			return false
		}
		age = nil
	} else if age == nil {
		problem.Send(w, problem.InvalidRequest, "a kids profile needs an age")
		return false
	}
	libs, tags, ok, err := p.accessFrom(ctx, c, pr, req)
	if err != nil {
		p.Logger.Error("listing libraries failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return false
	} else if !ok {
		problem.Send(w, problem.InvalidRequest, "unknown library or invalid tag")
		return false
	}
	cats, ok, err := p.categoriesFrom(ctx, pr, req)
	if err != nil {
		p.Logger.Error("listing categories failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return false
	} else if !ok {
		problem.Send(w, problem.InvalidRequest, "unknown category")
		return false
	}
	if !p.checkPIN(w, ctx, c.Account.ID, c.Owner.ID, req.PIN) {
		return false
	}
	if err := p.Store.SetProfileCategories(ctx, c.Account.ID, pr.ID, cats); err != nil {
		problem.Send(w, problem.NotFound, "")
		return false
	}
	if err := p.Store.SetKind(ctx, c.Account.ID, pr.ID, kind, age); err != nil {
		problem.Send(w, problem.NotFound, "")
		return false
	}
	if err := p.Store.SetProfileAccess(ctx, c.Account.ID, pr.ID, libs, tags); err != nil {
		problem.Send(w, problem.NotFound, "")
		return false
	}
	if p.Images != nil {
		p.Images.Forget(pr.ID)
	}
	return true
}

// checkPIN checks a profile's PIN. It writes the problem and returns false
// when the PIN is missing, wrong or can't be tried yet.
func (p Profiles) checkPIN(w http.ResponseWriter, ctx context.Context, accountID, profileID, given string) bool {
	return checkPIN(w, ctx, p.PINs, p.Logger, accountID, profileID, given)
}

// checkPIN checks a profile's PIN with checker, writing the problem and
// returning false when it's missing, wrong, or can't be tried yet.
func checkPIN(w http.ResponseWriter, ctx context.Context, checker *pin.Checker, logger *slog.Logger, accountID, profileID, given string) bool {
	err := checker.Check(ctx, accountID, profileID, given)
	var wait *pin.WaitError
	switch {
	case err == nil:
		return true
	case errors.Is(err, pin.ErrRequired):
		problem.Send(w, problem.PINRequired, "")
	case errors.Is(err, pin.ErrIncorrect):
		problem.Send(w, problem.PINIncorrect, "")
	case errors.As(err, &wait):
		t := problem.RateLimited
		if wait.Locked {
			t = problem.PINLocked
		}
		pb := problem.New(t, "")
		pb.RetryAfter = int((wait.Wait + time.Second - 1) / time.Second)
		problem.Write(w, pb)
	case errors.Is(err, store.ErrNotFound):
		problem.Send(w, problem.NotFound, "")
	default:
		logger.Error("checking a PIN failed", "err", err)
		problem.Send(w, problem.Internal, "")
	}
	return false
}

// pinAllowed reports whether the caller may set a profile's PIN: the owner
// for any profile, a standard profile for itself. Kids profiles can't lock
// themselves.
func pinAllowed(c *Caller, pr *store.Profile) bool {
	return c.IsOwner() || (pr.ID == c.Profile.ID && c.Profile.Kind == "standard")
}

// setPIN gives a profile a PIN. Changing an existing one needs a fresh login
// (rule 21) instead of the owner's PIN.
func (p Profiles) setPIN(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PIN      string `json:"pin"`
		OwnerPIN string `json:"ownerPin"`
	}
	if !decode(r, &req) || !pin.Valid(req.PIN) {
		problem.Send(w, problem.InvalidRequest, "a PIN is 4 to 8 digits")
		return
	}
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	pr, err := p.Store.Profile(ctx, c.Account.ID, r.PathValue("id"))
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if !pinAllowed(c, pr) {
		problem.Send(w, problem.OwnerOnly, "")
		return
	}
	if pr.HasPIN {
		if !p.Sessions.RequireFresh(w, c) {
			return
		}
		if err := p.Store.ReplacePIN(ctx, c.Account.ID, pr.ID, p.PINs.Hash(pr.ID, req.PIN)); err != nil {
			problem.Send(w, problem.NotFound, "")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// The owner protecting someone else's profile proves it's the owner.
	if !pr.Owner && c.IsOwner() && !p.checkPIN(w, ctx, c.Account.ID, c.Owner.ID, req.OwnerPIN) {
		return
	}
	if err := p.Store.SetPIN(ctx, c.Account.ID, pr.ID, p.PINs.Hash(pr.ID, req.PIN)); errors.Is(err, store.ErrPINExists) {
		p.Sessions.RequireFresh(w, c) // set by someone else meanwhile
		return
	} else if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// removePIN needs a fresh login (rule 21).
func (p Profiles) removePIN(w http.ResponseWriter, r *http.Request) {
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	pr, err := p.Store.Profile(r.Context(), c.Account.ID, r.PathValue("id"))
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if !pinAllowed(c, pr) {
		problem.Send(w, problem.OwnerOnly, "")
		return
	}
	if !pr.HasPIN {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !p.Sessions.RequireFresh(w, c) {
		return
	}
	if err := p.Store.ReplacePIN(r.Context(), c.Account.ID, pr.ID, nil); err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// remove deletes a profile and, with its Jellyfin user, its watch history.
// Owner only, never the owner profile itself, and only after a fresh login.
func (p Profiles) remove(w http.ResponseWriter, r *http.Request) {
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	pr, err := p.Store.Profile(ctx, c.Account.ID, r.PathValue("id"))
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if !c.IsOwner() {
		problem.Send(w, problem.OwnerOnly, "")
		return
	}
	if pr.Owner {
		problem.Send(w, problem.InvalidRequest, "the owner profile goes with the account")
		return
	}
	if !p.Sessions.RequireFresh(w, c) {
		return
	}
	// Jellyfin first: if that fails, the profile stays and can be deleted
	// again, rather than leaving a history nobody can reach or erase.
	if err := p.Provisioner.DeleteJellyfinUser(ctx, pr.JellyfinUserID); err != nil {
		p.Logger.Error("deleting a profile's Jellyfin user failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	if err := p.Store.DeleteProfile(ctx, c.Account.ID, pr.ID); err != nil {
		p.Logger.Error("deleting a profile failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	if p.Images != nil {
		p.Images.Forget(pr.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// activate switches this session to a profile, with its PIN if it has one.
func (p Profiles) activate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PIN string `json:"pin"`
	}
	if r.ContentLength != 0 && !decode(r, &req) {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	c := p.Sessions.CurrentAccount(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	pr, err := p.Store.Profile(ctx, c.Account.ID, id)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if (c.Profile == nil || id != c.Profile.ID) && !p.checkPIN(w, ctx, c.Account.ID, id, req.PIN) {
		return
	}
	if err := p.Store.SetActiveProfile(ctx, c.Session.ID, c.Account.ID, id); err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	writeOK(w, toProfileJSON(pr))
}

// ---- Sessions ----------------------------------------------------------------------

type sessionJSONOut struct {
	ID           string `json:"id"`
	Current      bool   `json:"current"`
	Device       string `json:"device,omitempty"`
	ProfileName  string `json:"profileName,omitempty"`
	CreatedAt    string `json:"createdAt"`
	LastActiveAt string `json:"lastActiveAt,omitempty"`
}

// sessions lists sessions: all of the account's for the owner profile, and
// only those currently using their own profile for anyone else (rule 51).
func (p Profiles) sessions(w http.ResponseWriter, r *http.Request) {
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	all, err := p.Store.Sessions(ctx, c.Account.ID)
	if err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	names := map[string]string{}
	if c.IsOwner() {
		profiles, _ := p.Store.Profiles(ctx, c.Account.ID)
		for _, pr := range profiles {
			names[pr.ID] = pr.Name
		}
	}
	out := []sessionJSONOut{}
	for _, s := range all {
		active := s.ActiveProfileID
		if active == "" {
			active = c.Owner.ID
		}
		if !c.IsOwner() && active != c.Profile.ID {
			continue
		}
		out = append(out, sessionJSONOut{
			ID: s.ID, Current: s.ID == c.Session.ID, Device: s.Device, ProfileName: names[active],
			CreatedAt: s.Created.Format(time.RFC3339), LastActiveAt: s.LastActive.Format(time.RFC3339),
		})
	}
	writeOK(w, map[string]any{"items": out})
}

func (p Profiles) endSession(w http.ResponseWriter, r *http.Request) {
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	target, err := p.findSession(r, c, id)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if err := p.Store.DeleteSession(ctx, c.Account.ID, target.ID); err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if target.ID == c.Session.ID {
		clearCookie(w, r)
	}
	w.WriteHeader(http.StatusNoContent)
}

// findSession returns a session the caller may see: any of the account's
// for the owner, only those using the caller's profile otherwise.
func (p Profiles) findSession(r *http.Request, c *Caller, id string) (*store.Session, error) {
	all, err := p.Store.Sessions(r.Context(), c.Account.ID)
	if err != nil {
		return nil, err
	}
	for _, s := range all {
		active := s.ActiveProfileID
		if active == "" {
			active = c.Owner.ID
		}
		if s.ID == id && (c.IsOwner() || active == c.Profile.ID) {
			return s, nil
		}
	}
	return nil, store.ErrNotFound
}

func (p Profiles) endOtherSessions(w http.ResponseWriter, r *http.Request) {
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	if !c.IsOwner() {
		problem.Send(w, problem.OwnerOnly, "")
		return
	}
	ctx := r.Context()
	if err := p.Store.DeleteOtherSessions(ctx, c.Account.ID, c.Session.ID); err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	// The other devices' logins end too, or they'd start a new session at
	// their next request: signed out for real. This browser's stays.
	if ck, err := cookie.Get(r, LoginCookie); err == nil && len(ck.Value) <= 64 {
		if l, err := p.Store.LoginByToken(ctx, ck.Value); err == nil && l.Subject == c.Account.Subject {
			if err := p.Store.DeleteOtherLogins(ctx, l.Subject, l.ID); err != nil {
				problem.Send(w, problem.Internal, "")
				return
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// logout ends this browser's session and, with Overfin's own login, the
// login too. It works even before an account exists.
func (p Profiles) logout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if c, err := cookie.Get(r, CookieName); err == nil && len(c.Value) <= 64 {
		if s, err := p.Store.SessionByToken(ctx, c.Value); err == nil {
			_ = p.Store.DeleteSession(ctx, s.AccountID, s.ID)
		}
	}
	clearCookie(w, r)
	logoutURL := p.LogoutURL
	if in := p.Sessions.SignIn; in != nil {
		if leave := in.Logout(w, r); logoutURL == "" {
			logoutURL = leave
		}
	}
	out := map[string]string{}
	if logoutURL != "" {
		out["logoutUrl"] = logoutURL
	}
	writeOK(w, out)
}
