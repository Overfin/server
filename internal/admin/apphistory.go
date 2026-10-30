package admin

import (
	"net/http"
	"strconv"
	"time"
)

// historyEventJSON is a film or a season that arrived in the libraries or
// left them; a season with its series' IDs.
type historyEventJSON struct {
	Event  string `json:"event"`
	Kind   string `json:"kind"`
	TMDB   string `json:"tmdbId,omitempty"`
	TVDB   string `json:"tvdbId,omitempty"`
	Season *int   `json:"season,omitempty"`
	At     string `json:"at"`
}

const (
	appHistoryLimit    = 500
	appHistoryMaxLimit = 1000
)

// appHistory (GET /apps/v1/history, history scope) lists when films and
// seasons arrived in the libraries and left them: since (RFC 3339, by when
// they happened; everything when left out), event (arrived or left; both
// when left out), in the order they were recorded, pages of limit (500,
// at most 1000), the next one at the cursor given. What arrived before
// Overfin kept this history is there, at the date Jellyfin added it.
func (p *Panel) appHistory(w http.ResponseWriter, r *http.Request) {
	if p.appKey(w, r, "history") == nil {
		return
	}
	q := r.URL.Query()
	var since time.Time
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			invalid(w, "since: an RFC 3339 time")
			return
		}
		since = t
	}
	event := q.Get("event")
	if event != "" && event != "arrived" && event != "left" {
		invalid(w, "event: arrived or left")
		return
	}
	limit := appHistoryLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > appHistoryMaxLimit {
			invalid(w, "limit: 1 to "+strconv.Itoa(appHistoryMaxLimit))
			return
		}
		limit = n
	}
	var after int64
	if v := q.Get("cursor"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			invalid(w, "cursor: as given in next")
			return
		}
		after = n
	}
	events, err := p.Store.TitleHistory(r.Context(), since, event, after, limit)
	if err != nil {
		p.fail(w, "reading the history", err)
		return
	}
	out := struct {
		Events []historyEventJSON `json:"events"`
		Next   string             `json:"next,omitempty"`
	}{Events: []historyEventJSON{}}
	for _, e := range events {
		out.Events = append(out.Events, historyEventJSON{Event: e.Event, Kind: e.Kind, TMDB: e.TMDB, TVDB: e.TVDB, Season: e.Season, At: e.At.Format(time.RFC3339)})
	}
	if len(events) == limit {
		out.Next = strconv.FormatInt(events[len(events)-1].ID, 10)
	}
	writeJSON(w, http.StatusOK, out)
}
