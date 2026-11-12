# Security

Overfin exists to make a Jellyfin server safe to put behind a public website.
These rules are the point of the project; a change that breaks one is a bug.
[THREAT_MODEL.md](THREAT_MODEL.md) explains which threat each rule answers.

How to read them: each rule is one promise that can be checked against the
code. When the code does something the promise doesn't cover, on purpose,
it's listed under the rule as an exception. A fact is stated once; other
rules point to it by number. Rules describe what Overfin does today;
what's planned is in [ROADMAP.md](ROADMAP.md).

## Hard rules and settings

Overfin must be easy to run, for anyone from a first-time operator to a
network security expert. So the rules come in two kinds:

- **Hard rules** hold on every server, whatever its settings. No setting
  loosens them. Above all, between servers (rules 94 to 100): one badly
  run server must never be a way into another.
- **Settings** are where the operator's setup decides. Each is listed here
  with what it can be set to. Its default is the safest choice that works
  without extra setup, and changing it only ever affects that operator's
  own server:
  - **HTTPS** (Deployment): Overfin's own certificate, a reverse proxy's,
    or none, for a LAN or a test. Off until configured.
  - **Cookies** (rule 26) follow HTTPS: `__Host-` and `Secure` with it,
    without both without it, since browsers refuse them there.
  - **The control panel** (rules 82, 83): on the site for admins, by
    default with Overfin's own sign-in; `admin.private` keeps it to private
    addresses, `admin.listen` moves it to a private listener of its own.
  - **Access to Jellyfin itself** (rule 112): closed; an admin can open it
    from the control panel, and close it again.
  - **What the operator may share or allow**, each off or at its strictest
    by default: copying history from another server (rule 87), apps
    reading totals (rule 85), notices (rule 68), IMDb's datasets
    (rule 115), how many wrong PINs block a profile (rule 34, 3 to 10),
    wishes sent to Seerr a day (rule 48), the password's digit and special
    character (rule 105), and whether a copied setup link signs its person
    in at once (rule 108).

Every rule not named here is a hard rule.

## Rules

### Upstream access

1. Browsers and apps never reach Jellyfin, Seerr or TMDB through Overfin:
   Overfin calls them through typed functions, no route passes requests
   through, the route registrar refuses catch-all patterns, and no route
   leads to Jellyfin's dashboard.
   - Exception: stream requests take Jellyfin paths checked against the
     patterns of its streaming routes (rule 10).
2. An ID, whether a client or an upstream service sent it, must match a
   strict format before it's used, and only such IDs appear in an
   upstream URL's path.
3. Every other client value travels to upstream encoded, in a query or a
   body, checked against a fixed set of values, a range or a pattern;
   free text (a search term) is cut to a fixed length.
4. Every Jellyfin call runs as Overfin's one Jellyfin account: an ordinary
   user, never an administrator, hidden, with every library and none of
   Jellyfin's limits. It holds nothing personal: what each profile may see
   is Overfin's decision (rules 41 and 42), and what each profile does is
   Overfin's to keep (rule 58).
5. Each playback runs on a Jellyfin device of its own, so playbacks never
   share a session or a transcode.
6. A profile brought over with a Jellyfin user of its own, from before
   Overfin's one account, keeps that user's token for one use only:
   reading that profile's history, when it's brought over and for its data
   download (rule 75).
7. The Jellyfin admin key is used only for: setup; the checks and
   settings at each start (rule 113); provisioning, including a new token
   for Overfin's account when Jellyfin refuses the old one; the control
   panel; and deleting the Jellyfin users from before Overfin's one account
   (rule 66).
8. Seerr calls use Seerr's API key, acting as Overfin's own Seerr user
   (rule 70). No client ever chooses who acts for it upstream.
9. Upstream tokens never reach a browser or an app, stream addresses and
   playlists included.

### Upstream data

Jellyfin, Seerr and TMDB are trusted services serving untrusted data:
metadata comes from the internet, and media files can be written by
anyone. Paired servers are not trusted at all (rule 94).

10. Overfin fetches no absolute URL found in upstream data. From Jellyfin it
    takes only relative stream and playlist paths matching its known
    streaming routes; from TMDB, only image paths in a strict format,
    fetched from TMDB's own image host.
11. Media sources Jellyfin marks remote, or not a file (`.strm`
    included), are refused.
12. Overfin follows no redirect from any server it calls: Jellyfin, Seerr,
    TMDB, IMDb, the identity provider, paired servers, the notice webhook.
13. Answers from upstream have size and time limits.
    - Exception: media streams, Jellyfin's or a paired server's, must start
      within a minute, then have no limit, so a film is never cut off.
    - Exception: subtitles are waited for up to 31 minutes, Jellyfin's own
      limit: the first time, Jellyfin reads the whole file to extract them.
    - Exception: Jellyfin's event socket has no overall limit; it's dropped
      after 90 seconds of silence.
14. HLS playlists are parsed, never searched and replaced. Only entries
    matching Jellyfin's known segment addresses are kept, and unknown tags
    are dropped. A paired server's playlists pass only when every address
    in them is the name of a file of the same stream.
15. Metadata text is served as plain text: markup, control characters and
    bidirectional overrides removed, length capped.
16. Images are decoded and re-encoded, which strips what's embedded in
    them: as JPEG, or PNG for logos and branding. Their size is checked
    before decoding, and oversized ones are refused. SVG, HTML and anything
    that fails to decode are never served. Trickplay sheets are re-encoded
    as JPEG, never resized.
17. Subtitles are served only as sanitized WebVTT, or, for subtitles that
    are pictures (PGS), as the track read whole and refused unless every
    part of it is well-formed and within its limits. A paired server's are
    checked again here. Fonts and other attached files are never served on
    their own. Overfin asks Jellyfin for no subtitle track inside remuxed
    and transcoded streams, and drops subtitle entries from playlists.
    - Exception: a file played as it is (direct play) is sent unchanged,
      with the subtitles, attached files and tags inside it; rules 15, 16
      and 77 don't apply to its contents.
18. Of an upstream answer's headers, only these are passed on: a checked
    `Content-Type` (audio or video, otherwise `application/octet-stream`),
    `Content-Length`, `Content-Range` and `Accept-Ranges`. Never
    `Set-Cookie` or `Location`.

### Identity and sessions

19. Identity comes from one of: Overfin's own sign-in (an email and a
    password, rules 101 to 109; the default), an OpenID Connect provider
    through Overfin's login, or forward-auth headers set by a trusted
    proxy. Anonymous mode has no identity at all. With Overfin's own
    sign-in or login, identity headers are ignored, whoever sends them, and
    a login's answer completes only in the browser that started it.
20. Forward-auth headers are believed only from the configured proxy
    addresses, and a header that appears twice is not believed at all.
21. Routes need an identity unless they're registered as public (rule 55);
    outside anonymous mode, a request without one is refused.
22. The client's address is taken only from a trusted proxy: the last
    address it adds to `X-Forwarded-For`. Forwarded headers from anyone
    else are ignored.
    - Exception, stricter only: `X-Forwarded-Proto` is believed from
      anyone, as it can only make that client's cookies stricter.
    - Exception, stricter only: the panel's private-address check (rule 83)
      reads forwarded headers from anyone, and refuses if any of them names
      a public address.
23. With an identity provider or forward auth, only identities in the
    required group get an account, at a limited rate. With Overfin's own
    sign-in, an invitation or an admin decides (rules 90 to 92, 107).
24. With Overfin's own sign-in, every request checks the person behind the
    login: someone disabled or removed is out at once, and a login ends
    after 30 days without use.
25. With Overfin's login (OpenID Connect), each login is checked again with
    the provider every 10 minutes by default, using its refresh token:
    - the required group is checked on every request, against the groups
      last heard of; someone taken out of it is out at the next check;
    - a check whose ID token lists no groups means none; one without an ID
      token can't confirm them, and the login ends within 12 hours;
    - a provider out of reach for an hour fails the login closed;
    - a provider that gives no refresh token gets logins of 12 hours at
      most, never extended.
26. Every cookie is `HttpOnly` and `SameSite=Strict`, and over HTTPS has
    the `__Host-` prefix and is `Secure` (a setting: without HTTPS, browsers
    refuse both). A cookie set without HTTPS is never read over HTTPS, nor
    the reverse, and a site whose address is `https://` always gets HTTPS
    cookies.
    - Exception: with an identity provider, the invitation's cookie is
      `Lax`, so it survives the trip there; it holds only the invitation.
    - Why pages reload once: a Strict cookie doesn't come along with a link
      from elsewhere (an email, a QR code), so `/signin` and `/link`, asked
      that way, answer a page that opens the same address again from the
      site, once.
27. Only same-origin requests are accepted (`Sec-Fetch-Site` `same-origin`,
    or `none` for an address typed in), so other sites and sibling
    subdomains can't act for the user.
    - Exception: top-level GET navigations to the pages people arrive at
      from elsewhere: sign-in, an invitation, a forgotten password, a setup
      link, a screen's code, an identity provider's return. They act on
      nothing but their own code.
    - Exception: GET and HEAD of the site's own pages and files, outside
      `/api/` (rule 93), and the gate a reverse proxy asks.
    - Exception: stream routes, which check their ticket themselves
      (rules 45, 46).
    - A request without `Sec-Fetch-Site` (older browsers, some TVs' web
      views, apps) passes this check; rule 28 still stops cross-site
      writes.
28. Requests that change something (POST, PUT, PATCH) must be JSON, which
    plain HTML forms can't send; the control panel's image uploads must
    carry an image type instead. Overfin answers no preflight and sends no
    CORS header, so no other site can send JSON or DELETE, nor read an
    answer.
29. After a login or a step-up, people are sent back only to a path on the
    same site, never to an address taken unchecked from the request.
30. Upstream and identity-provider tokens are encrypted at rest, and PINs
    are kept as keyed hashes. Every key derives from one secret key kept
    outside the database, so the database alone opens nothing.
31. These need a fresh login (the password again with Overfin's own
    sign-in; a new login at the provider otherwise): deleting a profile or
    the account, exporting the whole account, changing or removing a PIN,
    changing one's password.
32. These need a PIN:
    - switching to a PIN-protected profile: that profile's;
    - setting another profile's first PIN, or changing a profile's kind,
      age, categories, libraries or hidden tags: the owner profile's;
    - a standard profile exporting its own data: its own, if it has one.

    A standard profile can set its own first PIN; changing or removing it
    takes the account's fresh login (rule 31).
33. No session ever starts on, or falls back to, a PIN-protected owner
    profile: it has no profile until one is chosen.
34. Each wrong PIN adds a growing delay, a minute at most. After
    `limits.pin_attempts` wrong PINs in a row (3 to 10, default 5), PIN
    entry on that profile is blocked until the owner's fresh login, which
    lifts every block in the account, or until an admin lifts it in the
    control panel. Without step-up, the block lasts 15 minutes. The count
    starts over 15 minutes after the last wrong PIN.

### Authorization

35. Every ID Overfin issues for something of its own (profiles, sessions,
    logins, playbacks, stream tickets, invitations, app keys, pairings…) is
    random, 128 bits at least. Catalog IDs (titles, people) are built from
    TMDB's and name nothing private.
36. Every lookup checks that what's asked for belongs to the caller: to
    their account, or for playbacks and tickets to their profile and
    session; invitations and app keys only to admins.
37. Whatever the caller may not see answers 404, never 403. A 403 only
    means the caller may not do this (an owner-only action, a missing
    step-up or PIN, an account that ended, a cross-site request…).
38. Only the owner profile may: manage profiles and other profiles' PINs;
    set the account's language, tastes and sign-in; delete the account;
    export the whole account; end sessions that aren't using its own
    profile.
39. An image is served only once the active profile may see what it
    belongs to (a library item, a catalog title, a collection), from
    Overfin's cache too. A person's photo goes to any profile: people carry
    no rating, library or tag.
40. A title's age is Overfin's own, from TMDB's ratings, never the media
    server's: its rating in the operator's ratings country when it has one,
    else the middle of its ratings elsewhere, each turned into the youngest
    age it allows by a table Overfin ships (ratings that only advise
    parents count as 8). A title with no such age has none.
41. A kids profile sees and wishes for only titles for its age, which the
    owner chooses, everywhere: search, discover, lists, title pages,
    images, playback. A title without an age is hidden from it.
42. The libraries and categories a profile gets (its own and its account
    template's) and the tags hidden from it apply to every profile, and
    Overfin applies them, not Jellyfin:
    - what can't be placed in a library or a category, like every title no
      library has, is hidden from a profile limited to some, and can't be
      wished for;
    - a title no library has shows to a profile with hidden tags only when
      Overfin already holds its TMDB keywords and none is hidden;
    - a deleted category counts for nothing: a profile limited to it alone
      sees nothing.
43. Counts (`totalCount`) include only what the active profile may see.
44. Play queues are refused.
45. A stream ticket lasts only as long as its playback: it ends when the
    playback stops, after 2 minutes with neither progress nor stream
    requests, or after 24 hours, and is never renewed. It works only in the
    session, and for the profile, that started it, and is refused when a
    browser says another site asked.
46. A ticket given to a paired server, for its viewer, belongs to no
    account here. It works only for that server, never for a browser (any
    request saying where it comes from is refused), only while the pairing
    lasts, and lasts like any other.

### Limits

47. Streams are limited per account (its account template), and
    playbacks that re-encode video in total (`limits.max_transcodes_total`).
    - Exception: a playback started as a copy that turns out to re-encode
      counts once that's found, so the total can briefly be one over.
48. These are rate-limited: sign-in, registration, forgotten passwords,
    PINs, search, copying history, new accounts, TMDB lookups of titles not
    kept (120 a minute per account), the apps API (120 calls a minute per
    key). Wishes go to Seerr at most `wishes.per_day` a day.
49. When an account reaches its stream limit, its oldest stream stops and
    that device is told why; the operator can choose to refuse the new
    stream instead. With a stream reserved for each profile (a template's
    setting), only the profile's own oldest stream stops. A new playback
    never stops another account's stream: with every transcode taken, one
    that needs a transcode is refused.
50. A transcode stops as soon as its playback stops, its ticket ends or its
    reports go silent.
51. Heavy work runs a few at a time: decoding images, hashing passwords,
    reading subtitles. Caches have byte limits.

### Web hardening

52. The API's and the control panel's answers carry a strict
    Content-Security-Policy, `frame-ancestors 'none'`, `nosniff` and no
    referrer. The site's own policy is the operator's (rule 93), strict by
    default.
53. The API's errors are RFC 9457 problem details with a machine-readable
    `type`; the control panel's and the apps API's are JSON with a `type`
    and a `detail`. Upstream errors become generic answers; their details
    stay in the server's log.
54. Logs never contain tokens, cookies, passwords, PINs or query strings,
    and name users only by a keyed hash of their ID.
    - Exception: the one-time setup code is printed in the log, on
      purpose: it proves the operator can read it (rule 84).
55. Only these routes need no identity, and they reveal no software
    version, no upstream server and nothing about anyone. A test fails on
    any other route that needs none.

    | Route | What |
    |---|---|
    | `/api/v1/health`, `/api/v1/info` | whether it's up; what a client needs before signing in |
    | sign-in, invitation, forgotten-password, setup and `/link` pages | with their script, style, logo and check against scripts |
    | sign-in, registration, forgotten-password and setup calls | rules 101 to 109 |
    | the login's start, the provider's return, the login's finish, logout | rule 25 |
    | the gate | rule 93 |
    | `theme.css`, the operator's branding | |
    | a screen's code: its start and its check | rule 116 |
    | `/api/v1/stream/…` | checks its ticket itself (rules 45, 46) |
    | `/api/v1/federation/…` | answers only signed requests of known servers, or a live pairing code (rule 95) |

56. The API's cursors are signed and bound to the query that made them; a
    changed cursor, or one used with another query, is refused.
    - Exception: the apps API's are plain positions in a list the key may
      read whole; federation's pages go by title key.
57. Only admins upload files: branding pictures (4 MB at most, PNG or JPEG,
    kept as PNG) and titles' artwork (10 MB at most). Uploads get rule 16's
    treatment, are made no wider than they're ever shown, and are kept by
    Overfin, never in Jellyfin (whose image routes check no permission). An
    artwork upload no edit uses is deleted after an hour. A title's artwork
    goes only to profiles that may see the title.

### Privacy

58. Overfin stores only what it needs:
    - for everyone: sessions and logins (with a device's name), profiles,
      short-lived playback state, encrypted upstream tokens and keys;
    - per profile: what it did with each title (where it stopped, whether
      and how often and when it watched it, its favorites, its track
      choices, series it took out of Continue Watching), its watchlist, its
      wishes in its order and which came, PIN failure times;
    - per account: what it said it likes (when the operator asks), and,
      with Overfin's own sign-in, the email, a hash of the password
      (PBKDF2-SHA256, 600,000 rounds), keyed hashes of the links sent or
      copied for it, whether it waits for setup or confirmation, and until
      a registration is confirmed, its invitation, sealed;
    - for the admin side: invitations, account expiry and limits, labels
      the operator sets, notices sent, screens' codes (hashed), history
      copies (the other server's user ID);
    - for wishes: what the fetcher said of each title and when each
      account last had its turn;
    - for federation: paired servers (address, key, limits, what each
      offers and wants), the titles they hold, the keys of servers no
      longer paired, as hashes, and which title keys of the catalog changed
      when, for 30 days (so paired servers ask only what changed).

    Profile data is deleted with its profile, account data with its
    account.
59. What a profile does is its own: only it sees it (and, for a kids
    profile, the owner, rule 73), it's in its data download (rule 75), and
    it's deleted with it. No control-panel page and no admin route shows
    it.
60. The admin side sees activity only live (Now playing): kept in memory,
    for a few minutes, within a size limit, never on disk. Names and emails
    from an identity provider are shown live through its connector, never
    stored or passed on.
61. Apps see only what rule 85 allows. The panel's valuation preview shows
    scores, never their parts, and there a title's watching, favorites,
    watchlist, follows and wishes count only with two accounts or more
    behind them.
62. No analytics, no telemetry, no calls home. Overfin contacts only what
    the operator set up: Jellyfin, Seerr, TMDB, IMDb, the identity
    provider, the email server, the notice webhook, paired servers.
63. Client addresses are used in memory (limits) and never stored or
    passed on, paired servers included (rule 97).
64. Overfin never sends a browser to a third party, except the identity
    provider, for signing in and out. Images, TMDB's included, and paired
    servers' streams (rule 97) are served by Overfin itself.
65. No answer outside the control panel reveals other people's data or
    activity.
    - Exception: a wish's state may hint that someone else wished for the
      title (rule 72).
66. Deleting an account deletes everything Overfin holds about it, and its
    profiles' Jellyfin users from before Overfin's one account. Seerr users
    accounts had before wishlists stay in Seerr for the operator to delete;
    Overfin no longer uses them.
67. Overfin offers no hooks for extra data collection. Operators who want
    more collect it with their own tools.
68. Notices are off by default. They carry account events only, never what
    anyone watches: an account made, its access ending soon or ended. They
    go by email to the admins, to the webhook the operator sets (http or
    https; signed when the operator makes a key; its address never logged),
    and, before access ends, by email to the person. A notice names the
    account by its label, or by its email with Overfin's own sign-in.
69. Overfin has no social features: no relations between people, comments,
    likes or messages. Operators who want them build them in their own
    backend, signing people in with the same identity provider.
70. Overfin's Jellyfin users have opaque names (`overfin-service-…`, and
    `of-…` from before), never real names or emails. Wishes sent to Seerr go
    as one Seerr user of Overfin's own, named "Overfin", whose address is on
    `.invalid` and as whom no one can sign in: Seerr learns what was wished
    for, never who wished it. Seerr users from before wishlists hold the
    accounts' names: keep Seerr private until they're deleted there.
71. Search words and other personal input travel in request bodies, never
    in the API's addresses.
    - Exception: the site's own search page keeps the words in its address,
      so back and reload work (browser history only; Overfin's log never
      holds query strings, rule 54).
    - Exception: TMDB gets search words in its query (rule 79).
72. No one learns who else wished for a title. A wish's state says what's
    happening to the title (waiting, downloading, not found), never whose
    turn brought it.
73. Profiles are private from each other; kids profiles are visible to the
    owner. Only the owner gets another profile's limits and preferences;
    the others get what choosing who's watching takes: its name, kind, and
    whether it has a PIN.
74. The owner profile lists and ends all the account's sessions; other
    profiles, only those using them. Ending one ends that device's login
    too.
75. People can download what Overfin holds about them, except secrets
    (tokens, PIN hashes, keys). The owner profile's download covers the
    account; any other profile's, only itself. A kids profile has no
    download of its own: it's in the owner's.
76. overfin-web loads nothing from third parties: no fonts, scripts or
    icons from elsewhere.
77. Titles are served without file names, tags (TMDB's keywords), or
    photos' time, place and camera; versions get neutral names (such as
    "2160p HDR · HEVC"), never Jellyfin's, which come from file names. No
    setting changes that.
    - Exception: owners see the list of the libraries' tags, to hide some
      from a profile.
78. File paths, server internals and permission flags are never served.
    - Exception, control panel only: Identification shows a film's or a
      series' folder and file names (never the whole path), to tell a title
      Jellyfin matched wrong; Libraries shows each library's folders, and
      the folders Jellyfin can read, to choose them.
79. Overfin asks TMDB, never the browser: what's trending, popular or
    upcoming, titles' details and ratings, search words, titles found by
    IMDb ID, and posters from TMDB's image server. Nothing about who's
    asking is sent. Without TMDB, it asks Seerr, which asks TMDB.
80. No TMDB token is shipped: each server uses its own, kept sealed (or
    given in the environment) and sent only to TMDB, as a bearer header,
    never in an address. Seerr's API key is kept sealed too. TMDB's data
    is kept five months at most without being fetched again, and deleted
    when its last source is removed.

### Administration

81. The control panel is apart from the API: no `/api/v1` route reaches
    it; it has its own routes, guards and sessions. Admins confirm it's
    them every time they sign in to it; a panel session ends after 30
    minutes without use (what the panel asks by itself in the background
    doesn't count) and after 12 hours at most. Admins can't take the admin
    mark from themselves, remove themselves or disable their own account.
82. With Overfin's own sign-in, the panel is at `/admin/` on the site, for
    people marked admin who confirmed a fresh login in the last five
    minutes, from the account's owner profile only (without a session, only
    when no PIN protects that profile). Their panel session ends as soon as
    they sign out of the site, stop being admins or switch profiles.
    `admin.private` keeps it to private addresses (rule 83's check);
    `admin.listen` puts it, with its sign-in and the setup page, on a
    listener of its own instead, and the site has none.
83. With an identity provider, the panel is on a listener of its own, off
    by default, which answers only private addresses (loopback, link-local,
    private ranges, 100.64.0.0/10, unique-local IPv6) and refuses whatever
    a proxy relays for a public address. Admins sign in there with a new
    login every time, in the admin group; the provider is asked again every
    5 minutes whether they still are: out of the group or refused, the
    session ends at once; the provider out of reach, it lasts 30 minutes.
    - Exception: a provider that gives no refresh token can't be asked
      again; the session lasts until it ends (rule 81).
84. Until Overfin is set up, it serves only the setup page: on the panel's
    listener, and with Overfin's own sign-in on the site too. Nothing is
    saved without the one-time code from Overfin's log; after 10 wrong
    codes it stops answering until a restart; each step is checked before
    it's saved; Jellyfin's key is kept sealed.
85. Other apps (Stewarr, Overtide…) read under `/apps/`, where the panel
    is and behind the same address limits, each with a key an admin made
    for what they ticked (values, watch statistics, in the libraries or
    not, what came and went, wishes to fetch, categories, episode orders),
    shown once, kept as a hash, revocable. What they read:
    - totals per title, counts below 2 given as 0, plays and favorites as 0
      when fewer than two accounts are behind them;
    - watch statistics without the titles fewer than two people watched,
      and times only by the day;
    - titles the libraries lack only when two accounts or more want them,
      or when Overfin picked them from TMDB's lists.

    - Exception: the fetcher's key (`wishes`, for Overtide) reads the wished
      titles in turn order, one person's wish included, never who wished
      nor how many.
    - Exception: scores (`score`) may count what one person did: a score
      mixes several inputs into one number, and the key is the operator's.
    - Exception: Overfin's picks can come from one account's watching.

### Code supply

86. No code copied or ported from other projects. Every dependency is
    pinned: Go modules by `go.sum`, GitHub Actions by commit, base images
    by digest.

### Migration

87. Copying history from another Jellyfin server is off until the operator
    names that server. Overfin signs in there with the person's own
    username and password, reads what they watched, where they stopped and
    their favorites, keeps a copy, and signs out; it changes nothing there.
    The password and that server's token are used for that one sign-in,
    never stored or logged. Failed tries are limited per person, and wrong
    usernames and wrong passwords get the same answer. An account there
    can be copied into one Overfin account only, once (an admin can allow
    another copy), never into a kids profile. Nothing is matched by name
    or email.

### Features

88. Every feature is built in. No code is loaded into Overfin while it
    runs, and there's no way to plug code of one's own into it.
89. Features built on the internal feature contract (watchlist,
    invitations, migration) get only: routes acting as the calling profile
    (or, on a visitor's route, Overfin setting the visitor's account up);
    control-panel routes under `/admin/api/modules/<name>/`, behind the
    panel's sign-in; a database of their own. Never the admin key, the
    media server, another feature or the core's data. The core tells them
    of deleted profiles and accounts, and they delete what they hold.

### Invitations

90. Invitation links carry 128 random bits; only their SHA-256 is kept,
    and a link is shown to the admin once (or emailed, when they ask).
    Opening one keeps it in a cookie for a day.
91. A single-use invitation lets one person in. With Overfin's own
    sign-in, registering holds a use at once, for 8 days, and for 30 days
    from when the address is confirmed; removing the person gives it back.
    The hold is kept sealed on the person's record, and they get an account
    only with it, whatever their cookies. With an identity provider, the
    use is taken when the account is set up, under the account-creation
    lock. Uses stay counted when the account is deleted.
92. With an identity provider, an invitation decides what an account gets,
    and, when the operator makes accounts invitation-only, whether there's
    one; it never lets in someone the provider or the required group
    refuses. With Overfin's own sign-in, an invitation is the only way in
    for a new email; everyone else is someone an admin added.

### The site

93. The site Overfin serves (overfin-web, or the operator's folder) goes to
    signed-in, allowed visitors only: the gate's check, which a reverse
    proxy can ask too (`/api/v1/gate`). Only GET and HEAD; never a folder
    listing, a name starting with a dot, or anything outside the folder
    (opened with `os.Root`, which links can't escape). Its files get the
    operator's Content-Security-Policy, strict by default.

### Between servers

94. Servers never trust each other by default. Two servers share only once
    paired by hand: one operator gives the other a code (valid 24 hours),
    the other uses it, and the first accepts. Each proves who it is with
    the key exchanged then, never with a web certificate, a name or an
    address alone.
95. Every request and answer between paired servers is signed (Ed25519,
    with a nonce used once, within 5 minutes). Refusals to servers known
    now or before are signed too, and unsigned ones count as no answer. A
    stranger gets a plain 404.
96. Each side shares only libraries it offers and the other wants: titles
    by key, with their languages, never files, paths or people. Playing
    stays within the pairing's limits (2 streams and 1 transcode at first,
    the owner's to change). What a paired server sends is cleaned like any
    upstream data (rules 14, 15, 17).
97. A browser never connects to another server: a paired server's title
    plays through the viewer's own server, which asks the other with a
    ticket the browser never sees. The other server sees that server only,
    never the viewer's address, browser or identity.
98. A server never acts for another beyond what was shared, and never
    hands one paired server's data to another.
99. A pairing over plain `http://` is signed but not encrypted: titles and
    tickets travel between the two servers in clear. The panel says so.
100. A compromised paired server can't reach the other's data or people.
     It can still make the other store its title list, however long, and
     fetch those titles' details from TMDB.

### Sign-in by email

101. Whether an email has an account is never told: a wrong password and
     an unknown email get the same answer in the same time; asking for a
     forgotten-password email, registering, or asking for a confirmation
     again get the same answer either way (registering an email that has
     an account sends it a note instead). Emails are sent in the
     background, so their sending time tells nothing.
102. Forms anyone can send first solve a check against scripts: a signed
     challenge, used once, within 10 minutes, whose SHA-256 with the answer
     starts with 17 zero bits.
     - Exception: forms that carry an emailed or setup link, a screen's
       secret, or a signed-in person's login.
103. Tries are limited per client address (an IPv6 /64 counting as one)
     and per email, in memory of bounded size. Wrong passwords: 10 per
     email per address, and 100 per email from all addresses together,
     each per 15 minutes. A browser that signed in with that email before
     keeps a device cookie (a random ID signed with the email, a year; it
     names no one and signs no one in): its wrong passwords count by
     themselves, 10 per 15 minutes, outside the 100.
     - Exception: guessing from many addresses blocks the email for new
       browsers for 15 minutes.
104. The links Overfin emails carry 256 random bits and work once:
     confirming an address within a day, choosing a new password within an
     hour. A new one replaces the last. Their token travels in the
     address's fragment, so it never reaches a server's log, and is used
     only on a click, so mail scanners opening links use nothing. Only keyed
     hashes of them are kept.
105. Passwords are 10 to 256 characters and, by default, need a digit and a
     special character. The operator can ask for a longer minimum (up to
     64) or drop the digit or the special character, never go below 10.
106. Registering takes an invitation (rule 92), an email server and an
     email only. The confirming link's page shows the address and is where
     the password is chosen; opening the link confirms nothing. An account
     still unconfirmed after a week is deleted.
107. Admins never type anyone's password. Someone an admin adds gets a
     random one nobody knows (256 bits, hashed at once, never shown, logged
     or sent) and a setup link to choose their own, which an admin also
     gives someone who forgot theirs.
     - Exception: `overfin -set-password`, run on the server itself, lets
       the operator set a password.
     - Exception: accounts made earlier with a password an admin typed
       keep it until they choose their own at their first sign-in.
108. A setup link carries 256 random bits, works once within 7 days, voids
     the one before, can be cancelled, keeps its token in the fragment, and
     is kept only as a keyed hash. Its page shows the address it's for,
     without using it up; looking is limited per client address. Choosing
     the password from an emailed one confirms the address. Copied by an
     admin, it doesn't: with an email server, the operator chooses whether
     the person signs in at once and confirms later, or confirms first.
     Someone an admin added is never removed as unconfirmed.
109. A new password ends the person's other logins, and the screen codes
     they confirmed that no screen collected.

### Overfin's Jellyfin

110. Overfin's Jellyfin is its own, and Overfin changes nothing on any
     other. It takes a Jellyfin over only when it's fresh (its setup never
     run: Overfin runs it), when it answers to a key Overfin created, or
     when the operator hands it over: they give an admin login, see what
     that server holds, and confirm it's for Overfin. That login is used
     only to look and to create Overfin's key, signed out each time, and
     never stored.
111. Overfin uses only keys it created, kept sealed, and revokes those of
     earlier takeovers.
112. Access to Overfin's Jellyfin is closed: from the takeover on, every
     administrator there has a password nobody knows.
     - Opening it, from the control panel, gives Overfin's administrator
       account there (made if missing) a new password, shown once, with a
       warning that changes made in Jellyfin may break Overfin and be
       undone by it.
     - Closing it gives every administrator a new password nobody knows;
       Jellyfin then ends their sign-ins. Other users and other apps' keys
       (Seerr's) are never touched.
     - Exception: when closing fails, access stays open. Overfin says so in
       its log at every start and in the panel; it closes again only from
       the panel.
113. The settings Overfin needs on its Jellyfin are Overfin's: it sets
     them, and sets them back at every start.
114. Overfin installs Intro Skipper only when the operator asks (setup
     wizard or control panel): it adds that project's plugin repository,
     checks that the pinned version's advertised checksum is the one
     Overfin knows (Jellyfin checks the download against it), installs it
     and restarts Jellyfin, whose plugin updates stay off. Removing it takes
     the plugin and its repository off. Overfin installs no other plugin.

### Outside data

115. IMDb's free datasets are off until the operator switches them on.
     Then the server downloads three of them, once a day, from IMDb's own
     address only (datasets.imdbws.com): files that are the same for
     everyone, so nothing about the server is sent. It keeps what it uses
     in its own folder, readable by Overfin alone (one file, and each day's
     votes for 35 days); downloads are deleted once that file is made, and
     a file that can't be read whole is never used. Switched off, the
     folder is deleted at the next pass. IMDb's numbers are never shown or
     served: only the order they give lists. The panel says IMDb allows the
     datasets for personal and non-commercial use.

### Signing a screen in with a code

116. A screen that's hard to type on asks for a code, after the check
     against scripts: 8 letters without look-alikes, valid 10 minutes,
     usable once. Only SHA-256 hashes are kept: of the code, and of a
     random secret only that screen holds (in an `HttpOnly` cookie), so
     only that screen collects the sign-in.
117. A signed-in person, on the account's owner profile, enters the code
     at `/link` or scans the screen's QR code (the code in the fragment),
     sees what confirming does, and confirms. The screen gets a login of
     its own on that account, not a fresh one (rule 31), lasting like any
     other, and starting without a profile when the owner's has a PIN.
     Someone told to choose a new password can neither confirm nor
     collect. Codes exist only with Overfin's own sign-in.
118. New codes are limited per address; wrong codes per person and per
     address.

## Deployment

Advice for operators. These aren't promises of Overfin's: they're what
keeps the promises above meaningful.

- Overfin needs a Jellyfin of its own (rule 110), on the same media as any
  other the operator runs. Until Overfin has set a fresh Jellyfin up, its
  setup is open to whoever reaches it first: keep it reachable only by
  Overfin until then.
- Keep Jellyfin and Seerr off the internet: ideally, reachable only from
  Overfin's host and the operator's admin network. Overfin's rules protect
  only what goes through Overfin.
- With an identity provider, publish the panel's listener only on the LAN
  or a VPN, never through the reverse proxy that serves the site.
- When access to Jellyfin is open (rule 112), reach its dashboard
  privately (LAN, VPN, SSH tunnel).
- HTTPS is recommended for any server reachable from the internet:
  Overfin gets its own certificate with `https.domain` (or
  `OVERFIN_DOMAIN`), or a reverse proxy provides it. Behind a proxy, set
  `site.url` to the site's `https://` address, and
  `identity.trusted_proxies`, so the client address is the proxy's to give.
  Without HTTPS, everything, passwords and emailed links included, travels
  in clear: keep that to a LAN or a test.
- With Overfin's own sign-in or login, the simplest is for the proxy to
  forward everything to Overfin. A proxy serving the site's pages itself
  can protect them by asking `/api/v1/gate` first. With forward auth from
  another provider, every route except the public ones (rule 55) must pass
  the proxy's authentication.
- On every route, the proxy must remove client-sent forwarded headers, and
  with forward auth identity headers too, before adding its own (rules 20
  and 22), and must add the client's address to `X-Forwarded-For` (rule
  83's check relies on it).

## Reporting a vulnerability

Please report it privately, through this repository's **Security** tab
(*Report a vulnerability*), not in a public issue. You'll get an answer, and
credit if you'd like it, once it's fixed.
