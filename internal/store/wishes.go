package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"time"
)

// Wishlists (migration 078): each profile's titles to get, in its own
// order, and what Overfin needs to take turns between accounts.

// MaxWishes caps one profile's wishlist.
const MaxWishes = 200

var (
	// ErrWishlistFull: the profile has MaxWishes wishes already.
	ErrWishlistFull = errors.New("store: the wishlist is full")
	// ErrWishOrder: a new order isn't the wishlist's titles, each once.
	ErrWishOrder = errors.New("store: not the wishlist's titles")
)

// Wish is one of a profile's wished titles: a film or a season, by its
// key. A series is wished for season by season; its seasons stay together
// on the wishlist (GroupWishes).
type Wish struct {
	AccountID string
	ProfileID string
	Title     string
	// Position is the person's order, 1 first.
	Position int
	Added    time.Time
}

// Wishes lists a profile's wishes, in its order.
func (s *Store) Wishes(ctx context.Context, profileID string) ([]Wish, error) {
	return s.wishes(ctx, `WHERE w.profile_id = ?`, profileID)
}

// AllWishes lists every profile's wishes, but for profiles whose wishing
// is switched off: what the turns are taken among.
func (s *Store) AllWishes(ctx context.Context) ([]Wish, error) {
	return s.wishes(ctx, `WHERE w.profile_id NOT IN (SELECT profile_id FROM wish_off)`)
}

func (s *Store) wishes(ctx context.Context, where string, args ...any) ([]Wish, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT p.account_id, w.profile_id, w.title, w.position, w.added_at
		FROM wishes w JOIN profiles p ON p.id = w.profile_id `+where+` ORDER BY w.profile_id, w.position`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Wish
	for rows.Next() {
		var w Wish
		var added int64
		if err := rows.Scan(&w.AccountID, &w.ProfileID, &w.Title, &w.Position, &added); err != nil {
			return nil, err
		}
		w.Added = time.Unix(added, 0)
		out = append(out, w)
	}
	return out, rows.Err()
}

// AddWish puts a title at the end of a profile's wishlist; one already on
// it stays where it is.
func (s *Store) AddWish(ctx context.Context, profileID, title string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n, there int
	if err := tx.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE title = ?) FROM wishes WHERE profile_id = ?`,
		title, profileID).Scan(&n, &there); err != nil {
		return err
	}
	if there > 0 {
		return nil
	}
	if n >= MaxWishes {
		return ErrWishlistFull
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO wishes (profile_id, title, position, added_at) VALUES (?, ?, ?, ?)`,
		profileID, title, n+1, s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveWish takes a title off a profile's wishlist; the ones after it
// move up.
func (s *Store) RemoveWish(ctx context.Context, profileID, title string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var pos int
	err = tx.QueryRowContext(ctx, `DELETE FROM wishes WHERE profile_id = ? AND title = ? RETURNING position`, profileID, title).Scan(&pos)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE wishes SET position = position - 1 WHERE profile_id = ? AND position > ?`,
		profileID, pos); err != nil {
		return err
	}
	return tx.Commit()
}

// wishGroup is what a wish moves with on a wishlist: a season, its
// series' other seasons; a film, itself alone.
func wishGroup(title string) string {
	if r, ok := ParseTitleKey(title); ok && r.Kind == "season" {
		return r.Series().Key()
	}
	return title
}

// GroupWishes keeps a series' seasons together on a profile's wishlist,
// where its first one is, by season number; everything else keeps its
// place.
func (s *Store) GroupWishes(ctx context.Context, profileID string) error {
	list, err := s.Wishes(ctx, profileID)
	if err != nil {
		return err
	}
	titles := make([]string, len(list))
	for i, w := range list {
		titles[i] = w.Title
	}
	grouped := GroupedWishes(titles)
	if slices.Equal(grouped, titles) {
		return nil
	}
	return s.OrderWishes(ctx, profileID, grouped)
}

// GroupedWishes orders titles so that a series' seasons follow each other,
// where its first one is, by season number: a series moves as a whole,
// its seasons never on their own.
func GroupedWishes(titles []string) []string {
	var groups []string
	members := map[string][]string{}
	for _, t := range titles {
		g := wishGroup(t)
		if _, seen := members[g]; !seen {
			groups = append(groups, g)
		}
		members[g] = append(members[g], t)
	}
	out := make([]string, 0, len(titles))
	for _, g := range groups {
		seasons := members[g]
		slices.SortStableFunc(seasons, func(a, b string) int {
			ra, _ := ParseTitleKey(a)
			rb, _ := ParseTitleKey(b)
			return ra.Season - rb.Season
		})
		out = append(out, seasons...)
	}
	return out
}

// OrderWishes sets a profile's order: titles must be its wishlist's, each
// once.
func (s *Store) OrderWishes(ctx context.Context, profileID string, titles []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM wishes WHERE profile_id = ?`, profileID).Scan(&n); err != nil {
		return err
	}
	if n != len(titles) {
		return ErrWishOrder
	}
	seen := make(map[string]bool, len(titles))
	for i, title := range titles {
		if seen[title] {
			return ErrWishOrder
		}
		seen[title] = true
		res, err := tx.ExecContext(ctx, `UPDATE wishes SET position = ? WHERE profile_id = ? AND title = ?`, i+1, profileID, title)
		if err != nil {
			return err
		}
		if k, _ := res.RowsAffected(); k == 0 {
			return ErrWishOrder
		}
	}
	return tx.Commit()
}

// WishedTitles lists every title someone wished, once each.
func (s *Store) WishedTitles(ctx context.Context) ([]string, error) {
	return s.strings(ctx, `SELECT DISTINCT title FROM wishes ORDER BY title`)
}

func (s *Store) strings(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Wish states: what the fetcher last said of a title.
const (
	WishUnreleased  = "unreleased"
	WishNotFound    = "notfound"
	WishDownloading = "downloading"
	// WishRequested: handed to Seerr, which hasn't started it yet (its
	// approval, say).
	WishRequested = "requested"
)

// WishState is what the fetcher last said of a wished title. A title
// without one is waiting.
type WishState struct {
	Title string
	State string
	// ReleaseOn is when an unreleased title comes out (YYYY-MM-DD), if
	// known; NextTry, when one not found is tried again.
	ReleaseOn string
	NextTry   time.Time
	// SeerrRequest is its request in Seerr, when Overfin handed it there.
	SeerrRequest int
	Updated      time.Time
}

// WishStates returns every wished title's state, by title.
func (s *Store) WishStates(ctx context.Context) (map[string]WishState, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT title, state, coalesce(release_on, ''), coalesce(next_try, 0), coalesce(seerr_request, 0), updated_at FROM wish_states`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]WishState{}
	for rows.Next() {
		var st WishState
		var next, updated int64
		if err := rows.Scan(&st.Title, &st.State, &st.ReleaseOn, &next, &st.SeerrRequest, &updated); err != nil {
			return nil, err
		}
		if next > 0 {
			st.NextTry = time.Unix(next, 0)
		}
		st.Updated = time.Unix(updated, 0)
		out[st.Title] = st
	}
	return out, rows.Err()
}

// SetWishState keeps what the fetcher said of a title; an empty state
// makes it waiting again.
func (s *Store) SetWishState(ctx context.Context, st WishState) error {
	if st.State == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM wish_states WHERE title = ?`, st.Title)
		return err
	}
	var release, next, request any
	if st.ReleaseOn != "" {
		release = st.ReleaseOn
	}
	if !st.NextTry.IsZero() {
		next = st.NextTry.Unix()
	}
	if st.SeerrRequest > 0 {
		request = st.SeerrRequest
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO wish_states (title, state, release_on, next_try, seerr_request, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (title) DO UPDATE SET state = excluded.state, release_on = excluded.release_on,
			next_try = excluded.next_try, seerr_request = excluded.seerr_request, updated_at = excluded.updated_at`,
		st.Title, st.State, release, next, request, s.now().Unix())
	return err
}

// WishTurn is when an account last had its turn, and whose wish went.
type WishTurn struct {
	At        time.Time
	ProfileID string
}

// WishTurns returns every account's last turn, by account.
func (s *Store) WishTurns(ctx context.Context) (map[string]WishTurn, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT account_id, turn_at, coalesce(profile_id, '') FROM wish_turns`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]WishTurn{}
	for rows.Next() {
		var account string
		var at int64
		var t WishTurn
		if err := rows.Scan(&account, &at, &t.ProfileID); err != nil {
			return nil, err
		}
		t.At = time.Unix(0, at) // nanoseconds: several turns go in a second
		out[account] = t
	}
	return out, rows.Err()
}

// SpendWishTurn records that an account's turn went, on a profile's wish.
func (s *Store) SpendWishTurn(ctx context.Context, accountID, profileID string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO wish_turns (account_id, turn_at, profile_id) VALUES (?, ?, ?)
		ON CONFLICT (account_id) DO UPDATE SET turn_at = excluded.turn_at, profile_id = excluded.profile_id`,
		accountID, s.now().UnixNano(), profileID)
	return err
}

// SpendWishTurns spends the turn of every account that wished for a title,
// when it's fetched: each got it, so the others go next. An account whose
// profiles wished it spends one turn, on the profile whose wish is highest.
// It reports how many accounts did.
func (s *Store) SpendWishTurns(ctx context.Context, title string) (int, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT p.account_id, w.profile_id FROM wishes w JOIN profiles p ON p.id = w.profile_id
		WHERE w.title = ? ORDER BY p.account_id, w.position, w.profile_id`, title)
	if err != nil {
		return 0, err
	}
	var accounts, profiles []string
	for rows.Next() {
		var account, profile string
		if err := rows.Scan(&account, &profile); err != nil {
			rows.Close()
			return 0, err
		}
		if len(accounts) == 0 || accounts[len(accounts)-1] != account {
			accounts, profiles = append(accounts, account), append(profiles, profile)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for i := range accounts {
		if err := s.SpendWishTurn(ctx, accounts[i], profiles[i]); err != nil {
			return i, err
		}
	}
	return len(accounts), nil
}

// ArriveWish settles a title that arrived: it leaves every wishlist it
// was on, into each profile's arrivals, and its state goes. It returns
// those profiles.
func (s *Store) ArriveWish(ctx context.Context, title string) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT profile_id, position FROM wishes WHERE title = ?`, title)
	if err != nil {
		return nil, err
	}
	type spot struct {
		profile  string
		position int
	}
	var spots []spot
	for rows.Next() {
		var sp spot
		if err := rows.Scan(&sp.profile, &sp.position); err != nil {
			rows.Close()
			return nil, err
		}
		spots = append(spots, sp)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	now := s.now().Unix()
	var profiles []string
	for _, sp := range spots {
		if _, err := tx.ExecContext(ctx, `DELETE FROM wishes WHERE profile_id = ? AND title = ?`, sp.profile, title); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE wishes SET position = position - 1 WHERE profile_id = ? AND position > ?`,
			sp.profile, sp.position); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO wish_arrivals (profile_id, title, arrived_at) VALUES (?, ?, ?)
			ON CONFLICT DO UPDATE SET arrived_at = excluded.arrived_at, seen = 0`, sp.profile, title, now); err != nil {
			return nil, err
		}
		profiles = append(profiles, sp.profile)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM wish_states WHERE title = ?`, title); err != nil {
		return nil, err
	}
	return profiles, tx.Commit()
}

// WishArrival is a wished title that arrived for a profile.
type WishArrival struct {
	AccountID string
	ProfileID string
	Title     string
	At        time.Time
}

// UnseenArrivals lists what arrived for a profile that it wasn't told of
// yet, the latest first.
func (s *Store) UnseenArrivals(ctx context.Context, profileID string) ([]string, error) {
	return s.strings(ctx, `SELECT title FROM wish_arrivals WHERE profile_id = ? AND seen = 0 ORDER BY arrived_at DESC, title`, profileID)
}

// SeeArrivals marks what arrived for a profile as told.
func (s *Store) SeeArrivals(ctx context.Context, profileID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE wish_arrivals SET seen = 1 WHERE profile_id = ?`, profileID)
	return err
}

// Arrivals lists what arrived since a time, for the score.
func (s *Store) Arrivals(ctx context.Context, since time.Time) ([]WishArrival, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT p.account_id, a.profile_id, a.title, a.arrived_at
		FROM wish_arrivals a JOIN profiles p ON p.id = a.profile_id WHERE a.arrived_at >= ?`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WishArrival
	for rows.Next() {
		var a WishArrival
		var at int64
		if err := rows.Scan(&a.AccountID, &a.ProfileID, &a.Title, &at); err != nil {
			return nil, err
		}
		a.At = time.Unix(at, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ForgetArrivals deletes arrivals older than a time: they no longer weigh.
func (s *Store) ForgetArrivals(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM wish_arrivals WHERE arrived_at < ?`, before.Unix())
	return err
}

// WishesOff reports whether a profile's owner switched its wishing off.
func (s *Store) WishesOff(ctx context.Context, profileID string) (bool, error) {
	var one int
	err := s.read.QueryRowContext(ctx, `SELECT 1 FROM wish_off WHERE profile_id = ?`, profileID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// SetWishesOff switches a profile's wishing off or back on; its wishes
// are kept either way, and wait while it's off.
func (s *Store) SetWishesOff(ctx context.Context, profileID string, off bool) error {
	var err error
	if off {
		_, err = s.db.ExecContext(ctx, `INSERT INTO wish_off (profile_id) VALUES (?) ON CONFLICT DO NOTHING`, profileID)
	} else {
		_, err = s.db.ExecContext(ctx, `DELETE FROM wish_off WHERE profile_id = ?`, profileID)
	}
	return err
}

// RecordWishSend notes that a wished title was handed to Seerr now.
func (s *Store) RecordWishSend(ctx context.Context, title string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO wish_sends (title, sent_at) VALUES (?, ?)`, title, s.now().Unix())
	return err
}

// WishSendsSince counts the wishes handed to Seerr since a time; older
// sends are forgotten.
func (s *Store) WishSendsSince(ctx context.Context, since time.Time) (int, error) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM wish_sends WHERE sent_at < ?`, since.Unix()); err != nil {
		return 0, err
	}
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM wish_sends`).Scan(&n)
	return n, err
}

// SeerrWishUser returns the Seerr user Overfin hands wishes over as; ok is
// false when it has none yet.
func (s *Store) SeerrWishUser(ctx context.Context) (id int, ok bool, err error) {
	var v string
	err = s.read.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'seerr-wish-user'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	id, err = strconv.Atoi(v)
	return id, err == nil, err
}

// SetSeerrWishUser keeps the Seerr user Overfin hands wishes over as.
func (s *Store) SetSeerrWishUser(ctx context.Context, id int) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('seerr-wish-user', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, []byte(strconv.Itoa(id)))
	return err
}
