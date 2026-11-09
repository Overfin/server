# Security

Overfin exists to make a Jellyfin server safe to put behind a public website.
These rules are the point of the project; a change that breaks one is a bug.
[THREAT_MODEL.md](THREAT_MODEL.md) explains which threat each rule answers.

## Hard rules and secure defaults

Overfin must be easy to run, for anyone from a first-time operator to a
network security expert. So the rules come in two kinds:

- **Hard rules** hold on every server, whatever its settings. No setting
  loosens them. Above all, between servers (rule 65): one badly
  run server must never be a way into another.
- **Secure defaults** are where an operator's choice of setup decides, and
  each says what it can be relaxed to. The default is always the strict
  one, and relaxing it only ever affects that operator's own server:
  - **HTTPS** is recommended, not required: Overfin's own (Let's Encrypt),
    a reverse proxy's, or none, for a LAN or a test.
  - **Cookies** (rule 17) are `__Host-` and `Secure` whenever there's
    HTTPS; without it, browsers refuse both, so they go without.
  - **The control panel** (rule 58) is on the site, for admins who confirm
    it's them, with Overfin's own sign-in; it can be kept to private
    addresses, or moved to a private listener of its own, off the site
    entirely; with an identity provider, it has its own private listener.
  - **Jellyfin and Seerr** are best kept off the internet (Deployment,
    below); exposing them is the operator's choice.
  - **Access to Jellyfin itself** (rule 67) is closed: its administrators
    have passwords nobody knows. An admin can open it from the control
    panel, and close it again.
  - **What the operator may choose to share or allow**, each off or at
    its strictest by default: copying
    history from another server (rule 60), apps reading totals (rule 40),
    and how many wrong PINs lock a profile (rule 22, within 3 to 10).

Every rule not named here is a hard rule.

## Rules

### Upstream access
1. Only the gateway talks to Jellyfin, Seerr and TMDB. Every upstream call is a
   typed function on a fixed list. There is no passthrough route, and the plugin
   registrar refuses catch-all patterns.
2. Client input reaches upstream only after checks. In URL paths, only IDs that
   match a strict format may appear (as in `/Items/{id}`). Everything else
   travels as encoded query or body values: parameters come from fixed sets of
   allowed values, and free text (such as a search term) is length-capped.
3. Every call to Jellyfin runs as Overfin's one Jellyfin account: an ordinary
   user of its own, never an administrator, hidden, with every library and no
   limit of Jellyfin's, made with the key Overfin created for itself
   (rule 67; that's provisioning), its token sealed like every other. It holds nothing personal: what each
   profile may see, Overfin decides before anything is shown or played
   (rule 28), and what each profile does is Overfin's to keep (rule 40).
   Each playback runs on a Jellyfin device of its own, so playbacks never
   share a session or a transcode. The one exception is a profile brought
   over with a Jellyfin user of its own, from before Overfin's one account:
   its token only reads that profile's history, once when it's brought over
   and again for its data download (rule 52). The Jellyfin admin key is used
   only by setup, provisioning code and the control panel (rule 58), never
   by the public API's user routes. Seerr has no per-user tokens, so Seerr
   calls use its API key with the acting user named in the request; that user
   is always Overfin's own (rule 47), never one a client names.
4. Upstream tokens never reach the browser, including inside stream URLs and
   HLS playlists.

### Upstream data
Jellyfin, Seerr and TMDB are trusted services serving untrusted data: metadata
comes from the internet, and media files can be written by anyone.

5. No URL found in upstream data is ever fetched. Remote and `.strm` media
   sources are not supported. The only URLs Overfin takes from Jellyfin are the
   stream URLs Jellyfin generates itself, and only as relative paths matching
   Jellyfin's known streaming routes, never absolute URLs. The only paths
   Overfin takes from TMDB data are image paths, checked against a strict
   format and fetched only from TMDB's fixed image host.
6. Overfin never follows redirects from Jellyfin, Seerr or TMDB. Upstream responses
   have size limits and timeouts, except media streams: Jellyfin must start
   answering within a minute, but once a stream flows it has no size or
   time limit, so a film is never cut off. Subtitles are waited for within
   Jellyfin's own limit (31 minutes): the first time a file's subtitles
   are asked for, Jellyfin reads the whole file to extract them.
7. IDs that come from upstream are validated exactly like client input.
8. HLS playlists are parsed, not searched and replaced. Only entries that match
   Jellyfin's known segment URLs are kept.
9. Metadata text is served as plain text. Markup, control characters and
   bidirectional overrides are removed, and lengths are capped.
10. Images are decoded and re-encoded (JPEG, or PNG when transparency is
    needed), which strips embedded metadata. Their dimensions are checked
    before decoding, and oversized images are refused. SVG, HTML and anything
    that fails to decode are never served. Trickplay sheets get the same
    treatment, re-encoded as JPEG.
    What admins upload is treated the same way on the way in: branding
    pictures (4 MB at most, PNG or JPEG, kept as PNG) and titles' artwork
    (the metadata editor, 10 MB at most), each made no wider than it's
    ever shown; titles' artwork is served only to profiles that may see a
    title it's the artwork of. Overfin makes the sizes it serves of TMDB's
    artwork and of uploads itself, from one original (images.Shrink);
    Jellyfin's own pictures come at Jellyfin's sizes, and trickplay sheets
    are never resized (the player reads them by position).
11. Subtitles Overfin serves are only sanitized WebVTT, or, for subtitles
    that are pictures (PGS), the track Jellyfin extracts, which Overfin
    reads whole and refuses unless every part of it is well-formed and
    within its limits. Fonts and other files attached to media are never
    served on their own. Remuxed and transcoded streams carry only the
    video and audio. A file played as it is (direct play) is sent whole,
    as any media server sends it: the subtitles, attached files and tags
    inside it reach the device unchecked.
12. From upstream responses, only the headers streaming needs are passed on: a
    checked `Content-Type`, `Content-Length`, `Content-Range` and
    `Accept-Ranges`. Never `Set-Cookie` or `Location`.

### Identity and sessions
13. Identity comes from Overfin's own sign-in (managed: an email and a
    password, rule 66; the default), from an
    OpenID Connect provider through Overfin's login, or from forward-auth
    headers set by a trusted proxy. With Overfin's sign-in or login,
    identity headers are ignored entirely, whoever sends them; a login
    answer only completes in the browser that started it. Forward-auth
    headers are accepted only from the configured proxy address, and the proxy must strip identity headers sent by
    clients. An identity header that appears more than once is not believed
    at all. Outside anonymous mode, a request to any non-public route that
    carries no identity is refused; routes are protected unless explicitly
    registered as public.
14. The client IP is taken only from the trusted proxy (the last address in
    the `X-Forwarded-For` header it sets). Forwarded headers from anyone else
    are ignored.
15. Only identities matching the configured group or claim are provisioned, and
    provisioning is rate-limited.
16. With Overfin's own sign-in, every request checks the person behind the
    login: someone disabled or removed is out at once, and a login ends
    after 30 days without activity. With an identity provider, sessions are
    revalidated with it at a configured
    interval (with Overfin's login, by refresh token every 10 minutes by
    default; if the provider stays unreachable for an hour, logins fail
    closed). A provider that gives no refresh tokens gets logins that last
    12 hours at most, without extension. Disabling an identity ends its
    sessions. The required group is checked on every request, against the
    groups Overfin last heard of: with Overfin's login, someone taken out of
    it is out at the next re-check. A re-check whose ID token lists no groups
    means none, and one with no ID token can't confirm them, so that login
    stops being extended and ends within 12 hours.
17. Session cookies are `HttpOnly` and `SameSite=Strict`, and over HTTPS
    they use the `__Host-` prefix and are `Secure`. A secure default: a
    server run without HTTPS (the operator's choice, for a LAN or a test)
    gets cookies without the prefix and the flag, which browsers refuse
    there. A cookie set without HTTPS is never read over HTTPS, nor the
    other way round, and a site whose address is `https://` always gets
    HTTPS cookies. The one cookie that isn't `SameSite=Strict` is the
    invitation's with an identity provider: it's `Lax`, so it survives the
    trip there, and holds only the invitation.
18. Only same-origin requests are accepted (`Sec-Fetch-Site: same-origin`, or
    `none` for plain navigation); cross-site and same-site requests are
    rejected, so sibling subdomains can't act for the user. The only
    exceptions are top-level GET navigations to the pages people arrive at
    from elsewhere (sign-in, an invitation, a forgotten password, an
    identity provider's return), which act on nothing but their own code
    and query, the gate a reverse proxy asks (rule 13), and the site's own
    pages and files (any GET or HEAD outside `/api/`, which change nothing;
    rule 64). A request
    without `Sec-Fetch-Site` (older browsers, some TVs' and apps' web views,
    apps) is let through this check; for those, the JSON rule is what rules
    out cross-site writes. State-changing requests must be JSON, except
    the control panel's image uploads, which must carry an image content
    type (plain HTML forms can send neither), and there is no CORS at all:
    a cross-site page can't send JSON without a preflight Overfin never
    answers.
19. After a login or step-up, users are sent back only to a path on the same
    origin, never to an address taken unchecked from the request.
20. Upstream tokens are encrypted at rest, with the key kept outside the
    database. PINs are hashed with that key, so a leaked database alone can't
    be used to guess them.
21. Rare, destructive actions (profile deletion, account deletion,
    exporting the whole account, changing or removing a PIN, changing one's
    password, and profile transfer, planned)
    require a fresh login: the password again with Overfin's own sign-in,
    a new login at the identity provider
    otherwise. Frequent sensitive actions require a PIN: switching to a
    PIN-protected profile needs that profile's; setting another profile's
    first PIN, and changing a profile's kind, age, categories, libraries or
    hidden tags, need the owner profile's. A standard profile exporting
    only its own data needs its own PIN, if it has one: the people using
    other profiles don't know the account's login. Kids profiles can't export
    themselves; their data is in the owner's export. No session ever falls
    back to a PIN-protected owner profile: when the owner has a PIN, a new
    session, or one whose profile was deleted, has no profile until one is
    chosen (`profile-required`).
22. Each wrong PIN adds a growing delay, never more than a minute. After
    a number of wrong PINs in a row (`limits.pin_attempts`, 3 to 10,
    default 5), PIN entry on that profile is blocked until the account
    owner confirms a fresh login (rule 21); on an instance without step-up,
    where the owner can't confirm a login, the block lasts 15 minutes. The
    count starts over 15 minutes after the last wrong PIN.

### Authorization
23. Every resource ID Overfin issues (profiles, sessions, playbacks, stream
    tickets, invitations, app keys, and the planned transfer codes)
    is random, at least 128 bits, and every lookup checks that it belongs to
    the current account (for playbacks and tickets, its profile; for
    invitations and app keys, that an admin asks). Catalog IDs (titles,
    people) are built from TMDB's and name nothing private.
24. Anything the caller may not see returns 404, never 403. A 403 only ever
    means the caller may not do this: an owner-only action, a missing step-up
    or PIN, an account that has ended or isn't allowed, a missing invitation,
    a cross-site request.
25. Account-wide actions are reserved for the owner profile: managing profiles
    and other profiles' PINs (a standard profile manages its own, rule 21),
    the account's language, its tastes and its sign-in,
    deleting the account, exporting the whole account, and ending other
    devices' sessions. Kids profiles can't export themselves; the owner's
    export holds them.
26. The client never chooses who acts for it upstream: Jellyfin calls run
    as Overfin's one account (rule 3), and Seerr's as Overfin's own Seerr
    user (rule 47).
27. An image is served only after checking that the active profile may see
    what it belongs to (a library item or a catalog title), also when it
    comes from Overfin's cache. A person's own photo goes to any profile:
    people carry no rating, library or tag.
28. Kids profiles see and wish for only titles for their age (one for films
    and series alike), chosen by the account's owner. A title's age is
    Overfin's own, from TMDB's ratings, never the media server's: its rating
    in the operator's ratings country when it has one, else the middle of
    its ratings in other countries, each converted to the youngest age it
    allows by a table Overfin ships (ratings that only advise parents count
    as 8). A title without such an age is hidden and can't be requested.
    Everywhere in the catalog (search, discover, lists, title details,
    images, playback), titles above the age are hidden. The same holds for
    the libraries and the categories a profile gets (its own, and its
    account template's) and the tags hidden from it, for any profile:
    Overfin applies them, not Jellyfin, and what it can't place in a
    library or a category, like every title no library has, is hidden from
    a profile limited to some (so it can't wish for them either). A category
    deleted counts for nothing: a profile limited to it alone sees nothing.
29. Play queues aren't accepted yet; when they are, every entry is checked
    against the active profile, when the queue is set and again when it
    advances.
30. A stream ticket lasts only as long as its playback: it ends when the
    playback stops, after 2 minutes without progress reports or stream
    requests, or after 24 hours at most, and is never renewed. It works only
    in the session, and for the profile, that started it.
    Transfer codes (planned, for profile transfer) will be long, single-use,
    rate-limited and expire within minutes.

### Limits
31. Streams are limited per account (its account template), and playbacks
    that re-encode the video in total (`limits.max_transcodes_total`).
    Images come in a fixed set of sizes. Sign-in, registration, forgotten
    passwords, PINs, search, copying history and new accounts are
    rate-limited; wishes go to Seerr at most wishes.per_day a day.
32. When an account reaches its stream limit, its oldest stream stops and the
    stopped device is told why (operators can choose to refuse the new stream
    instead). With a stream reserved for each profile (a template's setting),
    only the profile's own oldest stream ever stops, never another
    profile's. Overfin never stops another user's stream: when every
    transcode is taken, a new playback that needs one is refused (Overfin
    already plays files as they are, or copies their video, whenever the
    device can take it).
33. A transcode is stopped as soon as its playback stops, its ticket expires or
    its progress reports go silent, so abandoned streams never keep the server
    busy.

### Web hardening
34. Strict response headers: CSP, frame-ancestors none, nosniff, no referrer.
35. The API's errors use RFC 9457 problem details with a machine-readable
    `type`; the control panel and the apps API (rule 58) answer with plain
    errors. Upstream errors are mapped to generic responses; internal details stay in
    the server log.
36. Logs never contain tokens, cookies, passwords or PINs.
37. Public endpoints (`/api/v1/health`, the sign-in, invitation and
    forgotten-password pages with their script, style, logo and check
    against scripts, the sign-in, registration and forgotten-password calls,
    the login's start, the identity provider's return and the login's
    finish, the gate, logout, `theme.css`, the operator's branding,
    `/api/v1/info`, and signing a screen in with a code: the page to
    confirm a code (`/link`) and the screen's start and check, rule 69)
    never reveal software versions, upstream servers or anything about
    users. This list is the whole set: a test fails on any other route
    that needs no session.
38. The API's cursors are signed and bound to the query that produced them. A
    modified cursor, or one used with a different query, is refused. The
    apps API's (rule 58) are plain positions in a list the key may read whole.
39. Today, only admins upload files: branding pictures and titles'
    artwork (rule 10); an artwork upload no edit uses is deleted after an
    hour.
    Profile pictures (planned) will get rule 10's treatment, with stricter
    size limits and a rate limit. Overfin keeps uploads itself, never in
    Jellyfin, whose image endpoints don't check permissions.

### Privacy
40. Overfin stores only what it needs to run: encrypted upstream tokens
    and keys, sessions, profiles, and short-lived playback state; with the admin side, also invites, account
    expiry and limits, and labels the operator sets; with the watchlist,
    each profile's list of titles (private to it, deleted with it); with
    wishlists, each profile's wished titles in its order, and what arrived
    for it (private to it, in its data download, deleted with it), plus
    what the fetcher said of each title and when each account last had its
    turn; with
    Overfin's own sign-in, each person's email, a hash of their password
    (PBKDF2-SHA256, 600,000 rounds) and keyed hashes of the links it emails
    them, deleted with their account (and, until a registration is
    confirmed, its invitation, sealed); what each account said it likes,
    when the operator asks (seen by its owner profile, deleted with the
    account); and what each profile did with each title (where it
    stopped, whether it watched it, how many times and when last, its
    favorites), brought over from Jellyfin once with the profile's own
    token, then kept by Overfin. That belongs to its profile: only that
    profile sees it (and, for a kids profile, its account's owner, rule
    50), never the admins, whom neither the panel nor any admin route
    shows it; it's in the data download (rule 52) and deleted with the
    profile. The admin side reads activity live (Now playing: what plays
    now, from Overfin's memory), for the page being viewed, and never
    stores it: at most it keeps it in memory, never on
    disk, for a few minutes and within a size limit, so a restart forgets
    it. Names and emails from the identity provider are shown live through
    its connector, the same way, never stored, nor handed on. The one
    exception to "never the
    admins": apps the operator gives a key to (rule 58) read totals per
    film and series (plays to the end, viewers counted by account,
    favorites counted by profile, the day it was last played), never
    anything per person. On a small server, a total can still point to
    someone; the operator decides which apps get them.
41. No analytics, no telemetry, no calls home. Metrics are aggregate counts
    with no user labels.
42. Client IPs are used in memory (rate limiting) and never stored by Overfin.
43. Overfin never sends the browser to a third party other than the identity
    provider, for signing in and out: images, including TMDB posters, are
    served by the gateway.
44. A public API response never reveals other users' data or activity. Only
    the admin API (rule 58) shows users, to the operator's admins.
45. Deleting an account deletes everything Overfin holds about it, and the
    Jellyfin users of its profiles from before Overfin's one Jellyfin
    account (rule 3). The Seerr users accounts had before wishlists stay in
    Seerr, untouched, for the operator to delete there; Overfin no longer
    uses them.
46. Overfin offers no hooks for extra data collection. Operators who want more
    collect it with their own tools. Notices (off by default) carry account
    events only, never what anyone watches: an account made, its access
    ending soon or ended. They go by email to the admins, to a webhook at
    the address the operator sets (http or https, no redirects followed,
    signed when the operator makes a key, its address never logged), and,
    before their access ends, by email to the person. Social features are out of Overfin's
    scope: it stores no relations between users, comments, likes, messages
    or anything of the kind. Operators who want them build them in their own
    backend, logging users in with the same identity provider.
47. Overfin's users on Jellyfin get opaque names (its one account,
    `overfin-service-…`, and those of profiles from before it, `of-…`),
    never real names or emails. Wishes handed to Seerr (wishes.to =
    "seerr") go as one user of Overfin's own, named "Overfin", with a
    placeholder address on `.invalid`, that no one can sign in to Seerr as:
    Seerr learns what was wished for, never who wished it. The Seerr users
    accounts had before wishlists hold names (the accounts' as the panel
    showed them): keep Seerr private, reachable by Overfin and the operator
    only, until they're deleted there.
48. Search terms and other personal input travel in request bodies, never in
    URLs, so they stay out of access logs and browser history.
49. Users never learn who else wished for a title, nor that anyone did.
50. Profiles are private from each other; kids profiles are visible to their
    account owner.
51. The owner profile can list and end all of the account's sessions; other
    profiles only see and end the sessions currently using them. Ending one
    ends the device's login too, so it signs in again; a session not used
    since this began has no login known to end, until its next use.
52. Users can download what Overfin holds about them, except secrets: tokens,
    PIN hashes and keys are never exported. The owner profile's export covers
    the account; any other profile's export covers only that profile. A kids
    profile has no export of its own: it's in the owner's.
53. Sites built for Overfin should load nothing from third parties (fonts,
    scripts, icons); overfin-web does exactly that.
54. Logs identify users only by a keyed hash (HMAC) of their ID and never
    contain query strings.
55. Privacy-sensitive fields are never served, and no setting changes that:
    versions get neutral names (such as "2160p HDR · HEVC"), never Jellyfin's,
    which come from file names; photos' capture time, location and camera
    data, and titles' tags (TMDB's keywords), aren't served either (owners see the list of the libraries' tags, to hide some from a
    profile).
    Embedded metadata is still stripped from image files (rule 10).
    Security-sensitive data (file paths, server internals, permission flags) is
    never exposed, whatever the configuration. Two exceptions, both in the
    control panel, for admins only: Identification (rule 58), where admins
    see a film's or a series' folder and file names (never the whole path),
    to tell a title Jellyfin matched wrong (the index keeps only those
    names); and Libraries, where admins see each library's folders and
    browse the folders Jellyfin can read, to choose them.
56. Counts (`totalCount`) only include what the active profile may see, so
    hidden items never show up as a number.
57. What's trending, popular or upcoming, titles' details and their
    certifications are asked of TMDB by the server, with the operator's own
    read access token (or of Seerr, which asks TMDB, when TMDB itself isn't
    there), and their posters of TMDB's image server. Nothing about who's
    asking is sent; browsers never contact Seerr or TMDB (rule 43). No token
    is shipped with Overfin: each server uses its own. It's kept sealed like
    every token (or given in the environment), and sent only to TMDB, as a
    bearer header, never in a URL; Seerr's API key is kept sealed too.
    TMDB's data is kept as its terms allow: five months at most without
    being fetched again, and deleted entirely when its last source is
    removed.

### Administration
58. Overfin's administration, its built-in control panel, is apart from the
    public API: no admin route can be registered there, and the panel has
    its own routes, guards and sessions. Admins confirm it's them every
    time they sign in to it; a session ends after 30 minutes without
    activity, and after 12 hours at most. Where it lives is a secure
    default:
    - With Overfin's own sign-in, it's at `/admin/` on the site, for people
      marked admin, who confirmed a fresh login (rule 21) in the last five
      minutes; their panel session ends as soon as they sign out of the
      site or stop being admins. `admin.private` keeps it to private
      addresses, with the guard below. With `admin.listen` instead, it's
      only on a listener of its own, with the guard below, which also
      serves the sign-in, so admins sign in there, and the site has no
      panel at all; so does the setup page. On the site, the panel opens
      only from the account's owner profile; a browser without a session
      counts as the profile a new session would start on (the owner's,
      unless a PIN protects it), so deleting the session's cookie never
      passes for a PIN-protected owner's. In the panel, admins can't take
      the admin mark from themselves, remove themselves or disable their own
      account.
    - With an identity provider, it's on its own listener, off by default,
      which answers only clients with private addresses (loopback,
      link-local, private ranges, the shared range VPNs use (100.64.0.0/10),
      unique-local IPv6) and refuses any request a proxy relays for
      a public address, so routing it through a public reverse proxy fails
      closed. How admins reach it (LAN, VPN, SSH tunnel) is the operator's
      choice. Admins sign in there with a fresh login every time, and must
      belong to the configured admin group.

    Until Overfin is set up (Jellyfin, and with Overfin's own sign-in a
    first admin), it serves only the setup page, on the panel's listener
    with its guards, and with Overfin's own sign-in on the site's address
    too: it saves nothing without the one-time code from Overfin's log,
    stops answering after 10 wrong codes until a restart, and checks each
    step before saving it; Jellyfin's key is kept sealed, like
    every token. Other apps
    (Stewarr…) read what Overfin shares with them where the panel is, under
    /apps/, behind the same address limits (private addresses only when
    the panel is): not with an admin's sign-in but each with a key an admin
    made for it in the panel, for what the admin ticked (values, watch
    statistics, in the libraries or not), shown once, kept only as a hash,
    revocable.
    What they read are totals per title, counts below 2 given as 0, and
    plays and favorites given as 0 when fewer than two people (accounts)
    account for them; watch statistics leave out a title fewer than two
    people watched, and say when a title changed only by its day; a title
    the libraries lack is listed only when two accounts or more want it
    (watched, marked, asked for or kept for later), or when it's one of
    Overfin's picks from TMDB's lists. So no title and no time points at
    one person. The one exception is the fetcher's key (`wishes`, for
    Overtide): to fetch what people wish for, it reads the wished titles
    in turn order, one person's wish included, but never who wished a
    title nor how many did, and it says back only what became of each.

### Code supply
59. No code copied or ported from other projects. Dependencies are few, chosen
    on purpose, and pinned.

### Migration
60. Bringing history over from another Jellyfin server (migration) is off
    by default; the operator names that server to turn it on. It's a copy:
    Overfin signs in there with the person's own username and password,
    reads what they watched, where they stopped and their favorites, keeps
    a copy, and signs out. It changes nothing on that server. The password
    is used for that one sign-in and never stored or logged, and neither is
    the token it gives, which is for that server only, never Overfin's own;
    that's why administrator accounts may copy their history too. Failed
    attempts are limited per user, and wrong usernames and wrong passwords
    get the same answer. An account on the other server can be copied into
    only one Overfin account. Nothing is guessed: no matching by name or
    email, only the username and password.

### Resource use
61. Memory-heavy work, such as decoding images, runs with bounded
    concurrency and bounded caches, so a burst of requests can't exhaust the
    server's memory.

### Features
62. Every feature is built in; nothing is added or loaded while Overfin
    runs, and there's no way to plug code of one's own into it. Features
    written on the internal feature contract (the watchlist, invitations,
    migration) go through the same registrar as the rest (no catch-alls, a
    login required; their control-panel routes, under
    `/admin/api/modules/<name>/`, behind the panel's sign-in), act only as the calling profile (or, on a visitor's
    route, can only have Overfin set the visitor's account up), and never
    get the media server's admin key. Each talks only to Overfin's core,
    never to another feature, the media server or the core's own data: it
    has a database of its own, holding only its tables, and what features
    share is a core service. The core tells them of deleted profiles and
    accounts, and they delete what they hold about them.

### Invitations
63. Invitation links carry 128 random bits; only their SHA-256 is kept with
    the invitations, and a link is shown to the admin once (or emailed,
    when they ask). Opening one keeps it in an HttpOnly cookie for a day:
    with Overfin's own sign-in, the page shows the registration form (rule
    66); otherwise it leads on to the site. A registration takes a use of
    its invitation at once, held for it until its account is set up (or
    for a week and a day; removing the person gives it back), so a
    single-use invitation lets one person register. The hold is kept
    sealed, like a token, on the person's record, and someone who
    registered gets an account only with it, whatever their browser's
    cookies. Otherwise, an
    account is set up with it only after a login, under the account-creation lock, so a
    single-use invitation is used once. Uses stay counted when the account
    is deleted. With an identity provider, an invitation decides what an
    account gets, and, if the operator makes accounts invitation-only,
    whether one is set up; it never lets in someone the provider or the
    required group refuses. With Overfin's own sign-in, an invitation is what lets a new
    email in (rule 66); everyone else is someone an admin added.

### The site
64. The site Overfin serves (overfin-web, or the operator's folder) goes to
    logged-in, allowed visitors only, the same check as the gate. Only GET
    and HEAD; never a directory listing, never a name starting with a dot,
    never anything outside the folder (it's opened with `os.Root`, which
    symbolic links can't escape). Its files get their own
    Content-Security-Policy (strict by default: everything from the site
    itself), while the API keeps its own, stricter one. Other sites may
    link to its pages, which change nothing; the API still refuses them.

### Between servers
65. Servers never trust each other by default, and nothing here is a secure
    default: these hold whatever either operator chose for their own
    server. When servers share with each other (federation, planned), each
    peer is untrusted, proves who it is with keys exchanged when the two
    were paired (never with a web certificate alone, nor a name or an
    address), and gets only what was shared with it, under the sharing
    server's rules. A server never acts on another's behalf beyond that,
    and never hands one peer's data to another. A weak or compromised
    server can at worst lose what was shared with it; it can never reach
    into the servers that shared it.

### Sign-in by email
66. With Overfin's own sign-in, people sign in with an email and a
    password, and whether an email has an account here is never told: a
    wrong password gets the same answer as an unknown email, and checking
    one takes the same time either way; asking for a forgotten-password
    email, registering, or asking for a confirmation again gets the same
    answer either way (registering with an email that has an account sends
    that address a note instead). Emails are sent in the background, so how
    long sending takes tells nothing. Nobody registers without an
    invitation (rule 63) and an email server; a registered account works
    only once its address is confirmed, and one still unconfirmed after a
    week is deleted. Forms anyone can send first solve a check against
    scripts (a signed challenge, used once, within 10 minutes, whose SHA-256
    with the answer starts with 17 zero bits), and tries are limited per IP
    address (an IPv6 /64 counting as one) and per email, in memory of
    bounded size. Wrong passwords are limited per email and
    client address, so someone else's guesses never lock a person out,
    with a much higher cap per email from anywhere. The links Overfin emails carry 256 random bits
    and work once (confirming an address within a day, choosing a new
    password within an hour); a new one replaces the last. Their token
    travels in the address's fragment, so it never reaches a server's logs,
    and is used only on a click, so mail scanners opening links use nothing.
    Only keyed hashes of them are kept. Passwords are 10 to 256 characters
    and, by default, need a digit and a special character; the operator can
    ask for a longer minimum (up to 64) or drop the digit or the special
    character, never fewer than 10 characters. Someone an admin adds is asked, by default, for
    a new password at their first sign-in, and gets nothing before choosing
    one. A new password ends the person's other logins.

### Overfin's Jellyfin
67. Overfin's Jellyfin is dedicated to it, and Overfin never changes
    anything on any other Jellyfin. It takes a Jellyfin over only when it's
    fresh (its setup never run: Overfin runs it), when it answers to a key
    Overfin created, or when the operator hands it over: they give an admin
    login, see what that server holds, and confirm it's dedicated to
    Overfin; that login is used once to create Overfin's key, then signed
    out, and never stored. Overfin uses only keys it created itself, kept
    sealed like every token, and revokes the keys it made on earlier
    takeovers. Access to its Jellyfin is closed: every administrator there
    has a password nobody knows, from the takeover on (a server handed
    over is closed at once, once Overfin's key is saved). An admin can
    open access from the control panel, which gives Overfin's own
    administrator account a new password, shown once with a warning that
    changes made in Jellyfin may break Overfin and be undone by it, and
    close it again, which gives every administrator a new password nobody
    knows, which ends their sign-ins there, and changes nothing else (other
    users stay, and other apps' keys, such as Seerr's, are never touched).
    The settings Overfin needs are its own: it sets them, and sets them
    back on every start. Only when the operator asks (setup wizard or
    control panel), Overfin installs Intro Skipper on its Jellyfin: it adds
    that project's plugin repository, checks that the pinned version's
    checksum is the one Overfin knows, installs it and restarts Jellyfin
    (Jellyfin's own plugin updates stay off, so that version stays);
    removing it from the panel takes the plugin and its repository off
    again. Overfin installs no other plugin.

### Outside data
68. IMDb's free datasets are off until the operator switches them on
    (Valuation, in the control panel). Then the server downloads three of
    them itself, once a day, from IMDb's own address only
    (datasets.imdbws.com): plain files, the same for everyone, so nothing
    about the server's titles or people is sent. It keeps what it uses of
    them in its own folder beside the database, readable by Overfin alone:
    one file, and each day's votes for 35 days; the downloads are deleted
    once that file is made, and a file that can't be read whole is never
    used. Switched off, the folder is deleted. IMDb's data counts in
    titles' values, and its daily votes make the trending and popular
    lists in place of TMDB's: only their order shows, IMDb's own numbers
    are never shown or served. A listed title the server doesn't know yet
    is found on TMDB by its IMDb ID, which is all that's sent. IMDb allows
    the datasets for personal and non-commercial use: the panel says so
    where they're switched on.

### Signing a screen in with a code
69. A screen that's hard to type on (a TV) can be signed in from another
    device. It asks Overfin for a code, solving the check against scripts
    first: 8 letters, without letters that look alike, valid 10 minutes and
    usable once. Only the code's SHA-256
    is kept, together with a random secret that only that screen holds (in
    an `HttpOnly` cookie), so only the screen that asked can collect the
    sign-in. A person who is signed in enters the code at `/link`, or scans
    the screen's QR code (the same address with the code in its fragment,
    which never reaches a server's logs), sees what confirming will do,
    and confirms. `/link` sends anyone not signed in to sign in first;
    looking at a code and confirming it need a signed-in person. The screen
    then gets a login of its own on that person's account, not a fresh one
    (rule 21), which lasts like any other (rule 16) and starts without a
    profile when the owner's profile has a PIN. Wrong codes are counted per
    person and per address, and new codes per address. Whatever ends a
    person's logins (a new password, signing out everywhere) also ends the
    codes they confirmed that no screen collected yet; someone told to
    choose a new password can neither confirm a code nor collect one. For
    now, codes are offered only with Overfin's own sign-in.

## Deployment

Overfin needs a Jellyfin of its own (rule 67), on the same media as any
other the operator runs; their other servers are never touched. Until
Overfin has set a fresh Jellyfin up, its setup is open to whoever reaches it
first: keep it reachable only by Overfin until then. Keeping Overfin's
Jellyfin off the internet is recommended, not required: ideally its port is
reachable only from Overfin's host and the operator's admin network.
Overfin's rules protect only what goes through Overfin; whatever reaches
Jellyfin directly is Jellyfin's own business. With an identity provider, Overfin's admin
listener must not be reachable from the internet: publish it only on the LAN or a VPN, never
through the reverse proxy that serves the site (rule 58). Overfin never offers
a route to Jellyfin's dashboard; when access is open (rule 67), operators
reach it privately (LAN, VPN, SSH tunnel).

HTTPS is recommended for any server reachable from the internet: Overfin
gets its own certificate with `https.domain` (or `OVERFIN_DOMAIN`), or a
reverse proxy provides it. Behind a proxy, set `site.url` to the site's
`https://` address, so cookies are HTTPS ones whatever the proxy says, and
`identity.trusted_proxies`, so the client address is the proxy's to give.
Without HTTPS, everything sent, passwords and emailed links included,
travels in clear: keep that to a LAN or a test.

With Overfin's own sign-in or login, the simplest is for the proxy to
forward everything to Overfin, which serves the site, its own pages
(`/signin`, `/invite/…`, `/reset`) and the API; a proxy serving the site's
pages itself can protect them by asking `/api/v1/gate` first (forward auth
to Overfin). With forward-auth from another provider instead, every route
to Overfin except the public ones (rule 37) must pass
through the proxy's authentication. In both cases, on every route, the proxy
must strip client-sent forwarded headers, and with forward-auth also
identity headers, before adding its own (rules 13 and 14).

## Reporting a vulnerability

Please report it privately, through this repository's **Security** tab
(*Report a vulnerability*), not in a public issue. You'll get an answer, and
credit if you'd like it, once it's fixed.
