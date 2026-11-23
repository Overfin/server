package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

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
}

// openDelivery plays a copy through the executor: plans it (refusals
// first), counts it (a stream; a transcode slot only when its video is
// converted), makes it, and registers what its stream serves. It
// answers w itself when it fails.
func (pb Playback) openDelivery(w http.ResponseWriter, r *http.Request, c store.IndexedCopy, req delivery.Request, a deliveryArgs) (*playback.Stream, bool) {
	ctx := r.Context()
	if pb.Delivery == nil {
		problem.Send(w, problem.NotPlayable, "")
		return nil, false
	}
	plan, err := pb.Delivery.Plan(ctx, c, req)
	if err != nil {
		pb.sendRefusal(w, err)
		return nil, false
	}
	play := a.auth
	play.DeviceID = pb.Tickets.ReserveDevice("overfin-play")
	s := &playback.Stream{AccountID: a.accountID, ProfileID: a.profileID, SessionID: a.sessionID, Device: a.device,
		ItemID: c.ItemID, SourceID: c.SourceID, Auth: play, Origin: a.origin,
		// Counted by what it will make, before anything starts.
		Delivery: &delivery.Started{Plan: plan}}
	replaced, err := pb.Tickets.Admit(s, plan.StartMs*10_000, time.Now(), a.lim, a.device)
	switch {
	case errors.Is(err, playback.ErrStreamLimit):
		pb.Tickets.ReleaseDevice(play.DeviceID)
		problem.Send(w, problem.StreamLimitReached, "")
		return nil, false
	case errors.Is(err, playback.ErrServerBusy):
		pb.Tickets.ReleaseDevice(play.DeviceID)
		problem.Send(w, problem.ServerBusy, "")
		return nil, false
	}
	for _, old := range replaced {
		pb.stopped(old, "taken-over")
	}
	started, err := pb.Delivery.Start(ctx, plan)
	if err != nil {
		pb.Tickets.Remove(s, "stopped", "", time.Now())
		pb.sendRefusal(w, err)
		return nil, false
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
			pb.Tickets.Remove(s, "stopped", "", time.Now())
			problem.Send(w, problem.NotPlayable, "")
			return nil, false
		}
		s.AddFiles(rw.Files)
		media = rw.Body
	}
	pb.Tickets.SetDelivery(s, started, media)
	pb.Logger.Info("playback started", "playback", s.ID, "delivery", plan.Kind, "method", plan.Method(),
		"videoConverted", plan.VideoConverted, "audioConverted", plan.AudioConverted)
	return s, true
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
		problem.Send(w, problem.ServerBusy, "")
	case rf.Status == http.StatusBadRequest:
		problem.Send(w, problem.InvalidRequest, "")
	default:
		// The closed code says why, for the player's information panel;
		// never free text from upstream.
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
