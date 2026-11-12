# Installing and running Overfin

Run it on the machine that has your films and series, or can reach them through a mounted network share. It needs **Docker** with Compose.
Overfin needs a **Jellyfin of its own**, and brings it: its image carries
its Jellyfin and Overfin's plugin for it, at versions made for each other.
The compose file runs both in one container; Overfin sets its Jellyfin up
by itself on its first start, and manages it from then on. A Jellyfin you
already use stays yours and untouched: see
[Your own Jellyfin](#your-own-jellyfin). Each release comes as an image
with a signed attestation of how it was built, for x86 and ARM (`ghcr.io/overfin/overfin`).

**1. Start it:**

```sh
mkdir overfin && cd overfin
curl -fsSLO https://raw.githubusercontent.com/overfin/server/v0.9.0/deploy/compose.yaml
curl -fsSL -o .env https://raw.githubusercontent.com/overfin/server/v0.9.0/deploy/.env.example
# in compose.yaml, change /srv/media to where your films and series are, then:
docker compose up -d
```

Your films and series are on the media line of `compose.yaml` (`/srv/media:/media:ro`: the part before
the colon is where they are on this machine; they're only read).
[.env](../deploy/.env.example) is optional: `OVERFIN_DOMAIN` (see below), and
your TMDB token as `OVERFIN_TMDB_TOKEN`, or give it in the wizard. On Fedora, RHEL and
other SELinux systems, add `,z` after `:ro` on the media line of
`compose.yaml`, so the container may read the folder.

Films and series in more than one place (another disk, a NAS share
mounted on the machine): add a line for each under `volumes`
in `compose.yaml`, then `docker compose up -d`. In the control panel, a
library's folders are then the paths after the colon:

```yaml
    volumes:
      - /srv/media:/media:ro
      - /mnt/nas/films:/nas/films:ro
```

**2. Set it up in your browser.** Open `http://<the machine>`. The setup
wizard first asks for a one-time code, so nobody else can set up your
server before you do; this prints it:

```sh
docker logs overfin | grep -o '"code":"[^"]*' | tail -1 | cut -d'"' -f4
```

("permission denied": put `sudo` in front of `docker`, as for any Docker
command when your user isn't in the `docker` group.)

Then, one step at a time: the default language, a TMDB token (free; the
wizard says how to get one; for now the site can't be used without it),
Intro Skipper (optional), Seerr (optional), and your email and a password,
for you, its first admin.

**3. Add your libraries** in the control panel (`/admin/`, Libraries), with
folders under `/media`, where the compose file puts your media folder.

That's all. People sign in with their email and a password. In the
control panel: add people (People: their email; Overfin emails them a
setup link to choose their password, or you copy it and hand it over),
connect an email server (Server,
Email), make invitations (People, Registration: links anyone can register
with, confirming their email; they need the site's address too: `OVERFIN_DOMAIN`,
or `site.url` in the file below), choose your GPU (Server, Transcoding; the
compose file says how to give the container one). Without an email server,
there are no invitations or forgotten-password emails: you add people
yourself, and copy their setup links (also for a forgotten password).

Overfin keeps its secret key in `overfin-secrets` and everything else in
`overfin-data`: back both up, apart. Jellyfin's data is in
`jellyfin-config`.

**Jellyfin itself** is closed: nobody signs in to it, and it listens
inside the container only. To look inside, open access in the control
panel (Server, Jellyfin), then follow
[Looking inside Jellyfin](../deploy/README.md#looking-inside-jellyfin).
Changes made there may break Overfin, or be undone by it.

## Your own Jellyfin

Overfin works only with the Jellyfin its image brings, at the version it's
made with, with Overfin's plugin: until then, every page says the server is
being updated, and the log says why. A Jellyfin you already use stays
yours: Overfin never touches it. Run Overfin's beside it, on the same media
folders.

Overfin's Jellyfin can be on another machine than Overfin (the one with
the media and the GPU, say): see
[Two machines](../deploy/README.md#two-machines). There too, Overfin sets a
fresh Jellyfin up itself; one already set up, it takes only if you hand it
over: the wizard asks for one of its administrators' login, used once,
shows what that server holds, and waits for you to confirm it's dedicated
to Overfin.

## On the internet, with HTTPS

**Overfin can get its own certificate** from Let's Encrypt. Point your
domain at the machine, open ports 80 and 443, and run it with your domain:

```sh
docker run -d --name overfin -p 80:8080 -p 443:8443 \
  -e OVERFIN_DOMAIN=media.example.org \
  -v overfin-data:/data -v overfin-secrets:/secrets \
  ghcr.io/overfin/overfin:0.9.0
```

With the compose file, set `OVERFIN_DOMAIN` in `.env` instead: it already
publishes ports 80 and 443.

It renews the certificate by itself, and sends plain-http visitors to
HTTPS. The setup code keeps a fresh install yours, but do the setup right
away.

**Or behind your own reverse proxy**, which handles HTTPS and passes
everything to Overfin on port 8080. Tell Overfin the site's address and the
proxy's (with an `https://` address, Overfin's cookies are HTTPS ones, and
it tells browsers to come back over HTTPS only), in `/etc/overfin/overfin.toml`
([overfin.example.toml](../overfin.example.toml) has every setting; with
compose, add `- ./overfin.toml:/etc/overfin/overfin.toml:ro` to Overfin's
`volumes`):

```toml
[site]
url = "https://media.example.org"

[identity]
trusted_proxies = ["198.51.100.7"]   # your reverse proxy: the client address comes from it
```

Caddy:

```caddyfile
media.example.org {
	reverse_proxy overfin:8080
}
```

nginx (inside your `server { listen 443 ssl; ... }` block):

```nginx
location / {
    client_max_body_size 10m;         # the panel's pictures
    proxy_pass http://overfin:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_buffering off;              # video streams
    proxy_read_timeout 1h;
}
```

Traefik (labels on Overfin's container):

```yaml
labels:
  - traefik.http.routers.overfin.rule=Host(`media.example.org`)
  - traefik.http.routers.overfin.tls.certresolver=letsencrypt
  - traefik.http.services.overfin.loadbalancer.server.port=8080
```

**Your own client instead?** Set `[site] serve` to its folder: Overfin
serves it the same way, with the Content-Security-Policy you give it
(`csp`), and `spa_fallback = true` for single-page apps. With
`serve = "none"`, Overfin only answers `/api/` (and, with its own sign-in,
`/admin/`, `/apps/` and its sign-in pages, which your proxy passes on too),
and your proxy serves the site, asking Overfin's gate first (`forward_auth` to `/api/v1/gate`) to keep
its pages private.


---

[← Back to the README](../README.md)
