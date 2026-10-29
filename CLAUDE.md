# Overfin

Overfin is a platform: this Go server over a Jellyfin of its own
(required; TMDB, with the operator's own token, needed for now; Seerr optional) and
Overfin's own apps (overfin-web first, its own repository). The server holds everything that matters (people, rules,
limits); apps, official or not, use only its public API. MIT, public.

## Non-negotiable
- Follow every rule in SECURITY.md. Never add a passthrough or catch-all route;
  routes go through `plugin.Registrar`, which enforces this.
- Never copy or port code from other projects (Streamyfin, jellyfin-web, etc.).
  Build from the Jellyfin, Seerr and TMDB API specs and official docs.
- Standard library first. Any new dependency needs a stated reason.
- This repo is public. Never commit details of the maintainer's own setup:
  domains, IPs, hostnames, usernames, emails, network layout or secrets. Use
  `example.org` and documentation IPs (192.0.2.0/24, 198.51.100.0/24) in
  examples.
- AI assistance is always disclosed: keep the README's "AI-assisted
  development" section accurate, and end commits Claude contributed to with a
  `Co-Authored-By: Claude ...` trailer.
- Nobody reviews your code after you: the maintainer gives directions and
  uses the result, but doesn't read the code line by line. So check your
  own work: a test that fails without your change, read what you touch and
  what calls it, and say plainly what you didn't check.

## Style
- Plain, idiomatic Go. Clear code over clever code: code that is easy to read
  is code that can be reviewed, and review is part of security.
- `gofmt`, `go vet ./...` and `go test ./...` must pass, and
  `node --test sdk/js/overfin.test.js` for the JavaScript SDK.
- overfin-web (its own repository, required in go.mod) carries a copy of
  `sdk/js/overfin.js`; a test checks the bundled version matches, so after
  changing the SDK, update overfin-web and bump it in go.mod.
- After editing `api/openapi.yaml`, check it still parses:
  `python3 -c "import yaml; yaml.safe_load(open('api/openapi.yaml'))"`.
  A colon followed by a space inside a plain-text description breaks it.
- CI (`.github/workflows/ci.yml`) runs all of the above on every push.
- Layout: the package `overfin` (in `overfin/`) is Overfin (startup,
  gateway, every feature built in); `cmd/overfin` is the official build
  (Overfin plus overfin-web). No Go files at the repository's root. Some features (`internal/watchlist`, `internal/invites`,
  `internal/migration`) are written on the internal feature contract
  (`internal/feature`), each with its own database; there are no modules
  for others to add.

## Layout
- `cmd/overfin`: entry point
- `overfin`: Overfin itself (startup, the gateway, wiring every feature)
- `internal/server`: HTTP handler, middleware
- `internal/plugin`: plugin interface and the route registrar
