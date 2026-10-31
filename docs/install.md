# Installing and running Overfin

You need **Docker** with Compose, and your media in a folder this machine can read.
Overfin needs a **Jellyfin of its own** (Jellyfin 12): the compose file
runs one, which Overfin sets up by itself on its first start, and manages
from then on. A Jellyfin you already use stays yours and untouched: see
[Your own Jellyfin](#your-own-jellyfin). Each release comes as a signed
image for x86 and ARM (`ghcr.io/overfin/overfin`).

**1. Start it:**

```sh
mkdir overfin && cd overfin
curl -fsSLO https://raw.githubusercontent.com/overfin/server/v0.8.1/deploy/compose.yaml
curl -fsSL -o .env https://raw.githubusercontent.com/overfin/server/v0.8.1/deploy/.env.example
# in compose.yaml, change /srv/media to where your films and series are, then:
docker compose up -d
```

Your films and series are on the media line of `compose.yaml` (`/srv/media:/media:ro`: the part before
the colon is where they are on this machine; Jellyfin only reads them).
[.env](../deploy/.env.example) is optional: `OVERFIN_DOMAIN` (see below), and
your TMDB token as `OVERFIN_TMDB_TOKEN`, or give it in the wizard. On Fedora, RHEL and
other SELinux systems, add `,z` after `:ro` on the media line of
`compose.yaml`, so Jellyfin may read the folder.

Films and series in more than one place (another disk, a NAS share
mounted on the machine): add a line for each under Jellyfin's `volumes`
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
control panel: add people (People: an email and a first password, which
they change at their first sign-in), connect an email server (Server,
Email), make invitations (People, Registration: links anyone can register
with, confirming their email), choose your GPU (Server, Transcoding; the
compose file says how to give Jellyfin one). Without an email server,
there are no invitations or forgotten-password emails: you add people
yourself.

Overfin keeps its secret key in `overfin-secrets` and everything else in
`overfin-data`: back both up, apart. Jellyfin's data is in
`jellyfin-config`.

**Jellyfin itself** is closed: nobody signs in to it, and the compose file
publishes no port for it. To look inside, open access in the control panel
(Server, Jellyfin), and uncomment its `ports:` line, which publishes it on
the machine itself only (reach it through an SSH tunnel), or on your LAN
address. Changes made there may break Overfin, or be undone by it.

## Your own Jellyfin

To run Overfin alone, next to a Jellyfin you run yourself:

```sh
docker run -d --name overfin -p 8080:8080 \
  -v overfin-data:/data -v overfin-secrets:/secrets \
  ghcr.io/overfin/overfin:0.8.1
```

and give the wizard Jellyfin's address (or set `OVERFIN_JELLYFIN_URL`).
That Jellyfin must be **Overfin's alone**: Overfin sets the settings it
needs there, and sets them back on every start. A fresh Jellyfin, Overfin
sets up itself. One already set up, it takes only if you hand it over: the
wizard asks for one of its administrators' login, used once, shows what
that server holds, and waits for you to confirm it's dedicated to Overfin.
A Jellyfin people use directly is never a good fit: run a second one for
Overfin, on the same media folders.

## On the internet, with HTTPS

**Overfin can get its own certificate** from Let's Encrypt. Point your
domain at the machine, open ports 80 and 443, and run it with your domain:

```sh
docker run -d --name overfin -p 80:8080 -p 443:8443 \
  -e OVERFIN_DOMAIN=media.example.org \
  -v overfin-data:/data -v overfin-secrets:/secrets \
  ghcr.io/overfin/overfin:0.8.1
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
([overfin.example.toml](../overfin.example.toml) has every setting):

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
`serve = "none"`, Overfin only answers `/api/`, and your proxy serves the
site, asking Overfin's gate first (`forward_auth` to `/api/v1/gate`) to keep
its pages private.


---

[← Back to the README](../README.md)
