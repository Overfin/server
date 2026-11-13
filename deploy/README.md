# Running Overfin

Overfin runs as two containers from two images of the same version: its
**gateway**, the only part the internet reaches, and its **backend**,
with Overfin's data, its control panel, and its own Jellyfin with Overfin's
plugin. [How it's built, and why](../docs/architecture.md).

## Before you start

- A machine with Docker and Docker Compose, with your films and series on
  it (or mounted on it).
- For the internet: a domain pointing to the machine, and ports **80 and
  443** open to it. Without one, the site is plain http, for your LAN.

## Starting it

```sh
mkdir overfin && cd overfin
curl -fsSLO https://raw.githubusercontent.com/overfin/server/v0.9.0/deploy/compose.yaml
curl -fsSL -o .env https://raw.githubusercontent.com/overfin/server/v0.9.0/deploy/.env.example
```

In `compose.yaml`, change `/srv/media` on the media line to your media
folder; in `.env`, set `OVERFIN_DOMAIN` to your domain if you have one.
Then:

```sh
docker compose up -d
```

On Fedora, RHEL and other SELinux systems, the container can't read the
media folder until it's labeled for containers: add `,z` after `:ro` on the
media line.

The two containers pair by themselves, through a volume only they share.

## Setting it up

From your LAN, open `http://<the machine>:8081`: the setup wizard asks, one
step at a time, for the default language, a TMDB token (it says how to get
one; for now the site can't be used without it), Intro Skipper (optional),
Seerr (optional), a username and a password for the control panel, and
your email and a password as the site's first admin. The wizard and the
control panel answer only private addresses (your LAN, a VPN), never the
internet.

Then, in the control panel (`http://<the machine>:8081`):

- **Libraries**: add yours, with folders under `/media`.
- **Server, Email**: an SMTP server, so people can register with an
  invitation and choose a new password by email.
- **Server, Transcoding**: your GPU, once Jellyfin has it (below).
- **People**: add them, or send invitations.

The site is at `http://<the machine>`, or your domain.

A forgotten control panel password:

```sh
docker exec -it overfin-backend /overfin-backend -set-panel-password
```

## A GPU for converting videos

Uncomment the lines for yours under `backend` in `compose.yaml` (Intel or
AMD: `/dev/dri`; NVIDIA: the `deploy` block, with the NVIDIA Container
Toolkit installed), run `docker compose up -d`, then choose it in the
control panel and test it on your own videos.

## Looking inside Jellyfin

Jellyfin listens inside the backend's container only. Open access in the
control panel (Server, Jellyfin): it gives Overfin's administrator account
there a new password, shown once, and Jellyfin's own pages are at
`http://<the machine>:8082`, from your LAN, until you close access again.
Changes made there may break Overfin, or be undone by it.

## On two machines

The gateway can run on another machine, a small server facing the
internet (a VPS, say), with the backend at home: no port to open at home,
the backend connects out to its gateway. Run the `gateway` service there,
and the `backend` service at home, each without the `pairing` volume and
the settings that name it (`OVERFIN_PAIRING_FILE`, `OVERFIN_GATEWAY_ADDRESS`),
then pair them once:

```sh
docker exec overfin-backend /overfin-backend -pair <gateway's host:port>
```

and set the code it prints as `OVERFIN_PAIRING` on the gateway: the backend
pairs at its next attempt, and the code is used up (remove it from the
gateway's settings). Pair with the address the backend
will reach the gateway at, by the site's name when a proxy in front of the
gateway routes TLS by name (or add `-server-name <the site's domain>`).

Behind a TCP proxy that sends the PROXY protocol, give the gateway
`OVERFIN_PROXY_PROTOCOL_FROM` (its address); behind an HTTP proxy,
`OVERFIN_TRUSTED_PROXIES`. With the proxy on the same machine and the
gateway's ports published by Docker, that address is the Docker network's
own (its gateway, such as 172.18.0.1), not 127.0.0.1: give the network a
fixed subnet so it doesn't change. Publish the gateway's ports on 127.0.0.1
only, then: ports Docker publishes bypass firewalls such as ufw.

## Updating

Back up first (below): Overfin migrates its database by itself when it
starts, and a migrated database doesn't go back to an older Overfin. The
backup is the way back.

Set the new `OVERFIN_VERSION` (or take the release's `compose.yaml`,
copying over your media lines and your GPU), then
`docker compose pull && docker compose up -d`. Both images come at that
version: there's no Jellyfin version to choose. Until both run it, the
site says it's being updated.

## Backups

The backend's volumes: `secrets` holds Overfin's secret key, `data`
everything else; `jellyfin-config` is Jellyfin's. `jellyfin-cache` needs no
backup: Jellyfin makes it again. The gateway has nothing to back up (its
certificates come again, its pairing is made again). Keep the secret key
apart from the rest: with it, anyone can read the tokens sealed in
Overfin's database.

Stop the stack first, so the databases are copied whole, then start it
again. From the folder with `compose.yaml` (it names the project `overfin`, so
the volumes' names start with `overfin_`, whatever the folder is called):

```sh
docker compose stop
mkdir -p backup
for v in data secrets jellyfin-config; do
  docker run --rm -v overfin_$v:/v:ro -v "$PWD/backup":/b alpine tar czf /b/$v.tgz -C /v .
done
cp compose.yaml .env backup/
docker compose start
```

To restore, into empty volumes (after `docker compose down` and
`docker volume rm` of the three, or on a new machine):

```sh
for v in data secrets jellyfin-config; do
  docker volume create overfin_$v
  docker run --rm -v overfin_$v:/v -v "$PWD/backup":/b:ro alpine tar xzf /b/$v.tgz -C /v
done
cp backup/compose.yaml backup/.env .
docker compose up -d
```
