# Overfin and its Jellyfin

Overfin runs with a Jellyfin of its own, and comes as two images of the
same version:

- `ghcr.io/overfin/overfin`: Overfin alone, about 25 MB, on a base with no
  shell and no system packages;
- `ghcr.io/overfin/overfin-jellyfin`: Jellyfin's official image, at the
  version Overfin is made for, with Overfin's plugin for it built in, about
  1.7 GB (Jellyfin's ffmpeg and GPU libraries).

Overfin sets that Jellyfin up on its first start, keeps the settings it
needs there, and is the only one that talks to it. Nobody signs in to
Jellyfin itself. Overfin starts only beside the Jellyfin and plugin it's
made with: until then, every page says the server is being updated, and its
log says which image it needs.

## Why two images

- **Overfin faces the internet; Jellyfin holds your media.** Apart, a flaw
  in Overfin doesn't reach your media folders, the GPU or Jellyfin's files,
  and Overfin runs on a base with nothing in it to use.
- **Updating Overfin leaves Jellyfin running.** Most releases change only
  Overfin: playback and conversions in progress go on, and Jellyfin's image,
  unchanged, isn't downloaded again.
- **Two machines when you want them.** Jellyfin beside the media and the
  GPU (a home server, a NAS), Overfin on the machine the internet reaches (a
  VPS, a gateway), with nothing on that one it doesn't use.

One setting, `OVERFIN_VERSION`, picks the version of both, so they always
match.

## Before you start

- A machine with Docker and Docker Compose, with your films and series on
  it (or mounted on it).
- For the internet: a domain pointing to the machine, and ports **80 and
  443** open to it. Without one, Overfin serves plain http, for your LAN.

## On one machine

`compose.yaml` runs both containers on one machine.

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

On Fedora, RHEL and other SELinux systems, the container can't read the media
folder until it's labeled for containers: add `,z` after `:ro` on the media
line of `compose.yaml`.

## Setting it up

Open the machine's address in a browser. The setup wizard asks first for a
one-time code from Overfin's log, so nobody else can set your server up
before you do. This prints it:

```sh
docker logs overfin | grep -o '"code":"[^"]*' | tail -1 | cut -d'"' -f4
```

("permission denied": put `sudo` in front of `docker`, as for any Docker
command when your user isn't in the `docker` group.) The code changes each
time Overfin restarts. Then the wizard asks, one step
at a time: the default language, a TMDB token (it says how to get one;
for now the site can't be used without it), Intro Skipper (skippable), Seerr (skippable), and your email
and a password, as the first admin.

Then, in the control panel (`/admin/`):

- **Libraries**: add yours, with folders under `/media`, where the compose
  file puts your media folder.
- **Server, Email**: an SMTP server, so people can register with an
  invitation and choose a new password by email. Without one, you add
  people with a password, and set forgotten ones.
- **Server, Transcoding**: your GPU, once Jellyfin has it (below).
- **People**: add them, or send invitations.

## A GPU for converting videos

Uncomment the lines for yours in `compose.yaml` (Intel or
AMD: `/dev/dri`; NVIDIA: the `deploy` block, with the NVIDIA Container
Toolkit installed), run `docker compose up -d`, then choose it in the
control panel and test it on your own videos.

## Looking inside Jellyfin

Jellyfin is closed. To look inside, open access in the control panel
(Server, Jellyfin), which gives Overfin's administrator account there a new
password, shown once. Then uncomment the `ports:` lines under `jellyfin` in
`compose.yaml` and run `docker compose up -d`: Jellyfin is on this machine
only (reach it with `ssh -L 8096:localhost:8096 <the machine>`). Changes
made there may break Overfin, or be undone by it. When you're done, close
access again from the panel, comment the lines again, and run
`docker compose up -d`.

## On two machines

Jellyfin on the machine with the media and the GPU, Overfin on the one the
internet reaches. Each gets its compose file and a `.env` with the same
`OVERFIN_VERSION`.

On the **media machine**:

```sh
mkdir overfin && cd overfin
curl -fsSLO https://raw.githubusercontent.com/overfin/server/v0.9.0/deploy/compose.jellyfin.yaml
curl -fsSL -o .env https://raw.githubusercontent.com/overfin/server/v0.9.0/deploy/.env.example
```

In `compose.jellyfin.yaml`, change `/srv/media` to your media folder, and
put this machine's address on the network between the two before the port
(`192.168.1.20:8096:8096`, a VPN address). Keep that port closed to
everything but Overfin's machine, with a firewall rule: Overfin's plugin
answers only Overfin, but Jellyfin's own sign-in page answers whoever
reaches it. Then:

```sh
docker compose -f compose.jellyfin.yaml up -d
```

On the **front machine**:

```sh
mkdir overfin && cd overfin
curl -fsSLO https://raw.githubusercontent.com/overfin/server/v0.9.0/deploy/compose.overfin.yaml
curl -fsSL -o .env https://raw.githubusercontent.com/overfin/server/v0.9.0/deploy/.env.example
```

In `.env`, set `OVERFIN_JELLYFIN_URL` to Jellyfin's address
(`http://192.168.1.20:8096`), and `OVERFIN_DOMAIN` if you have one. Then:

```sh
docker compose -f compose.overfin.yaml up -d
```

and set it up in your browser, as below. A GPU goes on the media machine;
libraries' folders are the ones in `compose.jellyfin.yaml`.

To update, set the new `OVERFIN_VERSION` on both, the media machine first:

```sh
docker compose -f compose.jellyfin.yaml pull && docker compose -f compose.jellyfin.yaml up -d
```

then the same with `compose.overfin.yaml` on the front machine. Between the
two, or if one is forgotten, the site says it's being updated, and
Overfin's log names the image the media machine needs.

## Updating

Back up first (below): Overfin migrates its database by itself when it
starts, and a migrated database doesn't go back to an older Overfin: an
older one refuses to start on it, saying so in its log. The backup is the
way back.

Then take the release's `compose.yaml` (keep your `.env`, and copy over
any lines you added, such as more media folders or a GPU), and
`docker compose pull && docker compose up -d`. Both images come at the
release's version: there's no Jellyfin version to choose. Two machines:
see [On two machines](#on-two-machines).

### From 0.9 and before

Up to 0.9, Jellyfin ran from Jellyfin's own image, as root. It now comes
from `overfin-jellyfin`, as user 65532, and Overfin serves nothing until it
does (every page says the server is being updated, and its log says why).
Back up first (below), then:

```sh
docker compose down
for v in jellyfin-config jellyfin-cache; do
  docker run --rm -v overfin_$v:/v alpine chown -R 65532:65532 /v
done
```

Take the release's `compose.yaml` (copy over your media lines and your
GPU), and `docker compose up -d`. The volumes keep their names: Jellyfin
keeps everything it had.

## Backups

`overfin-secrets` holds Overfin's secret key, `overfin-data` everything
else; `jellyfin-config` is Jellyfin's. `jellyfin-cache` needs no backup:
Jellyfin makes it again. Keep the secret key apart from the rest: with
it, anyone can read the tokens sealed in Overfin's database. On two
machines, Overfin's volumes are on the front machine and Jellyfin's on the
media machine: the same commands on each, with `-f` and its compose file,
and only its own volumes in the list.

Stop the stack first, so the databases are copied whole, then start it
again. From the folder with `compose.yaml` (it names the project `overfin`, so
the volumes' names start with `overfin_`, whatever the folder is called):

```sh
docker compose stop
mkdir -p backup
for v in overfin-data overfin-secrets jellyfin-config; do
  docker run --rm -v overfin_$v:/v:ro -v "$PWD/backup":/b alpine tar czf /b/$v.tgz -C /v .
done
cp compose.yaml .env backup/
docker compose start
```

To restore, into empty volumes (after `docker compose down` and
`docker volume rm` of the three, or on a new machine):

```sh
for v in overfin-data overfin-secrets jellyfin-config; do
  docker volume create overfin_$v
  docker run --rm -v overfin_$v:/v -v "$PWD/backup":/b:ro alpine tar xzf /b/$v.tgz -C /v
done
cp backup/compose.yaml backup/.env .
docker compose up -d
```

Both were tested end to end: a backup of 0.4.0, an update to 0.5 and back
to 0.4.0 from the backup, accounts, libraries and settings intact.
