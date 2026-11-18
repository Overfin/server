package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/overfin/server/internal/lang"
)

// Languages are the languages the operator offers (regional tags, such as
// "fr-CA") and the default among them. Until the operator sets them, they
// come from TMDB's language and the ratings country the operator chose
// ("fr" in "CA" gives fr-CA), else English (United States). set is false
// until then.
func (s *Store) Languages(ctx context.Context) (offered []string, def string, set bool, err error) {
	var list, d []byte
	err = s.read.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'languages'`).Scan(&list)
	if err == nil {
		err = s.read.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'default-language'`).Scan(&d)
	}
	switch {
	case err == nil:
		return strings.Split(string(list), ","), string(d), true, nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, "", false, err
	}
	tmdb, err := s.TMDBLanguage(ctx)
	if err != nil {
		return nil, "", false, err
	}
	// The chosen one only: the server's country may come from these
	// languages (ServerCountry).
	country, _, err := s.ChosenRatingsCountry(ctx)
	if err != nil {
		return nil, "", false, err
	}
	def = lang.Region(tmdb, country)
	if def == "" {
		def = "en-US"
	}
	return []string{def}, def, false, nil
}

// SetLanguages saves the languages offered and the default, which must be
// one of them, the server's country left as it is. TMDB is asked in the
// default (its regional tag); when that changes, its data is fetched again
// (SetTMDBLanguage), and changed reports it. When the ratings country
// follows the server's country and that moves with them, titles' ages are
// worked out again.
func (s *Store) SetLanguages(ctx context.Context, offered []string, def string) (changed bool, err error) {
	return s.SetCountryLanguages(ctx, "", offered, def)
}

// SetCountryLanguages is SetLanguages, with the server's country saved too
// (ServerCountry); "" leaves it as it is.
func (s *Store) SetCountryLanguages(ctx context.Context, country string, offered []string, def string) (changed bool, err error) {
	old, err := s.TMDBLanguage(ctx)
	if err != nil {
		return false, err
	}
	oldCountry, err := s.RatingsCountry(ctx)
	if err != nil {
		return false, err
	}
	values := map[string]string{"languages": strings.Join(offered, ","), "default-language": def}
	if country != "" {
		values["server-country"] = country
	}
	for name, value := range values {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES (?, ?)
			ON CONFLICT (name) DO UPDATE SET value = excluded.value`, name, []byte(value)); err != nil {
			return false, err
		}
	}
	if err := s.refreshAgesIfMoved(ctx, oldCountry); err != nil {
		return false, err
	}
	if def != old {
		return true, s.SetTMDBLanguage(ctx, def)
	}
	return false, nil
}

// ServerCountry is the server's country (ISO 3166, such as "CA"), whose
// languages the control panel offers: the one the operator picked, else,
// on an install from before it was asked, the region of the default
// language ("fr-CA" gives "CA") when Overfin has languages for it
// (lang.CountryLanguages), else the first of the other languages' that
// has; "" when none does, or the languages aren't set yet.
func (s *Store) ServerCountry(ctx context.Context) (string, error) {
	var v []byte
	err := s.read.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'server-country'`).Scan(&v)
	if err == nil {
		return string(v), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	offered, def, set, err := s.Languages(ctx)
	if err != nil || !set {
		return "", err
	}
	for _, tag := range append([]string{def}, offered...) {
		_, region, _ := strings.Cut(tag, "-")
		if len(lang.CountryLanguages(region)) > 0 {
			return region, nil
		}
	}
	return "", nil
}

// SyncTMDBLanguage asks TMDB in the default language when it was asked in
// another (an install from before regional languages asked "fr" for
// fr-CA): its data is fetched again then, and changed says so.
func (s *Store) SyncTMDBLanguage(ctx context.Context) (changed bool, err error) {
	_, def, set, err := s.Languages(ctx)
	if err != nil || !set {
		return false, err
	}
	old, err := s.TMDBLanguage(ctx)
	if err != nil || old == def {
		return false, err
	}
	return true, s.SetTMDBLanguage(ctx, def)
}

// SetAccountLanguage changes an account's language.
func (s *Store) SetAccountLanguage(ctx context.Context, accountID, tag string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET language = ? WHERE id = ?`, tag, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// LanguageOf is the language an account uses: its own when it's still
// offered, else the default.
func (s *Store) LanguageOf(ctx context.Context, a *Account) (string, error) {
	offered, def, _, err := s.Languages(ctx)
	if err != nil {
		return "", err
	}
	if slices.Contains(offered, a.Language) {
		return a.Language, nil
	}
	return def, nil
}

// tmdbTokenContext binds the sealed TMDB token to its row.
const tmdbTokenContext = "tmdb token"

// TMDBToken is the operator's TMDB token, as the panel or the setup saved it;
// ok is false without one.
func (s *Store) TMDBToken(ctx context.Context) (token string, ok bool, err error) {
	var sealed []byte
	err = s.read.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'tmdb-token'`).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	k, err := s.box.Open(sealed, tmdbTokenContext)
	if err != nil {
		return "", false, fmt.Errorf("store: TMDB's token can't be read with this secret key: %w", err)
	}
	return string(k), true, nil
}

// SetTMDBToken saves the operator's TMDB token, sealed; "" forgets it.
func (s *Store) SetTMDBToken(ctx context.Context, token string) error {
	if token == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE name = 'tmdb-token'`)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('tmdb-token', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, s.box.Seal([]byte(token), tmdbTokenContext))
	return err
}
