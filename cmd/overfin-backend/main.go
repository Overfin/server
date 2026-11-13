// Command overfin-backend is Overfin's backend, the entrypoint of the
// overfin-backend image: Overfin with its data, next to the media, with
// its Jellyfin and Overfin's plugin for it beside it. It serves the site
// to its gateway over the link it opens to it, and the control panel on
// its own listener, for private addresses only.
//
// Pairing with a gateway, from the backend's machine:
//
//	docker exec <backend> /overfin-backend -pair <gateway's host:port> [-server-name <name>]
//
// prints a one-time code to set on the gateway (OVERFIN_PAIRING).
package main

import (
	"os"

	"github.com/overfin/server/internal/backend"
	"github.com/overfin/server/internal/images"
	overfin "github.com/overfin/server/overfin"
	web "github.com/overfin/web"
)

func main() {
	// An image worker: this same program, started by the backend to decode
	// images apart from it (internal/images).
	if images.RunWorker() {
		return
	}
	if code, ran := backend.Init(); ran {
		os.Exit(code)
	}
	overfin.RunBackend(web.Files)
}
