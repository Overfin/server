package api

import (
	"net/http"

	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// apiVersion is the API's level, which apps compare to what they support.
// It isn't the software's version, which stays private.
const apiVersion = "1.0"

// Info tells an app or a site what this server is before anyone signs in:
// that it's an Overfin server, its name, and the features switched on.
type Info struct {
	Store  *store.Store
	Mounts []Mount
}

func (Info) Name() string { return "info" }

func (i Info) Register(r *plugin.Registrar) error {
	return r.HandlePublic("GET /api/v1/info", http.HandlerFunc(i.get))
}

type infoJSON struct {
	Name       string   `json:"name"`
	APIVersion string   `json:"apiVersion"`
	Features   []string `json:"features"`
}

func (i Info) get(w http.ResponseWriter, r *http.Request) {
	br, err := i.Store.Branding(r.Context())
	if err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	out := infoJSON{Name: BrandName(br), APIVersion: apiVersion, Features: []string{}}
	for _, m := range i.Mounts {
		out.Features = append(out.Features, m.Name())
	}
	writeOK(w, out)
}
