# Features

## Everything Overfin does

**For the people watching**

- **Signing in with their email** and a password. No Jellyfin account, no
  app to install.
- **Profiles**, each with its own history and an optional PIN. **Kids
  profiles** see only titles for their age, which the account's owner
  sets: a title's age comes from TMDB's ratings, those of the country you
  choose first, else the middle of other countries'. Everything else is
  hidden everywhere, not just greyed out, and can't be played. The owner
  can also keep any profile to some categories or libraries, or hide tags
  from it.
- **A catalog that looks the part**: posters, logos and backdrops from
  TMDB, cast pages, users' scores, search, trending and upcoming titles. A
  title your libraries don't have can be requested (with Seerr).
- **Watching in the browser**: played as is whenever the browser can,
  subtitles, audio tracks, quality choice, seek previews, skipping intros
  and credits (with Intro Skipper, which the setup offers to install), the
  next episode lined up, and picking up where they left off on any device.
- **Their language and their look**: each account picks its language among
  those you offer (the site, titles' names and synopses, emails), and each
  profile light, dark, or the device's.
- **Recommendations** for each profile on the home page, from what it
  watches and what its account said it likes, and **similar titles** on
  each title's page. Children's content shows in a profile's
  recommendations only when it's a kids profile or chose it ("Content for
  children").
- **Continue watching, a list (with a page of its own), favorites and
  watched marks** per profile; and the right to download what's kept about
  them, or delete it.

**For you, running it**

- **Overfin and its own Jellyfin, in one compose file**: Overfin sets
  Jellyfin up and keeps its settings, and nothing else talks to it. HTTPS
  by itself (Let's Encrypt) when you give it a domain, behind your own
  reverse proxy, or plain http on a LAN.
- **A setup wizard**, one step at a time: the default language, TMDB,
  Intro Skipper, Seerr, and you, its first admin.
- **Categories** of your own (Films, Series, Anime, Kids…), made from
  rules (films or series, genres, original language, library, tags, for
  ages up to, except other categories), each in the site's menu with its
  page, and on the home page if you like; the libraries can leave the
  menu. A template, or an account's owner, can keep profiles to some
  categories.
- **A control panel**: what's playing now (and stopping it), people and
  their accounts, notices (an account made, access ending, by email or
  webhook), invitations, templates (how many streams and profiles an
  account gets, whether each profile keeps a stream of its own, and the
  categories its profiles see), limits, languages,
  libraries, the GPU that converts videos (tested on your own files),
  seek-bar previews (off by default: they take time and disk space), email, Seerr, fixing titles Jellyfin matched wrong, what each title is
  worth to your server (Valuation).
- **Bringing people over** from another Jellyfin, their watch history
  included, with their old username and password, once. Nothing changes on
  that server.
- **Your own identity provider** instead of Overfin's sign-in, if you have
  one: OpenID Connect (Authentik, Pocket ID…) or a proxy's forward auth.
- **An API** for your own apps, and a key-protected one for tools such as
  Overtide: each title's value, from the weights you set (Valuation), its
  watch totals, and what to add next.

```
browser ──▶ the site (overfin-web, or your own)
   └──▶ /api/v1/*  ──▶  Overfin  ──▶  Jellyfin (private)
                          │
                          └──▶  its own sign-in, or your identity provider
```

Every app, official or not, talks to Overfin through the same public,
documented API, so a site or an app of your own can do everything the
official ones do, with total freedom on looks and feel. This repository is
the server; [overfin-web](https://github.com/overfin/web) is its web app.

## What's missing

The main gaps (details in [ROADMAP.md](../ROADMAP.md)):

- TV and phone apps, and a sign-in for native apps.
- Emails beyond accounts (confirming an address, a new password,
  invitations and notices about accounts are there).
- SDKs other than JavaScript.

The API specification also describes endpoints that aren't served yet; they
are marked `x-status: planned`.

## Building your own client

- **The API**, specified in [api/openapi.yaml](../api/openapi.yaml), with its
  conventions at the top. Errors are typed problem details.
- **The JavaScript SDK**, [sdk/js](../sdk/js): one ES module, no dependencies,
  TypeScript types, and a player that handles progress, recovery and
  takeovers.
- **overfin-web**, in
  [its own repository](https://github.com/overfin/web): Overfin's own
  web app, in Svelte and TypeScript on the SDK, bundled by the official
  build and served by default. It's what the real server runs, and it uses
  nothing but the public API, like any client.

## Security, in short

Overfin treats every browser as untrusted and Jellyfin's data as untrusted
input. Among its rules: every route is declared (no passthrough to
Jellyfin), Jellyfin tokens never reach the browser and are encrypted at
rest, Overfin reaches Jellyfin as one locked-down account and decides
itself what each profile may see, images are
re-encoded, nothing is collected for analytics, and between Overfin servers (planned
sharing) nothing is ever trusted by default. What depends on your setup
(HTTPS, where the control panel answers) has a secure default you may
relax for your own server.

The rules are in [SECURITY.md](../SECURITY.md), the threats they answer in
[THREAT_MODEL.md](../THREAT_MODEL.md). Remember they haven't been reviewed by
anyone outside the project yet.

## The features one by one, TMDB and Seerr
Everything is built in. Accounts and profiles, the library, playback and the
watchlist are always on; the rest is switched on in the control panel.

| Feature | Offers | |
| --- | --- | --- |
| Library | Browsing, images | Always on |
| Playback | Watching, subtitles, limits | Always on |
| Watchlist | Each profile's watchlist | Always on |
| Invitations | Invitations and invite templates | Always on |
| Migration | Bringing Jellyfin accounts over | Switch |
| Catalog | The library's index, matched to TMDB | Always on |
| Discovery | Trending, popular and upcoming titles, from TMDB | With a TMDB token or Seerr; kids profiles see what their age allows |
| Requests | Asking for titles no library has, through Seerr | When Seerr is connected |

Everything people see about a title, its text and its images, comes from
TMDB, in each account's language. For that, Overfin needs **your own TMDB
token** (TMDB's "API Read Access Token": free, and the setup wizard says
how to get one), given in the wizard, the control panel (Server, TMDB) or
`OVERFIN_TMDB_TOKEN`. No token is shipped with Overfin: each server uses
its own. For now, Overfin can't do without one: titles show with no name
or picture, Home stays empty, there's no discovery, and kids profiles see
nothing, since titles' ages come from TMDB's ratings. Running on what
Jellyfin knows alone is planned.

Overfin asks TMDB by itself, by title, never saying who's asking; browsers
never contact TMDB. It keeps TMDB's data as its terms allow (five months at
most), and overfin-web shows TMDB's attribution. The pictures of the
titles your libraries have are kept on disk ahead of time, in `artwork/`
next to the database, so browsing the library never waits for TMDB; the
others are fetched when first shown. TMDB's free API is for non-commercial
use: a site that charges for access needs TMDB's commercial terms. Seerr,
when connected, is a second source, and takes requests.

Each account asks Seerr through a Seerr user of its own, which Overfin makes
and names after the account (the name the control panel shows), so Seerr's
pages say who asked for what. Seerr's own settings give those users their
permissions, auto-approval and quotas. No one signs in to Seerr as them, and
people never need Seerr itself: keep it private on your network, reachable
by Overfin and you only.


---

[← Back to the README](../README.md)
