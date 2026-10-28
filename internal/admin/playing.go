package admin

import (
	"errors"
	"net/http"
	"time"

	"github.com/overfin/server/internal/connector"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/store"
)

// Now playing: the live view. Who's watching what, how it's served, and a
// way to stop it. Read from memory when the page asks, never kept (rule 40).

// Playing gives the playbacks running now, and stops one.
type Playing interface {
	// NowPlaying lists the live playbacks, oldest first, and the limit on
	// conversions running at once (0: none).
	NowPlaying() (live []playback.Live, maxTranscodes int)
	// EndPlayback stops a playback; its player says an administrator
	// stopped it. False when it had already ended.
	EndPlayback(id string) bool
}

type playingJSON struct {
	ID      string `json:"id"`
	Account struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"account"`
	Profile     string   `json:"profile,omitempty"`
	Device      string   `json:"device,omitempty"`
	Title       string   `json:"title,omitempty"`
	Method      string   `json:"method"`
	Reasons     []string `json:"reasons,omitempty"`
	Height      int      `json:"height,omitempty"`
	BitrateKbps int      `json:"bitrateKbps,omitempty"`
	PositionMs  int64    `json:"positionMs"`
	DurationMs  int64    `json:"durationMs,omitempty"`
	Paused      bool     `json:"paused"`
	StartedAt   string   `json:"startedAt"`
}

func (p *Panel) playing(w http.ResponseWriter, r *http.Request, _ *session) {
	ctx := r.Context()
	live, maxTranscodes := p.Playing.NowPlaying()
	var people map[string]connector.Person
	if p.Directory != nil && len(live) > 0 {
		people, _ = p.Directory.People(ctx) // names are a bonus: without them, labels
	}
	names := map[string]string{}    // account names, by ID
	profiles := map[string]string{} // profile names, by ID
	out := []playingJSON{}
	transcodes := 0
	for _, l := range live {
		s := l.Stream
		if _, ok := names[s.AccountID]; !ok {
			a, err := p.Store.AccountByID(ctx, s.AccountID)
			if errors.Is(err, store.ErrNotFound) {
				a = &store.Account{Label: "Deleted account"} // its playback ends shortly
			} else if err != nil {
				p.fail(w, "listing what's playing", err)
				return
			}
			prs, err := p.Store.Profiles(ctx, s.AccountID)
			if err != nil {
				p.fail(w, "listing what's playing", err)
				return
			}
			owner := ""
			for _, pr := range prs {
				profiles[pr.ID] = pr.Name
				if pr.Owner {
					owner = pr.Name
				}
			}
			names[s.AccountID] = accountName(people[a.Subject], a.Label, owner)
		}
		pj := playingJSON{ID: s.ID, Profile: profiles[s.ProfileID], Device: s.Device, Title: l.Title, Method: l.Method,
			Reasons: s.Decision.Reasons(), BitrateKbps: s.Decision.Source.Bitrate / 1000, PositionMs: l.PositionMs,
			DurationMs: s.Decision.Source.RunTimeTicks / 10_000, Paused: l.Paused, StartedAt: l.Started.Format(time.RFC3339)}
		pj.Account.ID, pj.Account.Name = s.AccountID, names[s.AccountID]
		for _, ms := range s.Decision.Source.MediaStreams {
			if ms.Type == "Video" {
				pj.Height = ms.Height
				break
			}
		}
		if l.Method == "transcode" {
			transcodes++
		}
		out = append(out, pj)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "transcodes": transcodes, "maxTranscodes": maxTranscodes})
}

// accountName is what the panel calls an account: the person's name at the
// identity provider, else the operator's label, else its owner profile's.
func accountName(person connector.Person, label, owner string) string {
	for _, n := range []string{person.Name, person.Username, label, owner} {
		if n != "" {
			return n
		}
	}
	return "Unnamed"
}

func (p *Panel) stopPlaying(w http.ResponseWriter, r *http.Request, s *session) {
	id := r.PathValue("id")
	if !p.Playing.EndPlayback(id) {
		notFound(w, "That playback has already ended")
		return
	}
	p.Logger.Info("a playback was stopped in the control panel", "playback", id, "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}
