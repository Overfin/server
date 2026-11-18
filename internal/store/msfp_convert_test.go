package store

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/msfp"
	"github.com/overfin/server/msfp/wire"
)

// openPathAt opens a database at path, migrated up to version only, as an Overfin of
// then left it.
func openPathAt(t *testing.T, path string, box *seal.Box, version int) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	s := &Store{db: db, read: db, box: box, now: time.Now}
	names, _ := fs.Glob(migrations, "migrations/*.sql")
	sort.Strings(names)
	for i, name := range names[:version] {
		body, _ := migrations.ReadFile(name)
		if err := s.apply(ctx, name, string(body), i+1); err != nil {
			t.Fatal(err)
		}
	}
}

// A server federating before the engine (schema 128) keeps its identity
// and its pairings whole once its database moves to the engine's records
// (migrations 129 and 130): its keys (current, next and its commitment,
// old during a change of key), its secret of opaque IDs (the version IDs
// its peers know stay the same), its libraries' public IDs, its address
// and its move, and each peer with its state, keys, pins, versions,
// offers, wants, limits and titles; its codes and forgotten keys too.
func TestFederationConverted(t *testing.T) {
	ctx := context.Background()
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	path := filepath.Join(t.TempDir(), "old.db")
	openPathAt(t, path, box, 128)

	key := func(b byte) wire.Key {
		k, err := wire.KeyFromSeed(bytes.Repeat([]byte{b}, 32))
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	current, next, old := key(1), key(2), key(3)
	ids := bytes.Repeat([]byte{4}, 32)
	peerKey, peerPrevious, peerNext := key(5), key(6), key(7)
	rotatedAt := time.Unix(1_790_000_000, 0).UTC()
	movedAt := time.Unix(1_790_100_000, 0).UTC()

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	meta := func(name string, v []byte) { exec(`INSERT INTO meta (name, value) VALUES (?, ?)`, name, v) }
	meta("federation-key", box.Seal(current.Seed(), "federation key"))
	meta("federation-next-key", box.Seal(next.Seed(), "federation next key"))
	meta("federation-next-commitment", wire.Commitment(next.Public()))
	meta("federation-old-key", box.Seal(old.Seed(), "federation old key"))
	meta("federation-rotated-at", []byte(strconv.FormatInt(rotatedAt.Unix(), 10)))
	meta("federation-old-revoked", []byte("0"))
	meta("federation-ids-key", box.Seal(ids, "federation ids key"))
	meta("federation-address", []byte("https://new.example.org"))
	meta("federation-former-address", []byte("https://old.example.org"))
	meta("federation-moved-at", []byte(strconv.FormatInt(movedAt.Unix(), 10)))
	meta("federation-uncallable-address", []byte("https://msfp-aaaaaaaaaaaaaaaaaaaaaaaaaa.invalid"))
	exec(`INSERT INTO federation_libraries (library_id, public_id) VALUES ('films', 'lib_AAAAAAAAAAAAAAAAAAAAAA')`)
	versions, _ := json.Marshal(wire.Versions{Core: []int{1}, Parts: map[string][]int{"catalog": {1}, "playback": {1}, "media": {1}, "video": {1}}})
	pending, _ := json.Marshal(wire.Rotation{Old: peerKey.Public(), New: key(8).Public(), Date: 1_790_200_000})
	exec(`INSERT INTO peers (id, public_key, address, name, state, created_at, paired_at, seen_at, max_streams, max_transcodes,
		max_bitrate_kbps, their_streams, their_transcodes, their_bitrate_kbps, certificates, versions, next_commitment, commitment_from,
		previous_key, previous_until, pending_rotation, our_key, our_address, new_address, new_address_tried, inbound, titles_seq,
		offers_seq, commitment_told)
		VALUES ('pb', ?, 'https://b.example.org', 'B', 'paired', 1789000000, 1789000100, 1789000200, 3, 1, 8000, 2, 1, 0, 'pinA pinB',
			?, ?, 'rotation', ?, 1789999999, ?, 'old', 'old.example.org', 'https://b2.example.org', 1790300000, 'yes', 42, 40, 1)`,
		[]byte(peerKey.Public()), string(versions), wire.Commitment(peerNext.Public()), []byte(peerPrevious.Public()), string(pending))
	exec(`INSERT INTO peer_rotations (peer_id, old_key, new_key, next_commitment, date, old_signature, new_signature, at)
		VALUES ('pb', ?, ?, ?, 1788000000, x'01', x'02', 1788000001)`, []byte(peerPrevious.Public()), []byte(peerKey.Public()),
		wire.Commitment(peerNext.Public()))
	exec(`INSERT INTO peer_offers (peer_id, library_id) VALUES ('pb', 'films')`)
	exec(`INSERT INTO peer_wants (peer_id, library_id, name, kind, menu_id, label, offered) VALUES ('pb', 'lib_x', 'Films', 'movies', ?, 'Theirs', 1)`,
		MenuID("pb", "lib_x"))
	exec(`INSERT INTO peer_titles (peer_id, title_key, kind, library_id, audio, added_at, seen) VALUES ('pb', 'movie/tmdb/949', 'movie', 'lib_x', '["fr"]', 1, 5)`)
	exec(`INSERT INTO peer_sent (peer_id, title_key) VALUES ('pb', 'movie/tmdb/1')`)
	exec(`INSERT INTO remote_playbacks (peer_id, remote_id) VALUES ('pb', 'play_x')`)
	gone := sha256.Sum256([]byte("a key forgotten"))
	exec(`INSERT INTO forgotten_peers (key_hash, forgotten_at, revoked) VALUES (?, 1789500000, 0)`, gone[:])
	secret := bytes.Repeat([]byte{7}, 32)
	expires := time.Now().Add(time.Hour).Unix()
	exec(`INSERT INTO pairing_codes (id, secret_hash, secret, note, created_at, expires_at) VALUES ('c1', ?, ?, 'for Marc', 1789000000, ?)`,
		wire.SecretHash(secret), box.Seal(secret, "c1"), expires)
	db.Close()

	// The Overfin with the engine opens it.
	s, err := Open(ctx, path, box)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var rec *msfp.ServerRecord
	var peer *msfp.PeerRecord
	var code *msfp.CodeRecord
	var forgotten *msfp.ForgottenKey
	var libIDs map[string]string
	var sent []string
	var left []msfp.Leftover
	var titles []msfp.Title
	err = s.MSFP().Read(ctx, func(tx msfp.Tx) error {
		rec, _ = tx.Server()
		peer, _ = tx.Peer("pb")
		code, _ = tx.CodeByHash(wire.SecretHash(secret))
		forgotten, _ = tx.Forgotten(gone[:])
		libIDs, _ = tx.LibraryIDs()
		sent, _ = tx.Sent("pb", []string{"movie/tmdb/1", "movie/tmdb/2"})
		left, _ = tx.Leftovers()
		titles, _ = tx.PeerTitles("pb", "", 10)
		return nil
	})
	if err != nil || rec == nil || peer == nil || code == nil {
		t.Fatalf("records: %v %v %v %v", rec, peer, code, err)
	}
	// Resealed under the engine's contexts, with the same box.
	sealer := s.MSFP().(msfp.SealerProvider).Sealer()
	open := func(sealed []byte, context string) []byte {
		t.Helper()
		b, err := sealer.Open(sealed, context)
		if err != nil {
			t.Fatalf("%s: %v", context, err)
		}
		return b
	}
	if !bytes.Equal(open(rec.Current, "msfp current key"), current.Seed()) || !bytes.Equal(open(rec.Next, "msfp next key"), next.Seed()) ||
		!bytes.Equal(open(rec.Old, "msfp old key"), old.Seed()) || !bytes.Equal(open(rec.IDsSecret, "msfp ids secret"), ids) ||
		!bytes.Equal(open(code.Secret, "msfp code secret"), secret) {
		t.Fatal("a seed or secret changed")
	}
	if _, err := sealer.Open(rec.Current, "federation key"); err == nil {
		t.Fatal("the current key opens under its former context")
	}
	if !rec.OldSince.Equal(rotatedAt) || rec.OldRevoked || rec.Address != "https://new.example.org" || rec.Former != "https://old.example.org" ||
		!rec.MovedAt.Equal(movedAt) || rec.Uncallable || rec.UncallableAddress != "https://msfp-aaaaaaaaaaaaaaaaaaaaaaaaaa.invalid" {
		t.Fatalf("server record: %+v", rec)
	}
	if code.Note != "for Marc" || code.Expires.Unix() != expires || forgotten == nil || forgotten.At.Unix() != 1789500000 {
		t.Fatalf("code %+v, forgotten %+v", code, forgotten)
	}
	if libIDs["films"] != "lib_AAAAAAAAAAAAAAAAAAAAAA" || !slices.Equal(sent, []string{"movie/tmdb/1"}) ||
		len(left) != 1 || left[0] != (msfp.Leftover{Peer: "pb", PlaybackID: "play_x"}) || len(titles) != 1 {
		t.Fatalf("library IDs %v, sent %v, leftovers %v, titles %v", libIDs, sent, left, titles)
	}
	if !bytes.Equal(peer.Key, peerKey.Public()) || peer.State != msfp.Paired || !peer.WasPaired || peer.Name != "B" ||
		peer.Address != "https://b.example.org" || !bytes.Equal(peer.PreviousKey, peerPrevious.Public()) ||
		!bytes.Equal(peer.Commitment, wire.Commitment(peerNext.Public())) || peer.CommitmentFrom != "rotation" || !peer.CommitmentTold ||
		peer.Pending == nil || peer.Pending.Date != 1_790_200_000 || len(peer.Rotations) != 1 || peer.OurKey != msfp.KeyOld ||
		peer.OurAddress != "old.example.org" || peer.Move == nil || peer.Move.Address != "https://b2.example.org" ||
		!slices.Equal(peer.Pins, []string{"pinA", "pinB"}) || peer.Versions == nil || !slices.Equal(peer.Versions.Core, []int{1}) ||
		peer.Limits != (msfp.Limits{Streams: 3, Transcodes: 1, BitrateKbps: 8000}) || peer.TheirLimits == nil ||
		*peer.TheirLimits != (msfp.Limits{Streams: 2, Transcodes: 1}) || !slices.Equal(peer.Offers, []string{"films"}) || peer.OffersSeq != 40 ||
		len(peer.Wants) != 1 || !peer.Wants[0].Wanted || peer.Wants[0].Gone || peer.Inbound != msfp.InboundYes ||
		peer.CatalogRead.Seq != 0 || !slices.Equal(peer.CatalogRead.Wants, []string{"lib_x"}) {
		t.Fatalf("peer: %+v", peer)
	}
	// Its reading up to change 42 was converted, then forgotten by
	// 131_pictures.sql: its titles are read whole once, pictures and all.
	if labels, _ := s.PeerWantLabels(ctx, "pb"); labels["lib_x"] != "Theirs" {
		t.Fatalf("labels: %v", labels)
	}
	var left130 int
	s.read.QueryRowContext(ctx, `SELECT count(*) FROM meta WHERE name LIKE 'federation-%'`).Scan(&left130)
	if left130 != 0 {
		t.Fatalf("%d federation meta values left", left130)
	}

	// The engine opens it as the same server: its fingerprint, its next
	// key, its change of key and its move, the same peer.
	eng, err := msfp.Open(ctx, msfp.Config{Address: "https://new.example.org", Store: s.MSFP(),
		Parts: []string{"catalog", "playback", "media", "video"}, Pins: []string{"x"},
		Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	sv, err := eng.Server(ctx)
	if err != nil || sv.Fingerprint != wire.Fingerprint(current.Public()) || sv.NextKey != wire.CommitmentFingerprint(wire.Commitment(next.Public())) ||
		!sv.NextOnServer || !sv.RotationSince.Equal(rotatedAt) || sv.Former != "https://old.example.org" || sv.NotRotated != 1 {
		t.Fatalf("the engine's view: %+v %v", sv, err)
	}
	pv, err := eng.Peer(ctx, "pb")
	if err != nil || pv.Fingerprint != wire.Fingerprint(peerKey.Public()) || pv.State != msfp.Paired || pv.Core != 1 || pv.Offers != 1 || pv.Wants != 1 {
		t.Fatalf("the engine's peer: %+v %v", pv, err)
	}
	// Version IDs as Overfin made them: "ver_", HMAC of "version", the
	// peer and the copy's ID, with the same secret (the engine's own,
	// msfp.Engine.versionID, is this construction).
	m := hmac.New(sha256.New, ids)
	m.Write([]byte("version\npb\n" + fmt.Sprintf("%032x", 1)))
	before := "ver_" + wire.Encode64(m.Sum(nil)[:16])
	m = hmac.New(sha256.New, open(rec.IDsSecret, "msfp ids secret"))
	m.Write([]byte("version\npb\n" + fmt.Sprintf("%032x", 1)))
	if after := "ver_" + wire.Encode64(m.Sum(nil)[:16]); after != before {
		t.Fatalf("version ID %s, was %s", after, before)
	}
}

// A server that never federated has nothing to convert: the engine makes
// its keys at its first start.
func TestFederationNothingToConvert(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	err := s.MSFP().Read(ctx, func(tx msfp.Tx) error {
		r, err := tx.Server()
		if r != nil {
			t.Errorf("a server record: %+v", r)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := msfp.Open(ctx, msfp.Config{Address: "https://a.example.org", Store: s.MSFP(), Pins: []string{"x"},
		Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	if sv, err := eng.Server(ctx); err != nil || sv.Fingerprint == "" || !sv.NextOnServer {
		t.Fatalf("%+v %v", sv, err)
	}
}
