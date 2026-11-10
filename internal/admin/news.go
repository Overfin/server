package admin

import (
	"net/http"
	"strconv"
	"time"
)

// newsJSON is what happened since the panel last asked: accounts made, each
// with the name of the invitation it came from ("" for none). Now is when
// to ask from next time, by the server's clock.
type newsJSON struct {
	Now      int64             `json:"now"`
	Accounts []newsAccountJSON `json:"accounts"`
}

type newsAccountJSON struct {
	ID         string `json:"id"`
	Invitation string `json:"invitation,omitempty"`
}

// news (GET /admin/api/news?since=Unix seconds) lists the accounts made
// since then, for the panel open in a browser to say so and refresh what
// it shows. Without since, it lists nothing and tells the time to start
// from: what happened before the panel was opened isn't news. A new
// account's label can only be its invitation's name (nothing else labels
// an account before it exists).
func (p *Panel) news(w http.ResponseWriter, r *http.Request, _ *session) {
	now := p.clock()
	out := newsJSON{Now: now.Unix(), Accounts: []newsAccountJSON{}}
	raw := r.URL.Query().Get("since")
	if raw == "" {
		writeJSON(w, http.StatusOK, out)
		return
	}
	since, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || since <= 0 || since > now.Unix()+60 {
		invalid(w, "since: a time in Unix seconds")
		return
	}
	accts, err := p.Store.AccountsSince(r.Context(), time.Unix(since, 0))
	if err != nil {
		p.fail(w, "reading new accounts", err)
		return
	}
	for _, a := range accts {
		out.Accounts = append(out.Accounts, newsAccountJSON{ID: a.ID, Invitation: a.Label})
	}
	writeJSON(w, http.StatusOK, out)
}
