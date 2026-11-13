# Installing and running Overfin

Run it on the machine that has your films and series, or can reach them
through a mounted network share. It needs **Docker** with Compose.

Overfin runs as two containers from two images of the same version: its
**gateway** (`ghcr.io/overfin/overfin-gateway`), the only part the internet
reaches, and its **backend** (`ghcr.io/overfin/overfin-backend`), with
Overfin's data, its control panel, and a Jellyfin of its own with Overfin's
plugin, which it sets up and runs by itself. Each comes with a signed
attestation of how it was built, for x86 and ARM.
[How it's built, and why](architecture.md). A Jellyfin you already use
stays yours and untouched.

**1. Start it:**

```sh
mkdir overfin && cd overfin
curl -fsSLO https://raw.githubusercontent.com/overfin/server/v0.9.0/deploy/compose.yaml
curl -fsSL -o .env https://raw.githubusercontent.com/overfin/server/v0.9.0/deploy/.env.example
# in compose.yaml, change /srv/media to where your films and series are, then:
docker compose up -d
```

Your films and series are on the media line of `compose.yaml`
(`/srv/media:/media:ro`: the part before the colon is where they are on
this machine; they're only read). [.env](../deploy/.env.example) is
optional: `OVERFIN_DOMAIN` (see below), and your TMDB token as
`OVERFIN_TMDB_TOKEN`, or give it in the wizard. On Fedora, RHEL and other
SELinux systems, add `,z` after `:ro` on the media line, so the container
may read the folder.

Films and series in more than one place (another disk, a NAS share
mounted on the machine): add a line for each under the backend's `volumes`
in `compose.yaml`, then `docker compose up -d`. In the control panel, a
library's folders are then the paths after the colon:

```yaml
    volumes:
      - /srv/media:/media:ro
      - /mnt/nas/films:/nas/films:ro
```

**2. Set it up from your LAN.** Open `http://<the machine>:8081`. The setup
wizard asks, one step at a time: the default language, a TMDB token (free;
the wizard says how to get one; for now the site can't be used without
it), Intro Skipper (optional), Seerr (optional), a username and a password
for the control panel, and your email and a password, for you, the site's
first admin. The wizard and the control panel answer private addresses
only (your LAN, a VPN): never the internet, whatever the network around
them.

**3. Add your libraries** in the control panel (`http://<the machine>:8081`,
Libraries), with folders under `/media`.

That's all: the site is at `http://<the machine>`, or your domain. People
sign in with their email and a password. In the control panel: add people
(People: their email; Overfin emails them a setup link to choose their
password, or you copy it and hand it over), connect an email server
(Server, Email), make invitations (People, Registration: links anyone can
register with, confirming their email; they need the site's address too:
`OVERFIN_DOMAIN`, or `site.url` in the file below), choose your GPU
(Server, Transcoding; the compose file says how to give the container
one). Without an email server, there are no invitations or
forgotten-password emails: you add people yourself, and copy their setup
links (also for a forgotten password).

The backend keeps Overfin's secret key in `secrets` and everything else in
`data`: back both up, apart. Jellyfin's data is in `jellyfin-config`. The
gateway has nothing to back up. See [Backups](../deploy/README.md#backups).

**Jellyfin itself** is closed: nobody signs in to it, and it listens
inside the backend's container only. To look inside, open access in the
control panel (Server, Jellyfin): its own pages are then at
`http://<the machine>:8082` from your LAN, until you close access again.
Changes made there may break Overfin, or be undone by it.

**On two machines**, the gateway facing the internet (a VPS) and the
backend at home: see [On two machines](../deploy/README.md#on-two-machines).

## On the internet, with HTTPS

**The gateway can get its own certificate** from Let's Encrypt. Point
your domain at the machine, open ports 80 and 443, and set
`OVERFIN_DOMAIN` in `.env`: the compose file already publishes both. It
renews the certificate by itself, and sends plain-http visitors to HTTPS.
Behind a TCP proxy that passes TLS through by name (HAProxy, say), the
certificate still works: it's checked over 443 itself.

**Or behind your own reverse proxy**, which handles HTTPS and passes
everything to the gateway on port 8080. Tell the gateway the proxy's
address, so the visitor's address comes from it: `OVERFIN_TRUSTED_PROXIES`
for an HTTP proxy (its `X-Forwarded-For`), `OVERFIN_PROXY_PROTOCOL_FROM`
for a TCP proxy that sends the PROXY protocol. On the same machine, with
the gateway's ports published by Docker, the proxy's connections arrive
from the Docker network's own address (its gateway, such as 172.18.0.1),
not from 127.0.0.1: name that one, or give the network a fixed subnet in
the compose file so the address doesn't change. Tell the backend the site's
address (with an `https://` address, Overfin's cookies are HTTPS ones, and
it tells browsers to come back over HTTPS only), in
`/etc/overfin/overfin.toml` ([overfin.example.toml](../overfin.example.toml)
has every setting; with compose, add
`- ./overfin.toml:/etc/overfin/overfin.toml:ro` to the backend's `volumes`):

```toml
[site]
url = "https://media.example.org"
```

Caddy:

```caddyfile
media.example.org {
	reverse_proxy overfin-gateway:8080
}
```

nginx (inside your `server { listen 443 ssl; ... }` block):

```nginx
location / {
    client_max_body_size 10m;         # the panel's pictures
    proxy_pass http://overfin-gateway:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_buffering off;              # video streams
    proxy_read_timeout 1h;
}
```

Traefik (labels on the gateway's container):

```yaml
labels:
  - traefik.http.routers.overfin.rule=Host(`media.example.org`)
  - traefik.http.routers.overfin.tls.certresolver=letsencrypt
  - traefik.http.services.overfin.loadbalancer.server.port=8080
```

**Your own client instead?** Set `[site] serve` to its folder: Overfin
serves it the same way, with the Content-Security-Policy you give it
(`csp`), and `spa_fallback = true` for single-page apps. With
`serve = "none"`, Overfin only answers `/api/` (and its sign-in pages,
which your proxy passes on too), and your proxy serves the site, asking
Overfin's gate first (`forward_auth` to `/api/v1/gate`) to keep its pages
private.


---

[← Back to the README](../README.md)
