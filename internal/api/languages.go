package api

import (
	"net/http"

	"github.com/overfin/server/internal/lang"
)

// languages are every language Overfin names, each named in itself, for
// choosing one (a profile's audio or subtitles): the same names the
// player's tracks have.
func (l Library) languages(w http.ResponseWriter, r *http.Request) {
	if l.active(w, r) == nil {
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	writeOK(w, struct {
		Languages []lang.Choice `json:"languages"`
	}{lang.List()})
}
