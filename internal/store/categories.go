package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/overfin/server/internal/ids"
)

// Category is a section of the site the operator makes from rules: the
// library's films and series that fit them. Nothing moves on disk.
type Category struct {
	ID string
	// Names are its name in each language (a tag's two letters: "en",
	// "fr").
	Names map[string]string
	Rules CategoryRules
	// Home makes it a row on the home page.
	Home bool
}

// CategoryRules say which titles are in a category: those that fit every
// rule given (none given: every film and series). Within a rule, any of
// its values fits.
type CategoryRules struct {
	Kinds     []string `json:"kinds,omitempty"`     // movie, series
	Genres    []int    `json:"genres,omitempty"`    // TMDB's genre IDs
	Languages []string `json:"languages,omitempty"` // original languages (ISO 639-1)
	Libraries []string `json:"libraries,omitempty"` // Jellyfin library IDs
	Tags      []string `json:"tags,omitempty"`      // Jellyfin tags, regardless of case
	// MaxAge: titles for that age at most (TitleAge); nil for no limit.
	MaxAge *int `json:"maxAge,omitempty"`
	// Except leaves out the titles of these categories (by ID), as their
	// own rules say.
	Except []string `json:"except,omitempty"`
}

// Name is the category's name in a language (a regional tag such as
// "fr-CA"), else in English, else in any.
func (c *Category) Name(lang string) string {
	if n := c.Names[strings.SplitN(lang, "-", 2)[0]]; n != "" {
		return n
	}
	if n := c.Names["en"]; n != "" {
		return n
	}
	keys := make([]string, 0, len(c.Names))
	for k := range c.Names {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if c.Names[k] != "" {
			return c.Names[k]
		}
	}
	return ""
}

// Categories lists the categories in the operator's order.
func (s *Store) Categories(ctx context.Context) ([]*Category, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT id, names, rules, home FROM categories ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Category
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CategoryByID returns a category, or ErrNotFound.
func (s *Store) CategoryByID(ctx context.Context, id string) (*Category, error) {
	c, err := scanCategory(s.read.QueryRowContext(ctx, `SELECT id, names, rules, home FROM categories WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func scanCategory(sc scanner) (*Category, error) {
	var c Category
	var names, rules string
	if err := sc.Scan(&c.ID, &names, &rules, &c.Home); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(names), &c.Names); err != nil {
		return nil, err
	}
	return &c, json.Unmarshal([]byte(rules), &c.Rules)
}

// SaveCategory saves a category: a new one (no ID yet) gets one and goes
// last.
func (s *Store) SaveCategory(ctx context.Context, c *Category) error {
	names, err := json.Marshal(c.Names)
	if err != nil {
		return err
	}
	rules, err := json.Marshal(c.Rules)
	if err != nil {
		return err
	}
	if c.ID == "" {
		c.ID = ids.New("cat")
		_, err = s.db.ExecContext(ctx, `INSERT INTO categories (id, position, names, rules, home)
			VALUES (?, coalesce((SELECT max(position) + 1 FROM categories), 0), ?, ?, ?)`, c.ID, string(names), string(rules), c.Home)
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE categories SET names = ?, rules = ?, home = ? WHERE id = ?`, string(names), string(rules), c.Home, c.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteCategory deletes a category; the others no longer leave its titles
// out.
func (s *Store) DeleteCategory(ctx context.Context, id string) error {
	all, err := s.Categories(ctx)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM categories WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	for _, c := range all {
		if c.ID == id || !slices.Contains(c.Rules.Except, id) {
			continue
		}
		c.Rules.Except = slices.DeleteFunc(c.Rules.Except, func(x string) bool { return x == id })
		rules, _ := json.Marshal(c.Rules)
		if _, err := tx.ExecContext(ctx, `UPDATE categories SET rules = ? WHERE id = ?`, string(rules), c.ID); err != nil {
			return err
		}
	}
	// Out of profiles' and templates' limits too: one limited to it alone
	// then sees nothing, until its limits are chosen again.
	for _, table := range []string{"profiles", "account_templates"} {
		if err := dropCategory(ctx, tx, table, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// OrderCategories puts the categories in this order (every one's ID).
func (s *Store) OrderCategories(ctx context.Context, order []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM categories`).Scan(&n); err != nil {
		return err
	}
	if n != len(order) {
		return ErrNotFound
	}
	// Each once: one given twice would leave another out.
	seen := map[string]bool{}
	for _, id := range order {
		if seen[id] {
			return ErrNotFound
		}
		seen[id] = true
	}
	for i, id := range order {
		res, err := tx.ExecContext(ctx, `UPDATE categories SET position = ? WHERE id = ?`, i, id)
		if err != nil {
			return err
		}
		if k, _ := res.RowsAffected(); k == 0 {
			return ErrNotFound
		}
	}
	return tx.Commit()
}

// LibrariesShown says whether the site's menu shows the libraries; until
// the operator says otherwise, it does.
func (s *Store) LibrariesShown(ctx context.Context) (bool, error) {
	var v []byte
	err := s.read.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'libraries-shown'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return string(v) != "off", err
}

// SetLibrariesShown saves it.
func (s *Store) SetLibrariesShown(ctx context.Context, on bool) error {
	v := "off"
	if on {
		v = "on"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('libraries-shown', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, []byte(v))
	return err
}

// CategoryFilter is a category as listings apply it: its rules, and
// those of the categories it leaves out.
type CategoryFilter struct {
	Rules  CategoryRules
	Except []CategoryRules
}

// CategoryFilterOf returns how listings apply a category.
func (s *Store) CategoryFilterOf(ctx context.Context, id string) (*CategoryFilter, error) {
	c, err := s.CategoryByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.FilterFor(ctx, c.Rules, c.ID)
}

// FilterFor returns how listings would apply rules, of the category self
// ("" for one not saved yet), for a preview.
func (s *Store) FilterFor(ctx context.Context, rules CategoryRules, self string) (*CategoryFilter, error) {
	f := &CategoryFilter{Rules: rules}
	for _, x := range rules.Except {
		if o, err := s.CategoryByID(ctx, x); err == nil && o.ID != self {
			f.Except = append(f.Except, o.Rules)
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	return f, nil
}

// LibraryLanguages lists the original languages of the library's films
// and series (ISO 639-1), the most common first.
func (s *Store) LibraryLanguages(ctx context.Context) ([]string, error) {
	return s.ids(ctx, `SELECT l FROM (SELECT t.language AS l, count(*) AS n
		FROM catalog_details_language t
		WHERE EXISTS (SELECT 1 FROM catalog_items i WHERE i.kind = t.kind AND i.tmdb = t.tmdb)
		GROUP BY l) WHERE l <> '' ORDER BY n DESC, l`)
}

// itemIn is the filter as SQL, for the catalog_items row named table.
func (f CategoryFilter) itemIn(table string) (string, []any) {
	cond, args := f.Rules.itemFits(table)
	for _, x := range f.Except {
		c, a := x.itemFits(table)
		cond, args = cond+" AND NOT "+c, append(args, a...)
	}
	return "(" + cond + ")", args
}

// itemFits is the rules as SQL, for the catalog_items row named table (a
// film or a series).
func (r CategoryRules) itemFits(table string) (string, []any) {
	conds := []string{table + ".kind IN ('movie', 'series')"}
	var args []any
	in := func(expr string, values []any) {
		conds = append(conds, expr+" IN ("+marks(len(values))+")")
		args = append(args, values...)
	}
	anyOf := func(values []string) []any {
		out := make([]any, len(values))
		for i, v := range values {
			out[i] = v
		}
		return out
	}
	if len(r.Kinds) > 0 {
		in(table+".kind", anyOf(r.Kinds))
	}
	if len(r.Libraries) > 0 {
		in(table+".library_id", anyOf(r.Libraries))
	}
	if len(r.Genres) > 0 {
		g := make([]any, len(r.Genres))
		for i, v := range r.Genres {
			g[i] = v
		}
		conds = append(conds, `EXISTS (SELECT 1 FROM catalog_title_genres g WHERE g.kind = `+table+`.kind AND g.tmdb = `+table+`.tmdb
			AND g.genre IN (`+marks(len(g))+`))`)
		args = append(args, g...)
	}
	if len(r.Languages) > 0 {
		conds = append(conds, `EXISTS (SELECT 1 FROM catalog_details_language l WHERE l.kind = `+table+`.kind AND l.tmdb = `+table+`.tmdb
			AND l.language IN (`+marks(len(r.Languages))+`))`)
		args = append(args, anyOf(r.Languages)...)
	}
	if len(r.Tags) > 0 {
		conds = append(conds, `EXISTS (SELECT 1 FROM catalog_tags t WHERE t.item_id = `+table+`.id
			AND fold(t.tag) IN (`+marks(len(r.Tags))+`))`)
		for _, t := range r.Tags {
			args = append(args, FoldTag(t))
		}
	}
	if r.MaxAge != nil {
		c, a := Ceiling{MaxAge: *r.MaxAge}.itemAllowed(table)
		conds, args = append(conds, c), append(args, a...)
	}
	return "(" + strings.Join(conds, " AND ") + ")", args
}

// CategorySample names the first titles (by name) a filter keeps, for the
// control panel's preview.
func (s *Store) CategorySample(ctx context.Context, f *CategoryFilter, n int) ([]string, error) {
	ids, err := s.ListCatalog(ctx, ListQuery{Sort: "name", Category: f, Limit: n})
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	return s.ids(ctx, `SELECT coalesce((SELECT t.name FROM catalog_titles t WHERE t.kind = i.kind AND t.tmdb = i.tmdb), i.name)
		FROM catalog_items i JOIN json_each(?) j ON j.value = i.id ORDER BY j.key`, jsonList(ids))
}

// dropCategory takes a category out of the categories lists of a table's
// rows.
func dropCategory(ctx context.Context, tx *sql.Tx, table, id string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, categories FROM `+table+` WHERE categories LIKE ?`, "%"+id+"%")
	if err != nil {
		return err
	}
	type row struct {
		id   string
		list []string
	}
	var found []row
	for rows.Next() {
		var r row
		var raw string
		if err := rows.Scan(&r.id, &raw); err != nil {
			rows.Close()
			return err
		}
		r.list = scanList(&raw)
		found = append(found, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range found {
		kept := slices.DeleteFunc(r.list, func(x string) bool { return x == id })
		if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET categories = ? WHERE id = ?`, jsonList(kept), r.id); err != nil {
			return err
		}
	}
	return nil
}

// TitleCategories returns, for TMDB titles ("movie/1", "series/2"), the
// categories each fits (their IDs, in the categories' order), by what
// TMDB says of it: kind, genres, original language, age. Rules on
// libraries and Jellyfin tags need the title in a library: a category with
// one of them fits none of these titles (Overswell, say, asks before
// adding them).
func (s *Store) TitleCategories(ctx context.Context, keys []string) (map[string][]string, error) {
	out := map[string][]string{}
	keys = plainKeys(keys)
	if len(keys) == 0 {
		return out, nil
	}
	categories, err := s.Categories(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range categories {
		f, err := s.FilterFor(ctx, c.Rules, c.ID)
		if err != nil {
			return nil, err
		}
		cond, args := f.itemIn("t")
		fits, err := s.ids(ctx, `SELECT t.kind || '/' || t.tmdb FROM (SELECT `+keyKind+` AS kind, `+keyTMDB+` AS tmdb,
				NULL AS id, NULL AS library_id, NULL AS series_id FROM json_each(?) j) t
			WHERE `+cond, append([]any{jsonList(keys)}, args...)...)
		if err != nil {
			return nil, err
		}
		for _, key := range fits {
			out[key] = append(out[key], c.ID)
		}
	}
	return out, nil
}
