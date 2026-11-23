package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"time"

	"github.com/msfp-io/msfp-go/wire"
)

// IndexedCopy is a version of a film or an episode (a Jellyfin item and
// media source) as the copy index keeps it (catalog_copies): its MSFP
// copy's facts, read from the file by Overfin (package copies), or from
// Jellyfin's data while it isn't (Minimal).
type IndexedCopy struct {
	// CopyID is the copy's ID players and peers see; ItemID and SourceID,
	// Jellyfin's version, which never leave Overfin.
	CopyID, ItemID, SourceID string
	// Position is the copy's place among its item's (Jellyfin's order).
	Position    int
	Fingerprint string
	ETag        string
	// Size and Modified (Unix seconds) are the file's when it was read.
	Size, Modified int64
	// Facts are the copy without its ID, etag, previews and segments.
	Facts wire.Copy
	// Streams maps each MSFP track ID (a<n>, s<n>, "v") to Jellyfin's
	// stream index.
	Streams map[string]int
	// Externals is a hash of the subtitle files beside it, as Jellyfin
	// lists them.
	Externals string
	// Minimal: built from Jellyfin's data, never played as it is.
	Minimal bool
	// Parts are a stacked film's files; 1 for one file.
	Parts int
	// Languages are the file's languages as the catalog keeps them; nil
	// for a minimal copy.
	Languages *CopyLanguages
	// PreviewLayout is its seek-bar previews' layout, nil without.
	PreviewLayout *wire.PreviewLayout
	ProbeVersion  int
	ProbedAt      time.Time
}

// CopyLanguages are a file's languages as the catalog's titles keep them
// (CatalogItem's AudioLanguages, AudioTags, SubtitleTags).
type CopyLanguages struct {
	AudioLanguages []string `json:"audioLanguages,omitempty"`
	Audio          []string `json:"audio,omitempty"`
	Subtitles      []string `json:"subtitles,omitempty"`
}

// maxCopyLanguages is how many languages of each kind a title keeps from
// its copies, as from Jellyfin.
const maxCopyLanguages = 12

const copyColumns = `copy_id, item_id, source_id, position, fingerprint, etag, size, modified, facts,
	jellyfin_streams, externals, minimal, parts, languages, preview_layout, probe_version, coalesce(probed_at, 0)`

func scanCopy(rows interface{ Scan(...any) error }) (IndexedCopy, error) {
	var c IndexedCopy
	var facts, streams string
	var languages, layout sql.NullString
	var probed int64
	if err := rows.Scan(&c.CopyID, &c.ItemID, &c.SourceID, &c.Position, &c.Fingerprint, &c.ETag, &c.Size, &c.Modified,
		&facts, &streams, &c.Externals, &c.Minimal, &c.Parts, &languages, &layout, &c.ProbeVersion, &probed); err != nil {
		return IndexedCopy{}, err
	}
	if err := json.Unmarshal([]byte(facts), &c.Facts); err != nil {
		return IndexedCopy{}, err
	}
	if err := json.Unmarshal([]byte(streams), &c.Streams); err != nil {
		return IndexedCopy{}, err
	}
	if languages.Valid {
		c.Languages = &CopyLanguages{}
		if err := json.Unmarshal([]byte(languages.String), c.Languages); err != nil {
			return IndexedCopy{}, err
		}
	}
	if layout.Valid {
		c.PreviewLayout = &wire.PreviewLayout{}
		if err := json.Unmarshal([]byte(layout.String), c.PreviewLayout); err != nil {
			return IndexedCopy{}, err
		}
	}
	if probed > 0 {
		c.ProbedAt = time.Unix(probed, 0)
	}
	return c, nil
}

// CopyOf is a version's copy in the index; ok is false when it has none.
func (s *Store) CopyOf(ctx context.Context, itemID, sourceID string) (c IndexedCopy, ok bool, err error) {
	c, err = scanCopy(s.read.QueryRowContext(ctx, `SELECT `+copyColumns+` FROM catalog_copies
		WHERE item_id = ? AND source_id = ?`, itemID, sourceID))
	if errors.Is(err, sql.ErrNoRows) {
		return IndexedCopy{}, false, nil
	}
	return c, err == nil, err
}

// ItemCopies are an item's copies, in their order; an episode a file
// holds after its first has its file's (RealItem).
func (s *Store) ItemCopies(ctx context.Context, itemID string) ([]IndexedCopy, error) {
	itemID = s.RealItem(ctx, itemID)
	var out []IndexedCopy
	err := s.each(ctx, func(rows *sql.Rows) error {
		c, err := scanCopy(rows)
		if err == nil {
			out = append(out, c)
		}
		return err
	}, `SELECT `+copyColumns+` FROM catalog_copies WHERE item_id = ? ORDER BY position, copy_id`, itemID)
	return out, err
}

// CopySegments are a version's skip markers; nil when none were read.
func (s *Store) CopySegments(ctx context.Context, itemID, sourceID string) ([]wire.CopySegment, error) {
	var raw string
	err := s.read.QueryRowContext(ctx, `SELECT segments FROM copy_segments WHERE item_id = ? AND source_id = ?`,
		itemID, sourceID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []wire.CopySegment
	return out, json.Unmarshal([]byte(raw), &out)
}

// ErrNoItem is a copy of an item the index doesn't have (yet).
var ErrNoItem = errors.New("store: the index has no such item")

// PutCopy keeps a version's copy, in place of the one it had (its ID
// changes with its path); the copy's title changes when what's served of
// it does (served_hash), and the item's languages and picture come from
// its copies once one is read from its file.
func (s *Store) PutCopy(ctx context.Context, c IndexedCopy) error {
	facts := c.Facts
	facts.ID, facts.ETag, facts.PreviewLayout, facts.Segments = "", "", nil, nil
	factsJSON, err := json.Marshal(facts)
	if err != nil {
		return err
	}
	streams, err := json.Marshal(c.Streams)
	if err != nil {
		return err
	}
	var languages, layout any
	if c.Languages != nil && !c.Minimal {
		b, _ := json.Marshal(c.Languages)
		languages = string(b)
	}
	if c.PreviewLayout != nil {
		b, _ := json.Marshal(c.PreviewLayout)
		layout = string(b)
	}
	var probed any
	if !c.ProbedAt.IsZero() {
		probed = c.ProbedAt.Unix()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var one int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM catalog_items WHERE id = ?`, c.ItemID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return ErrNoItem
	} else if err != nil {
		return err
	}
	// Another ID for the same version (the file renamed or moved): the old
	// row goes, the new one comes, both noted as changes of the title.
	if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_copies WHERE item_id = ? AND source_id = ? AND copy_id <> ?`,
		c.ItemID, c.SourceID, c.CopyID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_copies (copy_id, item_id, source_id, position, fingerprint, etag,
			size, modified, duration_ms, facts, jellyfin_streams, externals, minimal, parts, languages, preview_layout,
			probe_version, probed_at, served_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')
		ON CONFLICT (copy_id) DO UPDATE SET item_id = excluded.item_id, source_id = excluded.source_id,
			position = excluded.position, fingerprint = excluded.fingerprint, etag = excluded.etag, size = excluded.size,
			modified = excluded.modified, duration_ms = excluded.duration_ms, facts = excluded.facts,
			jellyfin_streams = excluded.jellyfin_streams, externals = excluded.externals, minimal = excluded.minimal,
			parts = excluded.parts, languages = excluded.languages, preview_layout = excluded.preview_layout,
			probe_version = excluded.probe_version, probed_at = excluded.probed_at`,
		c.CopyID, c.ItemID, c.SourceID, c.Position, c.Fingerprint, c.ETag, c.Size, c.Modified, facts.DurationMs,
		string(factsJSON), string(streams), c.Externals, c.Minimal, max(c.Parts, 1), languages, layout,
		c.ProbeVersion, probed); err != nil {
		return err
	}
	if err := rehashCopies(ctx, tx, c.ItemID); err != nil {
		return err
	}
	if err := summarizeCopies(ctx, tx, c.ItemID); err != nil {
		return err
	}
	return tx.Commit()
}

// SetCopyLayout keeps a version's previews' layout (nil: none), when it
// changed.
func (s *Store) SetCopyLayout(ctx context.Context, itemID, sourceID string, l *wire.PreviewLayout) error {
	var layout any
	if l != nil {
		b, _ := json.Marshal(l)
		layout = string(b)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE catalog_copies SET preview_layout = ?
		WHERE item_id = ? AND source_id = ? AND preview_layout IS NOT ?`, layout, itemID, sourceID, layout)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	if err := rehashCopies(ctx, tx, itemID); err != nil {
		return err
	}
	return tx.Commit()
}

// ForgetIndexedCopies deletes the copies of versions not in seen (item ID, then
// source ID): their files left the libraries. The items' languages and
// pictures follow their remaining copies.
func (s *Store) ForgetIndexedCopies(ctx context.Context, seen map[[2]string]bool) error {
	var gone [][2]string
	err := s.each(ctx, func(rows *sql.Rows) error {
		var k [2]string
		if err := rows.Scan(&k[0], &k[1]); err != nil {
			return err
		}
		if !seen[k] {
			gone = append(gone, k)
		}
		return nil
	}, `SELECT item_id, source_id FROM catalog_copies`)
	if err != nil || len(gone) == 0 {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	items := map[string]bool{}
	for _, k := range gone {
		if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_copies WHERE item_id = ? AND source_id = ?`, k[0], k[1]); err != nil {
			return err
		}
		items[k[0]] = true
	}
	for item := range items {
		if err := summarizeCopies(ctx, tx, item); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// setCopySegments replaces an item's skip markers, by source, in tx, and
// the hashes of its copies as served.
func setCopySegments(ctx context.Context, tx *sql.Tx, itemID string, bySource map[string][]wire.CopySegment) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM copy_segments WHERE item_id = ?`, itemID); err != nil {
		return err
	}
	for source, segs := range bySource {
		if len(segs) == 0 {
			continue
		}
		b, err := json.Marshal(segs)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO copy_segments (item_id, source_id, segments) VALUES (?, ?, ?)`,
			itemID, source, string(b)); err != nil {
			return err
		}
	}
	return rehashCopies(ctx, tx, itemID)
}

// rehashCopies sets the hash of each of an item's copies as served: its
// ID, facts, etag, place, previews and segments; the trigger notes a
// change of its title when one changes.
func rehashCopies(ctx context.Context, tx *sql.Tx, itemID string) error {
	type row struct {
		id, hash string
	}
	var rows []row
	q, err := tx.QueryContext(ctx, `SELECT c.copy_id, c.facts, c.etag, c.position, coalesce(c.preview_layout, ''),
			coalesce(g.segments, ''), c.served_hash
		FROM catalog_copies c LEFT JOIN copy_segments g ON g.item_id = c.item_id AND g.source_id = c.source_id
		WHERE c.item_id = ?`, itemID)
	if err != nil {
		return err
	}
	for q.Next() {
		var id, facts, etag, layout, segments, old string
		var position int
		if err := q.Scan(&id, &facts, &etag, &position, &layout, &segments, &old); err != nil {
			q.Close()
			return err
		}
		h := sha256.New()
		for _, part := range []string{id, facts, etag, strconv.Itoa(position), layout, segments} {
			h.Write([]byte(strconv.Itoa(len(part)) + ":" + part + ";"))
		}
		if sum := hex.EncodeToString(h.Sum(nil))[:32]; sum != old {
			rows = append(rows, row{id, sum})
		}
	}
	q.Close()
	if err := q.Err(); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx, `UPDATE catalog_copies SET served_hash = ? WHERE copy_id = ?`, r.hash, r.id); err != nil {
			return err
		}
	}
	return nil
}

// summarizeCopies sets an item's languages and picture (and those of the
// episodes its file holds after its first) from its copies read from
// their files, once one is: their languages merged in the copies' order,
// the best picture. Without one, the item keeps Jellyfin's.
func summarizeCopies(ctx context.Context, tx *sql.Tx, itemID string) error {
	q, err := tx.QueryContext(ctx, `SELECT facts, languages FROM catalog_copies
		WHERE item_id = ? AND minimal = 0 AND languages IS NOT NULL ORDER BY position, copy_id`, itemID)
	if err != nil {
		return err
	}
	var all CopyLanguages
	var pictures []Picture
	read := 0
	for q.Next() {
		var facts, languages string
		if err := q.Scan(&facts, &languages); err != nil {
			q.Close()
			return err
		}
		var c wire.Copy
		var l CopyLanguages
		if json.Unmarshal([]byte(facts), &c) != nil || json.Unmarshal([]byte(languages), &l) != nil {
			continue
		}
		read++
		all.AudioLanguages = mergeLanguages(all.AudioLanguages, l.AudioLanguages)
		all.Audio = mergeLanguages(all.Audio, l.Audio)
		all.Subtitles = mergeLanguages(all.Subtitles, l.Subtitles)
		if p, ok := copyPicture(c); ok {
			pictures = append(pictures, p)
		}
	}
	q.Close()
	if err := q.Err(); err != nil || read == 0 {
		return err
	}
	var picture any
	if p, ok := BestPicture(pictures); ok {
		b, _ := json.Marshal(p)
		picture = string(b)
	}
	_, err = tx.ExecContext(ctx, `UPDATE catalog_items SET audio_languages = ?, audio_tags = ?, subtitle_tags = ?, picture = ?
		WHERE id = ? OR range_of = ?`, jsonList(all.AudioLanguages), jsonList(all.Audio), jsonList(all.Subtitles), picture,
		itemID, itemID)
	return err
}

// mergeLanguages adds to have those of more it lacks, up to
// maxCopyLanguages.
func mergeLanguages(have, more []string) []string {
	for _, l := range more {
		if l != "" && !slices.Contains(have, l) && len(have) < maxCopyLanguages {
			have = append(have, l)
		}
	}
	return have
}

// ---- Raw probes -----------------------------------------------------------------

// CopyProbe is what ffprobe said of a file (package copies keeps its
// outputs), kept by the file's fingerprint: never a path, never served.
type CopyProbe struct {
	Fingerprint  string
	ProbeVersion int
	MediaType    string
	Size         int64
	Raw          []byte
	// FramesPending: its HDR picture's first frames are still to read
	// (an imported probe made without them).
	FramesPending bool
}

// maxRawProbe bounds a kept probe, unpacked (the probe's own output cap).
const maxRawProbe = 16 << 20

// PutCopyProbe keeps a file's probe, gzipped.
func (s *Store) PutCopyProbe(ctx context.Context, p CopyProbe, now time.Time) error {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(p.Raw); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO copy_probes (fingerprint, probe_version, media_type, size, raw, at, frames_pending)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (fingerprint) DO UPDATE SET probe_version = excluded.probe_version,
		media_type = excluded.media_type, size = excluded.size, raw = excluded.raw, at = excluded.at,
		frames_pending = excluded.frames_pending`,
		p.Fingerprint, p.ProbeVersion, p.MediaType, p.Size, buf.Bytes(), now.Unix(), p.FramesPending)
	if err == nil {
		_, err = s.db.ExecContext(ctx, `DELETE FROM copy_probe_failed WHERE fingerprint = ?`, p.Fingerprint)
	}
	return err
}

// CopyProbeOf is a file's kept probe, unpacked; ok is false when none is.
func (s *Store) CopyProbeOf(ctx context.Context, fingerprint string) (p CopyProbe, ok bool, err error) {
	var packed []byte
	err = s.read.QueryRowContext(ctx, `SELECT probe_version, media_type, size, raw, frames_pending FROM copy_probes
		WHERE fingerprint = ?`, fingerprint).Scan(&p.ProbeVersion, &p.MediaType, &p.Size, &packed, &p.FramesPending)
	if errors.Is(err, sql.ErrNoRows) {
		return CopyProbe{}, false, nil
	}
	if err != nil {
		return CopyProbe{}, false, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		return CopyProbe{}, false, err
	}
	p.Raw, err = io.ReadAll(io.LimitReader(zr, maxRawProbe+1))
	if err != nil {
		return CopyProbe{}, false, err
	}
	if len(p.Raw) > maxRawProbe {
		return CopyProbe{}, false, errors.New("store: a kept probe is too big")
	}
	p.Fingerprint = fingerprint
	return p, true, nil
}

// CopyProbesPendingFrames are the fingerprints of the kept probes whose
// first frames are still to read (CopyProbe.FramesPending).
func (s *Store) CopyProbesPendingFrames(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	err := s.each(ctx, func(rows *sql.Rows) error {
		var fp string
		err := rows.Scan(&fp)
		out[fp] = true
		return err
	}, `SELECT fingerprint FROM copy_probes WHERE frames_pending = 1`)
	return out, err
}

// ForgetCopyProbes deletes the probes and failures of files no copy has,
// noted before: a probe kept for a file not indexed yet (an import) waits.
func (s *Store) ForgetCopyProbes(ctx context.Context, before time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM copy_probes
		WHERE at < ? AND fingerprint NOT IN (SELECT fingerprint FROM catalog_copies)`, before.Unix()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM copy_probe_failed
		WHERE at < ? AND fingerprint NOT IN (SELECT fingerprint FROM catalog_copies)`, before.Unix())
	return err
}

// CopyProbeFailed notes that probing a file failed now.
func (s *Store) CopyProbeFailed(ctx context.Context, fingerprint string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO copy_probe_failed (fingerprint, at) VALUES (?, ?)
		ON CONFLICT (fingerprint) DO UPDATE SET at = excluded.at`, fingerprint, now.Unix())
	return err
}

// CopyProbesFailedSince are the fingerprints whose probe failed since.
func (s *Store) CopyProbesFailedSince(ctx context.Context, since time.Time) (map[string]bool, error) {
	out := map[string]bool{}
	err := s.each(ctx, func(rows *sql.Rows) error {
		var fp string
		err := rows.Scan(&fp)
		out[fp] = true
		return err
	}, `SELECT fingerprint FROM copy_probe_failed WHERE at >= ?`, since.Unix())
	return out, err
}
