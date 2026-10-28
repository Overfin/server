package store

import "context"

// The titles whose logos are checked again, once (migration 066,
// catalog.Logos).

// LogosToCheck returns up to limit films and series whose logo is still to
// be checked.
func (s *Store) LogosToCheck(ctx context.Context, limit int) ([]ArtworkTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, tmdb FROM logos_to_check ORDER BY kind, tmdb LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtworkTarget
	for rows.Next() {
		var t ArtworkTarget
		if err := rows.Scan(&t.Kind, &t.TMDB); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TextLanguages lists the languages a film's or a series' texts are kept
// in, besides the default's.
func (s *Store) TextLanguages(ctx context.Context, kind, tmdb string) ([]string, error) {
	return s.ids(ctx, `SELECT language FROM catalog_title_texts WHERE kind = ? AND tmdb = ? ORDER BY language`, kind, tmdb)
}

// SetCheckedLogo keeps a title's logos as checked, and takes it off the
// list, at once: logo is its logo in the default language ("" when TMDB
// has none: the one kept is deleted, and its name is shown instead);
// others, its logo in other languages its texts are kept in. Only logos
// already kept are changed: a title whose artwork was deleted meanwhile
// gets none back.
func (s *Store) SetCheckedLogo(ctx context.Context, t ArtworkTarget, logo string, others map[string]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if logo == "" {
		_, err = tx.ExecContext(ctx, `DELETE FROM artwork WHERE key = ? AND type = 'logo'`, t.Key())
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE artwork SET path = ? WHERE key = ? AND type = 'logo'`, logo, t.Key())
	}
	if err != nil {
		return err
	}
	for language, l := range others {
		if _, err := tx.ExecContext(ctx, `UPDATE catalog_title_texts SET logo = ? WHERE kind = ? AND tmdb = ? AND language = ?`,
			l, t.Kind, t.TMDB, language); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM logos_to_check WHERE kind = ? AND tmdb = ?`, t.Kind, t.TMDB); err != nil {
		return err
	}
	return tx.Commit()
}

// LogoChecked takes a title off the list, its logos as they are: TMDB
// doesn't know it any more, and the titles' keeper deals with that.
func (s *Store) LogoChecked(ctx context.Context, t ArtworkTarget) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM logos_to_check WHERE kind = ? AND tmdb = ?`, t.Kind, t.TMDB)
	return err
}
