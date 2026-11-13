# Development

```sh
go test ./...
node --test sdk/js/overfin.test.js
mkdir -p dev && printf '[storage]\npath = "dev/overfin.db"\n' > dev/overfin.toml
# keep this key: the database can't be read without it
OVERFIN_SECRET_KEY=$(openssl rand -hex 32) go run ./cmd/overfin -config dev/overfin.toml
```

Your own build in the compose stack: in the repository,
`docker build -t overfin-gateway:dev .` and
`docker build -f backend/Dockerfile -t overfin-backend:dev .` (the same
`--build-arg VERSION=…` for both: the link refuses two versions), then
`OVERFIN_GATEWAY_IMAGE=overfin-gateway:dev` and
`OVERFIN_BACKEND_IMAGE=overfin-backend:dev` in the .env beside compose.yaml,
and `docker compose up -d`.

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
