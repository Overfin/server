# Development

```sh
go test ./...
node --test sdk/js/overfin.test.js
go run ./cmd/overfin -config overfin.toml
```

Your own build in the compose stack: `docker build -t overfin:dev .` in the
repository, then `OVERFIN_IMAGE=overfin:dev` in the .env beside compose.yaml and
`docker compose up -d`.

Go with the standard library and three dependencies (a TOML parser, a
pure-Go SQLite, and the Go team's golang.org/x/crypto for getting
certificates from Let's Encrypt), besides overfin-web, which the official
build bundles. CI runs formatting, vet, staticcheck, govulncheck, the tests
with the race detector, and a check that the API specification matches the
routes.


The control panel and the setup page are a Svelte app in `frontend/`, on
[Overfin UI](https://github.com/overfin/ui); `npm run check` and `npm run
build` there, and commit `internal/admin/dist`, which Go embeds.

---

[← Back to the README](../README.md)
