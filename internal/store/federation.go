package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// Paired servers' titles, as this server's pages read them beside its
// own (ROADMAP.md milestone 6): the MSFP engine reads them from each peer
// and keeps them in peer_titles (store/msfp.go); what follows joins them
// with this server's data. And the catalog's change log, which the
// database's triggers keep (title_changes).

// ownFirst orders heldTitles (as t) with the library's own titles first:
// what paired servers hold is fetched from TMDB once ours is, so a large
// peer index never holds our own library's pages back.
const ownFirst = `ORDER BY NOT EXISTS (SELECT 1 FROM catalog_items i WHERE i.kind = t.kind AND i.tmdb = t.tmdb)`

// heldTitles, in SQL, are the films and series this server can play or
// will: its library's, and those its paired servers hold for it (by TMDB
// ID), as (kind, tmdb) rows. TMDB's data is kept for all of them alike.
const heldTitles = `SELECT kind, tmdb FROM catalog_items WHERE kind IN ('movie', 'series') AND tmdb IS NOT NULL
	UNION SELECT p.kind, substr(p.title_key, length(p.kind) + 7) FROM peer_titles p
		WHERE p.kind IN ('movie', 'series') AND p.title_key LIKE p.kind || '/tmdb/%'
		AND EXISTS (SELECT 1 FROM peers e WHERE e.id = p.peer_id AND e.state = 'paired' AND e.suspended_at IS NULL)`

// MenuLibrary is a library taken from a paired server, as the site's
// menu shows it beside ours.
type MenuLibrary struct {
	// ID is its ID on this server's site, 32 hex digits as ours.
	ID string
	// Name is our operator's name for it, else the peer's: "Films (B)".
	Name string
	Kind string // movies, tvshows… as Jellyfin's
}

// MenuLibraries lists the libraries taken from paired servers that hold
// something for us, in the order they were paired, then by name.
func (s *Store) MenuLibraries(ctx context.Context) ([]MenuLibrary, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT w.menu_id, coalesce(w.label, w.name || ' (' || p.name || ')'), w.kind
		FROM peer_wants w JOIN peers p ON p.id = w.peer_id AND p.state = 'paired' AND p.suspended_at IS NULL
		WHERE w.menu_id IS NOT NULL
			AND EXISTS (SELECT 1 FROM peer_titles t WHERE t.peer_id = w.peer_id AND t.library_id = w.library_id)
		ORDER BY p.paired_at, p.id, w.name, w.library_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MenuLibrary{}
	for rows.Next() {
		var l MenuLibrary
		if err := rows.Scan(&l.ID, &l.Name, &l.Kind); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LabelPeerWant sets our operator's name for a wanted library's entry in
// the menu ("" for the one made of the peer's names).
func (s *Store) LabelPeerWant(ctx context.Context, peerID, libraryID, label string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE peer_wants SET label = nullif(?, '') WHERE peer_id = ? AND library_id = ?`,
		label, peerID, libraryID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PeerWantLabels are our operator's names for the menu entries of a
// peer's libraries we want, by the peer's library ID ("" for the one
// made of the peer's names).
func (s *Store) PeerWantLabels(ctx context.Context, peerID string) (map[string]string, error) {
	out := map[string]string{}
	err := s.each(ctx, func(rows *sql.Rows) error {
		var id, label string
		if err := rows.Scan(&id, &label); err != nil {
			return err
		}
		out[id] = label
		return nil
	}, `SELECT library_id, coalesce(label, '') FROM peer_wants WHERE peer_id = ?`, peerID)
	return out, err
}

// mergeTags adds a JSON list's tags to tags, each once.
func mergeTags(tags []string, list string) []string {
	var more []string
	json.Unmarshal([]byte(list), &more)
	for _, t := range more {
		if !slices.Contains(tags, t) {
			tags = append(tags, t)
		}
	}
	return tags
}

// PeerTitleCounts counts, per peer, the films and series we know it holds
// for us.
func (s *Store) PeerTitleCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT peer_id, count(*) FROM peer_titles WHERE kind IN ('movie', 'series') GROUP BY peer_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// PeerWith is what a peer said of one of its episodes' copies: the other
// episodes it holds (MSFP video, section 4.2); none for a title it
// doesn't hold for us.
func (s *Store) PeerWith(ctx context.Context, peerID, key string) ([]string, error) {
	var with sql.NullString
	err := s.read.QueryRowContext(ctx, `SELECT with_keys FROM peer_titles WHERE peer_id = ? AND title_key = ?`, peerID, key).Scan(&with)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return mergeTags(nil, with.String), err
}

// PeerSource is a paired server holding a title for us, with its copies'
// languages: its ID and name.
type PeerSource struct {
	Peer, Name string
	Audio      []string
	Subtitles  []string
	// Video is the picture it sent of the title (MSFP video section 3.2);
	// nil when it sent none.
	Video *Picture
}

// PeerSources lists the paired servers holding a film or an episode for
// us, in the order they were paired.
func (s *Store) PeerSources(ctx context.Context, key string) ([]PeerSource, error) {
	var out []PeerSource
	err := s.each(ctx, func(rows *sql.Rows) error {
		var ps PeerSource
		var audio, subs, video sql.NullString
		if err := rows.Scan(&ps.Peer, &ps.Name, &audio, &subs, &video); err != nil {
			return err
		}
		ps.Audio, ps.Subtitles = mergeTags(nil, audio.String), mergeTags(nil, subs.String)
		if p, ok := readPicture(video); ok {
			ps.Video = &p
		}
		out = append(out, ps)
		return nil
	}, `SELECT t.peer_id, p.name, t.audio, t.subtitles, t.video FROM peer_titles t
		JOIN peers p ON p.id = t.peer_id AND p.state = 'paired' AND p.suspended_at IS NULL
		WHERE t.title_key = ? AND t.kind IN ('movie', 'episode') ORDER BY p.paired_at, p.id`, key)
	return out, err
}

// ItemAudio is each item's audio languages, as tags.
func (s *Store) ItemAudio(ctx context.Context, items []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, id := range items {
		var audio sql.NullString
		err := s.read.QueryRowContext(ctx, `SELECT audio_tags FROM catalog_items WHERE id = ?`, id).Scan(&audio)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		out[id] = mergeTags(nil, audio.String)
	}
	return out, nil
}

// TitlePeers are the paired servers holding titles for us, by key, as
// TitleInfos gives them (Peers), for what needs nothing else of them.
func (s *Store) TitlePeers(ctx context.Context, keys []string) (map[string][]string, error) {
	out := map[string][]string{}
	if keys = plainKeys(keys); len(keys) == 0 {
		return out, nil
	}
	rows, err := s.read.QueryContext(ctx, `SELECT t.title_key, t.peer_id FROM peer_titles t
		JOIN (SELECT DISTINCT value FROM json_each(?)) j ON j.value = t.title_key
		JOIN peers p ON p.id = t.peer_id AND p.state = 'paired' AND p.suspended_at IS NULL ORDER BY t.title_key, p.paired_at, p.id`, jsonList(keys))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, id string
		if err := rows.Scan(&key, &id); err != nil {
			return nil, err
		}
		out[key] = append(out[key], id)
	}
	return out, rows.Err()
}

// changesHead is SQL for the number of the last change noted in the
// catalog (title_changes): 0 before the first. It holds once old changes
// are trimmed.
const changesHead = `SELECT coalesce((SELECT seq FROM sqlite_sequence WHERE name = 'title_changes'), 0)`

// TitleChanges says where the catalog's changes stand: head, the last
// noted; floor, the last one no longer kept (a peer that read up to floor
// or later can be told what changed since).
func (s *Store) TitleChanges(ctx context.Context) (head, floor int64, err error) {
	err = s.read.QueryRowContext(ctx, `SELECT (`+changesHead+`), coalesce((SELECT min(seq) - 1 FROM title_changes), (`+changesHead+`))`).
		Scan(&head, &floor)
	return head, floor, err
}

// TrimTitleChanges forgets the changes noted before a time: a peer that
// last read before them gets the whole list again.
func (s *Store) TrimTitleChanges(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM title_changes WHERE at < ?`, before.Unix())
	return err
}

// NewTitles are the titles the catalog changed after seq (come, gone or
// changed), at most limit, with the last change number read; head when
// since is negative: where the log stands now, nothing read.
func (s *Store) NewTitles(ctx context.Context, since int64, limit int) (keys []string, last int64, err error) {
	if since < 0 {
		head, _, err := s.TitleChanges(ctx)
		return nil, head, err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT seq, title_key FROM title_changes
		WHERE seq > ? AND title_key NOT LIKE 'item/%' ORDER BY seq LIMIT ?`, since, limit)
	if err != nil {
		return nil, since, err
	}
	defer rows.Close()
	last = since
	seen := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&last, &k); err != nil {
			return nil, since, err
		}
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	return keys, last, rows.Err()
}

// PeerName is a paired server's name, as it calls itself.
func (s *Store) PeerName(ctx context.Context, id string) (string, error) {
	var name string
	err := s.read.QueryRowContext(ctx, `SELECT name FROM peers WHERE id = ?`, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}
