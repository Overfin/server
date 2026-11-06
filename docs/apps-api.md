# Other apps: the apps API

Other apps can read some of what Overfin knows, with a key you make for
each in the control panel's **Apps** tab (shown once, revocable), for what
you tick: each title's **value** (the Valuation tab's score, 0 to 100),
its **watch statistics**, whether the libraries have it (**in the
libraries**), and **what came and went** (when each film and season arrived
in the libraries and left them). Totals per title only, never anything per person; counts
below 2 are given as 0, and so are plays and favorites when fewer than two
people account for them, so none points at one person. Apps ask the control
panel's address (the site's, or its own private listener when you gave it
one):

```
GET /apps/v1/titles?inLibrary=true&sort=score&order=asc&limit=100
Authorization: Bearer ofapp_…
```

```json
{"titles": [{"kind": "season", "name": "Game of Thrones", "tmdbId": "1399", "tvdbId": "121361", "imdbId": "tt0944947",
  "season": 2, "score": 41.5, "inLibrary": true,
  "stats": {"plays": 12, "viewers": 3, "favorites": 2, "lastPlayed": "2026-10-20"}}],
 "next": "100", "generatedAt": "…"}
```

One row per film and per season (a season carries its series' IDs), with
the groups the key may read, or those named in `fields` (`score`, `stats`,
`availability`). Filters: `inLibrary`, `kind` (`movie` or `season`),
`list` (`trending` or `popular`: the titles in TMDB's list, in its order
unless `sort` is given, films and series side by side by their place; a
season by its series' place; or `suggestions`: the titles the libraries
lack and that are out (or out within two days), in the order an app
should add them, the most valuable first with titles from the operator's
import lists mixed in by the **Lists share** set on the Valuation tab; never sorted
otherwise; each with the `categories` it fits, by what TMDB says of it, as
`GET /apps/v1/categories` lists them).
Sorting: `sort=score` (keys with the value only) or `name`, `order=asc` or
`desc`. Pages of `limit` rows (100 by default, at most 1000); the next one
at `cursor=` what `next` says. An app that frees space (such as Overtide)
asks for the least valuable titles the libraries have, as above; an app
that adds titles (such as Overtide), for `list=suggestions`.

The operator's Radarr and Sonarr import lists, sent by the app that reads
them (such as Overtide), all at once, replacing what it sent before (key
with **Import lists to send**):

```
PUT /apps/v1/lists
Authorization: Bearer ofapp_…

{"lists": [{"name": "Latest films", "titles": [{"kind": "movie", "tmdbId": "550"}, {"kind": "series", "tmdbId": "1399"}]}]}
```

Their titles the libraries lack are picks, valued like every other; the
**Lists share** of the suggestions comes from them, in their order. At
most 200 lists and 100,000 titles in all.

What TMDB says of a series' episodes, for an app that imports them and has
to tell what a file's number means (any key):

```
GET /apps/v1/episode-orders?tmdbId=615
```

Each season's episodes with their air dates, English names and names in the
server's language (`translated`, when it isn't English), and whether another
order on TMDB (DVD, production, digital…) puts other episodes in that
season's places (`rearranged`). 503 when Overfin has no TMDB token.

When each film and season arrived in the libraries and left them, as
Overfin's index saw it (what the libraries had before this history began
arrived at the date Jellyfin added it):

```
GET /apps/v1/history?since=2026-05-01T00:00:00Z&event=left
Authorization: Bearer ofapp_…
```

```json
{"events": [{"event": "left", "kind": "season", "tmdbId": "1399", "tvdbId": "121361", "season": 2, "at": "…"}],
 "next": "500"}
```

`event` is `arrived` or `left` (both when left out); pages of `limit`
events (500 by default, at most 1000), the next one at `cursor=` what
`next` says. Overtide uses it not to take a title again soon after it
left.

The older endpoint, for keys made for watch statistics before, still
answers:

```
GET /apps/v1/watch-stats[?since=2026-10-22T00:00:00Z]
Authorization: Bearer ofapp_…
```

```json
{"titles": [{"kind": "series", "tmdbId": "1399", "tvdbId": "121361", "imdbId": "tt0944947",
  "plays": 12, "viewers": 3, "favorites": 1, "lastPlayed": "2026-10-20", "changedAt": "…"}],
 "generatedAt": "…"}
```

`plays` are plays to the end (a series adds up its episodes), `viewers`
the accounts that watched some of it, `favorites` the profiles that keep
it as a favorite. Only titles at least two people watched are listed,
and `changedAt` is only the day (midnight UTC). With `since`, only the
titles changed on that day or later.


---

[← Back to the README](../README.md)
