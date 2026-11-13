# Threat model

Overfin sits between the internet and a Jellyfin server of its own (plus
TMDB and Seerr, both optional). It protects Jellyfin from strangers, and users from strangers and from
each other. Numbers in brackets refer to the rules in
[SECURITY.md](SECURITY.md).

## Trust boundaries

```
internet ──▶ reverse proxy ──▶ gateway ◀══ link ══ backend ──▶ Jellyfin / Seerr / TMDB
 untrusted     operator's,      the first   (opened   the        trusted code,
               optional         boundary    by the    boundary   untrusted data
               (TLS, identity)              backend)
```

The gateway faces the internet and holds nothing worth taking; the backend
holds the data and the secrets, and checks everything itself [119, 123, 127].

| Party | Trusted? |
|---|---|
| Visitors, logged in or not | No |
| The website built on Overfin | Partly: it may have bugs, so Overfin limits what a broken site can do |
| The reverse proxy and identity provider, when there are | Yes, for the identity and client IP they assert |
| Overfin's gateway, to its backend | Partly: it may be compromised, so the backend checks every session itself and trusts only the client address it gives [123, 126] |
| Other Overfin servers (federation) | No: each proves who it is and gets only what was shared with it [94, 96] |
| Other sites on the same domain (sibling subdomains) | No |
| Jellyfin, Seerr and TMDB as services | Yes |
| Data from Jellyfin, Seerr and TMDB (metadata, files, subtitles, images) | No |
| The operator | Yes, by design |

## Attackers

| Attacker | Goal | Main defenses |
|---|---|---|
| Stranger on the internet | Reach Jellyfin, exploit it, get an account | Small fixed API [1], identity before any route [21], provisioning only for allowed identities [23], registration only with an invitation, a confirmed email and a check against scripts [92, 102, 106], rate limits [48] |
| Logged-in, malicious user | Other users' data, more access, exhausting the server | Ownership checks [8, 35, 36, 37, 39], owner-only account actions [38], signed cursors [56], limits [47, 48, 49] |
| Script injected into the website (XSS) | Act as the user, take over the account, send users to a phishing page | No tokens in reach [9, 26], step-up for lasting damage [31], checked return addresses [29] |
| A compromised sibling subdomain | Make requests in the user's name | Same-origin requests only [27], `__Host-` cookies over HTTPS [26] |
| Another server, weak or compromised | Reach into servers that share with it | Peers untrusted, authenticated, limited to what was shared [94–100] |
| Attacker-controlled media or metadata | Run code in the browser, reach the internal network, exhaust memory | Untrusted-data rules [10–18] |
| Network observer | See who watches what, catch passwords or links | TLS (a secure default: without HTTPS, everything travels in clear), nothing personal in URLs [71] |
| An app the operator gave a key | Per-person activity | Totals only, for the scopes an admin ticked; keys kept as hashes, revocable [61, 85] |
| Thief of a database or backup | Tokens, identities, PINs | Encryption and keyed PIN hashes [30], pseudonyms [70], minimal data [58] |

## Case 1: a compromised website

A site built on Overfin has an XSS bug, so an attacker's script runs in a
logged-in user's browser on that site.

- **It can't steal anything durable.** Session cookies are `HttpOnly`, and
  `__Host-` scoped over HTTPS [26], and Jellyfin tokens never reach the
  browser [9].
- **Nothing else can do what it does.** Only same-origin requests are
  accepted, which excludes other sites and sibling subdomains alike, and state
  changes must be JSON [27, 28]. The compromised site is the only way in.
- **It can act as the user, but not do lasting damage.** Rare destructive
  actions need a fresh login: on the identity provider's own domain, which
  the script can't read or fake; or, with Overfin's own sign-in, the
  person's password. The script runs on the same origin as the sign-in
  page, so it could show a fake prompt and catch what's typed: with Overfin's own sign-in, this
  protection is weaker than a provider's login on its own domain. A PIN typed into the page could be captured by
  the script, so PINs only guard frequent, recoverable actions [32].
- **Overfin's own responses never become the XSS.** Images are re-encoded and
  never SVG or HTML [16], text is plain [15], and every response carries a
  restrictive CSP and `nosniff` [52].
- **It can't use Overfin to send users elsewhere.** After a login or step-up,
  users only return to a path on the same origin, so
  Overfin's domain can't be used as a springboard to a phishing page [29].

## Case 2: a malicious logged-in user

The user controls their browser and can call the API directly with any input.
Nothing is enforced by the UI.

- **Other people's things:** Overfin IDs are random and checked against the
  current account [35, 36]. Library items are filtered by Overfin, which reaches
  Jellyfin as one account and checks every item, listing, image and playback
  against the profile's access before it's shown [4, 41, 42]. Jellyfin's image endpoints don't check
  permissions, so Overfin checks every image itself, cached or not [39].
  Hidden things return 404, so their existence doesn't leak [37].
- **Acting for the whole account:** account-wide actions (managing profiles,
  deleting the account, exporting all of it, ending other devices' sessions)
  are reserved for the owner profile [38, 74, 75].
- **Escaping a kids profile:** PINs are checked on the server, wrong guesses
  slow down and then require the owner to log in again, and the client never
  picks who acts upstream [8, 32, 34]. Kids can only wish for titles
  for their age (from ratings in any country), and titles without one are refused [40, 41].
- **Exhausting resources:** stream, transcode and rate limits, fixed image
  sizes [47, 48]. Limits fail gently and never cut off another user [49]. Faking
  `X-Forwarded-For` doesn't dodge rate limits, because the client IP comes only
  from the trusted proxy [22].
- **Injecting upstream:** strict IDs, allowed values and length caps [2, 3].
- **Smuggling through cursors or counts:** cursors are signed and bound to
  their query [56], and counts never include hidden items, so they can't
  reveal them [43].
- **Brute force and link sharing:** stream tickets work only in the session
  and for the profile that started them, and end with their playback [45]; emailed links are long and work once [104]. Tickets are not bound to IP
  addresses: phones change networks mid-stream, and storing IPs would break
  [63].
- **Spying through upstream data:** Overfin's data model has no fields for
  other users' activity or for who requested a title [65, 72].

## Case 3: untrusted upstream data

Jellyfin, Seerr and TMDB are trusted, but much of what they serve isn't.
Metadata comes from internet providers and TMDB's contributors, subtitles from
subtitle sites, and media files (with their embedded titles, chapters,
subtitles and attachments) from anywhere.

- **Server-side request forgery:** a `.strm` file or remote media source can
  point at any URL, including the operator's internal network. Overfin never
  fetches URLs from data and never follows upstream redirects [10, 11, 12]. The only
  exceptions are Jellyfin's own stream paths and TMDB image paths, both
  format-checked and fetched only from their fixed host [10].
- **Redirecting the browser:** playlists are parsed and filtered [14], and only
  a few upstream headers are passed on [18].
- **Script or markup in metadata:** plain text only [15].
- **Malicious images and subtitles:** images are re-encoded [16], subtitles
  Overfin serves are sanitized WebVTT or picture tracks checked whole [17].
  Remuxed and transcoded streams carry only video and audio; a file played as
  it is is the known limitation below.
- **Oversized or malformed data:** size limits and timeouts, upstream IDs
  validated like client input, and image dimensions checked before decoding,
  so a small file claiming to be enormous can't exhaust memory [2, 13, 16].

## Case 4: a compromised gateway

The gateway is the part the internet reaches, possibly on a rented
server the operator doesn't fully trust. Suppose an attacker controls it.

- **People's data:** the gateway holds none, on its disk or in its logs
  [127]: no accounts, progress, emails, secrets, or the keys to Jellyfin,
  the identity provider or federation.
- **Acting as a user or an admin:** the backend checks every session
  itself; the gateway can only pass along what real visitors send while
  it's compromised, never make up a session or reach the control panel,
  which isn't behind it [123, 124, 129, 130].
- **Lying about addresses:** possible, so the backend's per-account and
  per-email limits don't depend on addresses [126].
- **Using the federation key:** it stays at the backend; the gateway only
  passes the bytes of TLS that runs end to end [132].
- **Getting in at home:** the backend opens no port; the link only lets
  the gateway ask what visitors may ask [119, 124]. Once the gateway is
  cleaned and paired again, nothing has to change elsewhere [120].

## Identity edge cases

- **Spoofed identity headers:** accepted only from the proxy, which must strip
  any the client sent. If a client-sent header slips through next to the
  proxy's, the duplicate makes the identity invalid rather than letting the
  first one win. A protected route with no identity is refused, and every
  route is protected unless explicitly public [20, 21].
- **Open sign-up at the identity provider:** only identities in the configured
  group or claim get accounts, at a limited rate [23].
- **Offboarding:** sessions are revalidated, and disabling an identity ends
  them [24, 25]. With Overfin's own sign-in,
  every request checks the person, so disabling them works at once.
- **Guessing who has an account:** Overfin's own sign-in answers the same
  for every email, and registering needs an invitation, solves a check
  against scripts and is limited per email, so it can't be used to spam
  strangers either [101–103, 106].
- **A stolen mailbox:** with Overfin's own sign-in, whoever reads someone's
  email can choose a new password with a "forgot password" email, which
  ends the person's other logins. Without an email server, there is no
  such way in [104, 109].
- **Being talked into confirming someone's code:** a stranger who sends
  someone their screen's code, or its QR code, gets that person's account
  on their screen if the person confirms. This is the known weakness of
  signing in by code, and rules can't remove it: the confirmation says what
  it does and to confirm only for a screen in front of you, codes last 10
  minutes, and the screen shows in the account's devices, where the owner
  can end it. Seeing a code isn't enough: only the screen that asked can
  collect the sign-in, and guessing one is limited per person and address
  [116–118].
- **Taking someone else's history:** copying it from another server
  requires that account's password, attempts are limited, and an account
  there can be copied into only one Overfin account. The token that sign-in
  gives is for that server only, used once and dropped [87].
- **Overfin taking over the wrong Jellyfin:** it takes over only a fresh
  one, one that answers to its own key, or one the operator explicitly
  hands over after seeing what it holds; it never changes another [110].

## Known limitations

- **Direct play sends a file whole.** When the device can play a file as it
  is, the subtitles, attached files (fonts, pictures) and tags inside it reach
  the device unchecked, as with any media server; the device's own player
  reads them [17].
- **Stream tickets appear in the reverse proxy's access logs,** because media
  players request them by URL. They work only in the session and for the
  profile that started them, and end with the playback (24 hours at most),
  which keeps this harmless [45], but operators should know.
- **Kids profiles are only as strong as the PINs on the adult profiles.** PINs
  are optional; without them, a child can switch to an adult profile.
- **Deleting an account doesn't delete everything elsewhere.** What
  Overfin holds goes, with the Jellyfin users of profiles from before
  Overfin's one account, but the Seerr users accounts had before wishlists
  stay in Seerr until the operator deletes them there [66].
- **Catalog searches reach TMDB.** With the TMDB catalog enabled, what users
  search for in the catalog is sent from the server to TMDB. Browsers never
  contact TMDB, but the operator's server does [64, 79].
- **Step-up trusts the identity provider's login time.** With an identity
  provider, Overfin asks for a new login over OpenID Connect and accepts it only if the signed ID token is
  for the same user and says the login happened after the step-up began. A
  provider that reports a wrong login time would defeat that check (with
  Overfin's own sign-in, step-up is the password again). Without
  step-up configured, the actions that need it are refused, and PIN locks end
  after 15 minutes instead of waiting for the owner [31, 34].

## Out of scope

- **A compromised Jellyfin, Seerr or TMDB.** Overfin can't protect those
  services themselves, but rules 10–18 keep a compromised upstream from automatically
  compromising Overfin and its users (a file played as it is aside, see Known
  limitations).
- **A compromised user device.** No web application can help there.
- **The operator.** They run the server and can see everything on it. Overfin's
  privacy rules protect users from strangers and from each other, not from the
  server's owner.
