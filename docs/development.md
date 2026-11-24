# Development

```sh
go test ./...
# before a release too: the link's long tests (a gateway restarted five
# times, 8 GiB through the link), a few minutes
OVERFIN_LONG_TESTS=1 go test ./internal/link
node --test sdk/js/overfin.test.js
mkdir -p dev && printf '[storage]\npath = "dev/overfin.db"\n' > dev/overfin.toml
# keep this key: the database can't be read without it
OVERFIN_SECRET_KEY=$(openssl rand -hex 32) go run ./cmd/overfin -config dev/overfin.toml
```

Your own build in the compose stack: in the repository,
`docker build -t overfin-gateway:dev .` and
`docker build -f backend/Dockerfile -t overfin-backend:dev .` (the same
`--build-arg VERSION=…` for both: the backend and its gateways refuse two
versions), then
`OVERFIN_GATEWAY_IMAGE=overfin-gateway:dev` and
`OVERFIN_BACKEND_IMAGE=overfin-backend:dev` in the .env beside compose.yaml,
and `docker compose up -d`.

Go with the standard library and four dependencies (a TOML parser, a
pure-Go SQLite, the Go team's golang.org/x/crypto for getting
certificates from Let's Encrypt, and [msfp-go](https://github.com/msfp-io/msfp-go),
MSFP's engine, which brings filippo.io/edwards25519, the group arithmetic
inside the standard library's Ed25519, for MSFP's strict key and signature
checks), besides overfin-web, which the official build bundles. CI runs formatting, vet, staticcheck, govulncheck, the tests
with the race detector, and a check that the API specification matches the
routes.


The control panel and the setup page are a Svelte app in `frontend/`, on
[Overfin UI](https://github.com/overfin/ui); `npm run check` and `npm run
build` there, and commit `internal/admin/dist`, which Go embeds.

---

[← Back to the README](../README.md)
