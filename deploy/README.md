# Overfin and its Jellyfin, on one machine

This folder runs Overfin with a Jellyfin of its own, in one container:
Overfin's image carries its Jellyfin and Overfin's plugin for it, at
versions made for each other. Overfin starts that Jellyfin, sets it up on
its first start, keeps the settings it needs there, and is the only one
that talks to it: Jellyfin listens inside the container only. Nobody signs
in to Jellyfin itself.

## Before you start

- A machine with Docker and Docker Compose, with your films and series on
  it (or mounted on it).
- For the internet: a domain pointing to the machine, and ports **80 and
  443** open to it. Without one, Overfin serves plain http, for your LAN.

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

Jellyfin listens inside the container only. To look inside, open access in
the control panel (Server, Jellyfin), which gives Overfin's administrator
account there a new password, shown once. Then, in `compose.yaml`, add
`OVERFIN_JELLYFIN_LISTEN: all` under `environment:` and
`- "127.0.0.1:8096:8096"` under `ports:`, and run `docker compose up -d`:
Jellyfin is on this machine only (reach it with
`ssh -L 8096:localhost:8096 <the machine>`). Changes made there may break
Overfin, or be undone by it. When you're done, close access again from the
panel, remove both lines, and run `docker compose up -d`.

## Two machines

Overfin can run on a front machine (the one the internet reaches) and its
Jellyfin on the machine with the media and the GPU, from the same image at
the same version on both:

- on the media machine, the image with `OVERFIN_ROLE: jellyfin`, its
  `jellyfin-config` and `jellyfin-cache` volumes, the media, the GPU, and
  port 8096 published to the front machine only (a firewall rule, or a
  private network between the two);
- on the front machine, the image with `OVERFIN_ROLE: gateway` (or no
  role: it's the default), its `overfin-data` and `overfin-secrets`
  volumes, and `OVERFIN_JELLYFIN_URL: http://<the media machine>:8096`.

Update both together: Overfin serves nothing until its Jellyfin and plugin
are the versions it's made with.

## Updating

Back up first (below): Overfin migrates its database by itself when it
starts, and a migrated database doesn't go back to an older Overfin: an
older one refuses to start on it, saying so in its log. The backup is the
way back.

Then take the release's `compose.yaml` (keep your `.env`, and copy over
any lines you added, such as more media folders or a GPU), and
`docker compose pull && docker compose up -d`. Jellyfin and Overfin's
plugin come with Overfin's image: there's no other version to choose.

### From Overfin and Jellyfin in two containers

Up to 0.9, this compose file ran Jellyfin's own image beside Overfin.
Overfin now serves nothing until its Jellyfin comes from its own image
(every page says the server is being updated, and its log says why).
Back up first (below), then:

```sh
docker compose down
# Jellyfin's files were root's; Overfin's image runs as user 65532.
for v in jellyfin-config jellyfin-cache; do
  docker run --rm -v overfin_$v:/v alpine chown -R 65532:65532 /v
done
```

Take the release's `compose.yaml` (copy over your media lines and your
GPU, now under `overfin`), and `docker compose up -d`. The volumes keep
their names: Jellyfin keeps everything it had.

## Backups

`overfin-secrets` holds Overfin's secret key, `overfin-data` everything
else; `jellyfin-config` is Jellyfin's. `jellyfin-cache` needs no backup:
Jellyfin makes it again. Keep the secret key apart from the rest: with
it, anyone can read the tokens sealed in Overfin's database.

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
