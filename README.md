# Overfin

**Your own streaming service, on top of Jellyfin.**

Share your films and series with family and friends the way a streaming
service would: their own sign-in, profiles, kids limits that hold, requests,
and a clean web app. Jellyfin stays private behind Overfin, and nobody needs
a Jellyfin account.

<p>
  <img src="docs/screenshots/home.jpg" alt="The home page: a slideshow of titles over rows of recommendations" width="72%">
  <img src="docs/screenshots/phone.jpg" alt="The home page on a phone" width="24%">
</p>
<p>
  <img src="docs/screenshots/film.jpg" alt="A series' page: poster, logo, rating, play and list buttons, its seasons" width="48%">
  <img src="docs/screenshots/library.jpg" alt="A category: its genres to choose from, recently added, and a row per genre" width="48%">
</p>

## Try it

You need Docker with Compose, and your media on the same machine:

```sh
mkdir overfin && cd overfin
curl -fsSLO https://raw.githubusercontent.com/overfin/server/v0.8.1/deploy/compose.yaml
curl -fsSL -o .env https://raw.githubusercontent.com/overfin/server/v0.8.1/deploy/.env.example
# in compose.yaml, change /srv/media to where your films and series are, then:
docker compose up -d
```

Open `http://<the machine>`: a setup wizard takes it from there, including
a free TMDB token, which Overfin needs for titles' names and pictures (the
wizard shows how to get one). Overfin comes with a Jellyfin of its own and
sets it up by itself. The
[install guide](docs/install.md) covers HTTPS, reverse proxies, more media
folders and a Jellyfin you run yourself.

## What you get

**For the people watching**

- **Sign in with an email**: no Jellyfin account, no app to install.
- **Profiles**, with PINs, and **kids profiles** that only ever see titles
  for their age.
- **A catalog that looks the part**: artwork from TMDB, cast pages, search,
  trending titles, and requests for what you don't have (with Seerr).
- **Watching in the browser**, phones included: subtitles, audio tracks,
  skip intro, next episode, and resume on any device.
- **Recommendations** per profile, a list, favorites, and their language.

**For you, running it**

- **One compose file** for Overfin and its own Jellyfin, with HTTPS by
  itself when you give it a domain.
- **A control panel**: people and invitations, what's playing, limits per
  account, categories (Films, Anime, Kids…), your GPU, your brand.
- **Bring people over** from another Jellyfin, watch history included.
- **Your identity provider** (OpenID Connect, forward auth) if you have one.
- **An open API**: the official web app uses nothing else, so your own app
  can do everything it does.

[Everything it does, in detail](docs/features.md).

## Where it stands

Overfin started in October 2026. It runs one real server every day, and
that's its only real-world use so far:

- **Browser only** for now: no TV or phone apps yet, and Jellyfin's own apps
  don't go through Overfin.
- **Jellyfin 12 only.**
- **Pre-releases (0.x)**: the API and settings can still change; the
  database migrates itself.
- **HTTPS by itself is new**: tested, but not yet run on a real domain.
  Behind a reverse proxy is the well-trodden path.

Try it, read it, build on it; don't rely on it for anything important yet.
What's next is in [ROADMAP.md](ROADMAP.md).

## Security

Every browser is untrusted, and so is what Jellyfin says. Every route is
declared (nothing passes through to Jellyfin), Jellyfin's tokens never
reach the browser, images are re-encoded, and nothing is collected for
analytics. The rules are in [SECURITY.md](SECURITY.md), the threats they
answer in [THREAT_MODEL.md](THREAT_MODEL.md). Nobody outside the project
has reviewed them yet.

## Guides

- [Installing and running Overfin](docs/install.md): setup, HTTPS, reverse
  proxies, your own Jellyfin, backups.
- [Sign-in and the control panel](docs/sign-in.md): identity providers,
  where the panel answers, getting back in.
- [Your brand, and your emails](docs/branding-and-emails.md)
- [Notices](docs/notices.md): emails and webhooks about accounts.
- [Moving people over from Jellyfin](docs/moving-from-jellyfin.md)
- [Features, in detail](docs/features.md): everything it does, TMDB and
  Seerr, building your own client.
- [Other apps](docs/apps-api.md): the API Overtide and Overswell read.
- [Useful to know](docs/operations.md): checks, reloads, recovery.
- [Development](docs/development.md)

## Build on it

```
browser ──▶ the site (overfin-web, or your own)
   └──▶ /api/v1/*  ──▶  Overfin  ──▶  Jellyfin (private)
```

- **The API**: [api/openapi.yaml](api/openapi.yaml) (endpoints not served
  yet are marked `x-status: planned`).
- **The JavaScript SDK**: [sdk/js](sdk/js), no dependencies, with a player.
- **The official web app**: [overfin-web](https://github.com/overfin/web),
  in Svelte, on the public API only.

## Development

```sh
go test ./...
go run ./cmd/overfin -config overfin.toml
```

Go, with three dependencies besides overfin-web; the control panel is a
Svelte app in `frontend/`. More in [Development](docs/development.md).

## How it's made

Overfin is designed by its maintainer, who makes the architecture, security and product decisions, and written by Claude (Anthropic's AI); Claude's commits carry a `Co-Authored-By: Claude` trailer. The code is checked by its tests, linters and the rules in [SECURITY.md](SECURITY.md); it hasn't had an independent security review yet.

## License

[MIT](LICENSE). overfin-web, which the official build bundles, includes
[hls.js](https://github.com/video-dev/hls.js), unmodified, under the Apache License 2.0.

Overfin is an independent project, not affiliated with or endorsed by the
Jellyfin project, Seerr or TMDB.
