package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/msfp-io/msfp-go"
	"github.com/overfin/server/internal/delivery"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// Playbacks the executor serves (package delivery): a copy of the index
// played as MSFP's play asks, the file from disk or a remux or transcode
// by the companion plugin. Jellyfin has no session of them (rule 5): the
// plugin's delivery runs on a Jellyfin device of its own, kept alive by
// Overfin's pings and stopped with the playback.

// deliveryArgs are whose a delivery's playback is and under which limits.
type deliveryArgs struct {
	lim    playback.Limits
	device string
	// accountID, profileID and sessionID are those it counts for.
	accountID, profileID, sessionID string
	// auth is the Jellyfin account its segments are fetched as; origin,
	// for a paired server's viewer, its site.
	auth   jellyfin.Auth
	origin string
	// reason is why the player chose the delivery (playback.Reasons);
	// trickplay and previews, the copy's seek-bar previews Overfin made
	// (nil and "" without); creditsMs, where its final credits start.
	reason    string
	trickplay *jellyfin.Trickplay
	previews  string
	creditsMs int64
	// admit, for a paired server's playback, counts it by the pairing's
	// limits (msfp.StartRequest.Admit) before this server's own.
	admit func(method string) (msfp.Slot, error)
}

// openDelivery plays a copy through the executor: plans it (refusals
// first), counts it (a stream; a transcode slot only when its video is
// converted), makes it, and registers what its stream serves. It
// answers w itself when it fails.
func (pb Playback) openDelivery(w http.ResponseWriter, r *http.Request, c store.IndexedCopy, req delivery.Request, a deliveryArgs) (*playback.Stream, bool) {
	s, err := pb.startDelivery(r.Context(), c, req, a)
	switch {
	case err == nil:
		return s, true
	case errors.Is(err, playback.ErrStreamLimit):
		problem.Send(w, problem.StreamLimitReached, "")
	case errors.Is(err, playback.ErrServerBusy):
		problem.Send(w, problem.ServerBusy, "")
	case errors.Is(err, errNotPlayable):
		problem.Send(w, problem.NotPlayable, "")
	default:
		pb.sendRefusal(w, err)
	}
	return nil, false
}

// errNotPlayable is a copy the executor can't play at all.
var errNotPlayable = errors.New("api: not playable")

// startDelivery is openDelivery's work, its failures as errors: a
// delivery refused (*delivery.Refusal), a limit reached
// (playback.ErrStreamLimit, ErrServerBusy, or a paired server's, from
// its admit), errNotPlayable.
func (pb Playback) startDelivery(ctx context.Context, c store.IndexedCopy, req delivery.Request, a deliveryArgs) (*playback.Stream, error) {
	if pb.Delivery == nil {
		return nil, errNotPlayable
	}
	plan, err := pb.Delivery.Plan(ctx, c, req)
	if err != nil {
		return nil, err
	}
	play := a.auth
	play.DeviceID = pb.Tickets.ReserveDevice("overfin-play")
	s := &playback.Stream{AccountID: a.accountID, ProfileID: a.profileID, SessionID: a.sessionID, Device: a.device,
		ItemID: c.ItemID, SourceID: c.SourceID, Auth: play, Origin: a.origin,
		Reason: a.reason, Trickplay: a.trickplay, Previews: a.previews, CreditsMs: a.creditsMs,
		// Counted by what it will make, before anything starts.
		Delivery: &delivery.Started{Plan: plan}}
	// A paired server's: counted by its pairing's limits first (the
	// engine's), then by this server's (Tickets.Admit), released when
	// anything after refuses.
	release := func() {}
	if a.admit != nil {
		slot, err := a.admit(plan.Method())
		if err != nil {
			pb.Tickets.ReleaseDevice(play.DeviceID)
			return nil, err
		}
		release = slot.Release
	}
	replaced, err := pb.Tickets.Admit(s, plan.StartMs*10_000, time.Now(), a.lim, a.device)
	if err != nil {
		release()
		pb.Tickets.ReleaseDevice(play.DeviceID)
		return nil, err
	}
	for _, old := range replaced {
		pb.stopped(old, "taken-over")
	}
	started, err := pb.Delivery.Start(ctx, plan)
	if err != nil {
		release()
		pb.Tickets.Remove(s, "stopped", "", time.Now())
		return nil, err
	}
	var media []byte
	if started.Protocol == "hls" {
		// The segments' URLs stay here, behind neutral names (rule 14),
		// under the version's one fixed playlist path (rule 10).
		rw, err := playback.RewriteVariant(started.Made.Playlist, playback.Upstream{Path: "/Videos/" + c.ItemID + "/main.m3u8"})
		if err != nil {
			pb.Logger.Warn("refused a delivery's media playlist", "playback", s.ID, "err", err)
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			_ = pb.Delivery.Stop(stopCtx, started)
			release()
			pb.Tickets.Remove(s, "stopped", "", time.Now())
			return nil, errNotPlayable
		}
		s.AddFiles(rw.Files)
		media = rw.Body
	}
	pb.Tickets.SetDelivery(s, started, media)
	pb.Logger.Info("playback started", "playback", s.ID, "delivery", plan.Kind, "method", plan.Method(),
		"videoConverted", plan.VideoConverted, "audioConverted", plan.AudioConverted)
	return s, nil
}

// sendRefusal answers a refused delivery as MSFP does: 409
// delivery-refused, 503 server-busy; anything else, as any error.
func (pb Playback) sendRefusal(w http.ResponseWriter, err error) {
	rf := delivery.AsRefusal(err)
	switch {
	case rf == nil:
		pb.Logger.Error("a delivery failed", "err", err)
		problem.Send(w, problem.Internal, "")
	case rf.Status == http.StatusServiceUnavailable:
		pb.Logger.Info("a delivery refused: the server is busy", "why", rf.Why)
		problem.Send(w, problem.ServerBusy, "")
	case rf.Status == http.StatusBadRequest:
		problem.Send(w, problem.InvalidRequest, "")
	default:
		// The closed code says why, for the player's information panel
		// and the log (no title, no account); never free text from
		// upstream.
		pb.Logger.Info("a delivery refused", "why", rf.Why)
		problem.Send(w, problem.DeliveryRefused, rf.Why)
	}
}

// serveDelivery serves what a delivery's stream holds: the file from
// disk, Overfin's own master playlist, the media playlist, its segments.
func (s Stream) serveDelivery(w http.ResponseWriter, r *http.Request, st *playback.Stream, a jellyfin.Auth, name string) {
	d, pb := st.Delivery, s.Playback
	switch {
	case name == "file" && d.Protocol == "file":
		if pb.Delivery == nil {
			problem.Send(w, problem.NotFound, "")
			return
		}
		f, err := pb.Delivery.OpenFile(r.Context(), d)
		if err != nil {
			problem.Send(w, problem.NotFound, "")
			return
		}
		defer f.Close()
		// While it flows, the playback is in use: a film played straight
		// is often one long download.
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go func() {
			tick := time.NewTicker(30 * time.Second)
			defer tick.Stop()
			for {
				select {
				case now := <-tick.C:
					pb.Tickets.Touch(st, now)
				case <-ctx.Done():
					return
				}
			}
		}()
		delivery.ServeFile(w, r, d, f, st.Over())
	case d.Protocol != "hls":
		problem.Send(w, problem.NotFound, "")
	case name == "master.m3u8":
		writePlaylist(w, d.Master)
	case name == delivery.MediaPlaylist:
		writePlaylist(w, st.MediaPlaylist)
	default:
		up, ok := st.File(name)
		if !ok {
			problem.Send(w, problem.NotFound, "")
			return
		}
		contentType := "video/mp4"
		if strings.HasSuffix(name, ".ts") {
			contentType = "video/mp2t"
		}
		s.proxy(w, r, st, a, up, peerTicket(st), contentType)
	}
}

func writePlaylist(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}
