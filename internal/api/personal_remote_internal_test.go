package api

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/provision"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// A paired server's copy holding two episodes, watched to its end, marks
// both watched: the other one by what that server said its copy holds
// (MSFP video, section 4.2: with).
func TestRemoteCopyWithMarksBoth(t *testing.T) {
	ctx := context.Background()
	box, _ := seal.New(make([]byte, 32))
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	acct, _, err := st.CreateAccount(ctx, "sub", store.NewOwner{Name: "Main", JellyfinUserID: strings.Repeat("a", 32),
		JellyfinUsername: "u", JellyfinToken: "t"})
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := st.Profiles(ctx, acct.ID)
	if err != nil || len(profiles) == 0 {
		t.Fatal(err)
	}
	key, _ := hex.DecodeString(strings.Repeat("ab", 32))
	peer := msfp.PeerRecord{ID: "pb", Key: key, Name: "B", Address: "https://b.example.org", State: msfp.Paired,
		Inbound: msfp.InboundYes, Wants: []msfp.WantedLibrary{{ID: "L", Name: "Series", Wanted: true}}}
	first, second := "series/tmdb/1399/e63056", "series/tmdb/1399/e63057"
	if err := st.MSFP().Update(ctx, func(tx msfp.Tx) error {
		if err := tx.PutPeer(peer); err != nil {
			return err
		}
		return tx.PutPeerTitles(peer.ID, 1, []msfp.Title{&msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: first, Kind: "episode",
			Libraries: []string{"L"}}, With: []string{second}}})
	}); err != nil {
		t.Fatal(err)
	}
	pb := Playback{Library: Library{Provisioner: &provision.Provisioner{Store: st}}, Tickets: playback.NewTickets(time.Hour)}
	s := &playback.Stream{AccountID: acct.ID, ProfileID: profiles[0].ID, SessionID: "sess",
		Remote: playback.NewRemote(peer.ID, first, wire.Copy{DurationMs: 3_600_000}, msfp.RemotePlayback{ID: "play_" + strings.Repeat("a", 26)})}
	pb.Tickets.Add(s, 0, time.Now())
	pb.record(ctx, s, 3_600_000, true)
	states, err := st.PersonalStates(ctx, profiles[0].ID, []string{first, second})
	if err != nil || !states[first].Played || !states[second].Played {
		t.Fatalf("%+v %v", states, err)
	}
}
