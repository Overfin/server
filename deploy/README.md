# Running Overfin

Overfin runs as two containers from two images of the same version: its
**gateway**, the only part the internet reaches, and its **backend**,
with Overfin's data, its control panel, and its own Jellyfin with Overfin's
plugin. [How it's built, and why](../docs/architecture.md).

## Before you start

- A machine with Docker and Docker Compose, with your films and series on
  it (or mounted on it).
- For the internet: a domain pointing to the machine, and ports **80 and
  443** open to it, or a gateway on another machine (below). Without
  either, the site is plain http, for your LAN only.

## Starting it

```sh
mkdir overfin && cd overfin
curl -fsSLO https://raw.githubusercontent.com/overfin/server/v0.10.3/deploy/compose.yaml
curl -fsSL -o .env https://raw.githubusercontent.com/overfin/server/v0.10.3/deploy/.env.example
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

The gateway reaches the backend through a socket in a volume only the two
share: nothing to pair.

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

The site is at `http://<the machine>`, or your domain. With a domain,
the LAN keeps a site of its own at `http://<the machine>:8090` once you
publish that port (the commented line in `compose.yaml`): it answers
private addresses only, and works when the internet is down. Its sign-in
is apart from the domain's (plain http has its own cookies).

Without a domain, set the site's address in the control panel to the
machine's LAN address (`http://<the machine>`): invitations and emails
give it.

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

## A gateway on another machine

A second gateway can run on another machine, a small server facing the
internet (a VPS, say): no port to open at home, the backend connects out
to it. The gateway beside the backend stays, for the LAN (without a
domain). A backend has one such remote gateway at most: pairing another
replaces it (a move to a new server). Paired servers (federation) are
then reached through it, so they never see your home's address.

On the other machine, run the `gateway` service without
`OVERFIN_LOCAL_SOCKET` and the `local` volume, with `OVERFIN_DOMAIN`. At
home, keep `OVERFIN_DOMAIN` on the backend (it's the site's address), and
take it off the `gateway` service, which stays the LAN's. Pair them once:

```sh
docker exec overfin-backend /overfin-backend -pair <gateway's host:port>
```

and set the code it prints as `OVERFIN_PAIRING` on the gateway: the backend
pairs at its next attempt, and the code is used up (remove it from the
gateway's settings). `-unpair` forgets it. Pair with the address the backend
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
`docker compose pull && docker compose up -d`. A `compose.yaml` that still
has the `pairing` volume keeps working; the new one's socket replaces it
(the backend then forgets that pairing by itself, and the `pairing` volume
can go: `docker volume rm overfin_pairing`). Both images come at that
version: there's no Jellyfin version to choose. Until both run it, the
site says it's being updated.

## Backups

The backend's volumes: `secrets` holds Overfin's secret key, `data`
everything else; `jellyfin-config` is Jellyfin's. `jellyfin-cache` needs no
backup: Jellyfin makes it again. The gateway has nothing to back up (its
certificates come again; a remote one's pairing is made again). Keep the secret key
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
