# The gateway and backend protocol

How Overfin's gateway (the part facing the internet) and its backend (the
data, the control panel, Jellyfin) talk, as the code does it today: each
message, each check, each limit, and the guarantees a test can check.
[architecture.md](architecture.md) says why there are two parts; the
promises are SECURITY.md's (rules 120 to 135). This document says how they
hold, what's tested, and what isn't yet.

Written 2026-11-13 from the code (`internal/link`, `internal/gateway`,
`internal/edge`, `cmd/overfin-gateway`, `overfin/events.go`). When the code
changes, this changes with it.

## Contents

1. [The parties](#1-the-parties)
2. [The connection](#2-the-connection)
3. [The preamble](#3-the-preamble)
4. [Pairing](#4-pairing)
5. [HTTP/2 over the link](#5-http2-over-the-link)
6. [Visitors' requests](#6-visitors-requests)
7. [The backend's link-only routes](#7-the-backends-link-only-routes)
8. [Events](#8-events)
9. [The session's first check](#9-the-sessions-first-check)
10. [The image cache](#10-the-image-cache)
11. [Egress, for federation](#11-egress-for-federation)
12. [The visitor's address](#12-the-visitors-address)
13. [Pages and limits](#13-pages-and-limits)
14. [Guarantees, and their tests](#14-guarantees-and-their-tests)
15. [Known gaps, and where this is going](#15-known-gaps-and-where-this-is-going)
16. [Each part taken over](#16-each-part-taken-over)
17. [Keys, from making to replacing](#17-keys-from-making-to-replacing)
18. [Invariants](#18-invariants)

## 1. The parties

- **The gateway** (`overfin-gateway`): HTTPS for visitors, the route
  allowlist, the session's first check, rate limits, the image cache, the
  pages shown when the backend is away. It holds its own key, its backend's
  public key, its certificates and cached images; no account, no secret of
  Overfin's.
- **The backend** (`overfin-backend`): everything else. It has no listener
  facing the internet: it connects out to its gateway, and serves visitors
  only through that connection. Its control panel has a listener of its own,
  for private addresses only (rule 129), outside this protocol.

## 2. The connection

**Who dials.** The backend, over TCP, to the address it paired with
(`net.Dialer`, 10 s to connect, 15 s keep-alive).

**Where the gateway listens.**
- With a domain (`OVERFIN_DOMAIN`): the link shares the HTTPS port (8443),
  told apart by its ALPN name. A ClientHello offering `overfin-link` or
  `overfin-egress` gets the link's TLS settings; any other gets the site's.
- Without a domain: a listener of its own, `OVERFIN_LINK_LISTEN` (default
  `:8444`).

**TLS.**
- TLS 1.3 only, on both sides, for the link and for egress.
- Each side has an Ed25519 key (`backend.key`, `gateway.key`, PKCS#8 PEM,
  mode 0600), in a self-signed certificate that only carries it: no names,
  no authority, valid from 1970 to 9999.
- The gateway requires a client certificate (any); the backend skips
  certificate verification. Each then takes the other's Ed25519 key from
  its certificate and compares it with the pinned one (or proves the
  pairing code, section 4). TLS 1.3's CertificateVerify proves each side
  holds its key.
- The backend requires the negotiated ALPN to be `overfin-link`; it sends
  its server name (SNI) as paired, so a proxy routing TLS by name passes it
  on.

**Reconnecting.** The backend tries again after 1 s, doubling up to 30 s; 30 s
after a refusal; back to 1 s after a connection that lasted over a minute.
Each wait varies by a fifth either way, so a backend coming back after a
gateway that keeps restarting doesn't keep its rhythm. It reads its pairing
file again at each attempt.

**A second connection.** The newest authenticated connection wins: the
gateway closes the one before.

## 3. The preamble

Before HTTP/2, one line each way, over the TLS connection. The backend
speaks first.

```
backend → gateway   OVFLINK1 link <version>\n
                    OVFLINK1 pair <version> <proof, 64 hex>\n
                    OVFLINK1 egress <version> <host:port>\n
gateway → backend   OK\n
                    OK <proof, 64 hex>\n          (pairing)
                    NO <reason>[ <detail>]\n      (then closes)
```

- A line is read byte by byte up to `\n`, at most 512 bytes, split on
  spaces. Nothing after it is read: the HTTP/2 that follows is untouched.
- Refusals: `NO preamble` (malformed), `NO version <gateway's version>`,
  `NO not-paired`, `NO no-code`, `NO code-used`, `NO wrong-code`,
  `NO error`; for egress also `NO not-allowed`, `NO unreachable`.
- **Order of checks, for every mode: the version first** (rule 122), then
  the key or the code. A backend of another version is never connected; the
  gateway notes the mismatch and shows visitors that the server is being
  updated, for as long as that backend keeps trying (two minutes after its
  last try).
- 15 s for the TLS handshake and the preamble together, on both sides; the
  deadline is cleared before HTTP/2.

## 4. Pairing

**The code.** 20 random bytes, base32 without padding, in groups of four:
`ABCD-EFGH-…` (39 characters). Read leniently: case, dashes and spaces
don't matter; it must decode to 20 bytes.

**Made by** `overfin-backend -pair <host:port> [-server-name <name>]`: the
backend keeps the address, the server name (the host by default) and the
code in `pairing.json`, prints the code, and keeps its former gateway pinned
until the new pairing succeeds; `overfin-backend -unpair` forgets it. The
link is for a remote gateway only: the gateway beside the backend reaches
it through a unix socket instead (`OVERFIN_LOCAL_SOCKET`), with no
pairing. (Setups made before that paired the two on one machine through a
shared file, `OVERFIN_GATEWAY_ADDRESS` and `OVERFIN_PAIRING_FILE`; that
still works.)

**The proof**, each side's:

```
HMAC-SHA256(key = the code's 20 bytes,
            msg = "overfin-link pairing " + side + "\n" ‖ gateway key (32) ‖ backend key (32))
side = "backend" | "gateway"
```

Both keys are bound: a proof made for another pair of keys is worthless.

**The gateway, given `pair`:**
1. checks the proof is 32 bytes of hex;
2. reads its code (`OVERFIN_PAIRING`, else the file `OVERFIN_PAIRING_FILE`,
   at each attempt); none: `NO no-code`;
3. refuses a code it already used (`NO code-used`): it keeps the SHA-256 of
   every code used, one a line, in `used-code`;
4. compares the proof in constant time: `NO wrong-code` (no lockout: a
   160-bit code can't be guessed; each counted, the first logged, then one
   line a minute at most with the count, so tries can't flood the log);
5. spends the code first: adds its hash to `used-code`, and if that can't
   be written, pairs nothing (`NO error`). A crash after it leaves the code
   spent and the old pairing in place; pair again with a new code. Never
   the reverse: a code is never usable twice;
6. keeps the backend's key (`backend.pub`) and answers with its own proof.

Checking a code, spending it and keeping the key are one step, under one
lock: of many backends pairing at once with the same code, one pairs.

**The backend** checks the gateway's proof in constant time, then pins the
gateway's key and forgets the code.

**Stored.** Backend, `<data>/link/`: `backend.key`, `pairing.json`
(address, server name, the gateway's key, a pending code). Gateway,
`<data>/link/`: `gateway.key`, `backend.pub`, `used-code`. Every file
written whole (a new file renamed into place), mode 0600.

## 5. HTTP/2 over the link

- Roles flipped: the **gateway is the HTTP/2 client**, the backend the
  server. Prior knowledge, no HTTP/1, no TLS of its own (the link's is
  under it).
- Settings, both sides: frames up to 1 MiB; flow-control windows of
  1 MiB per stream and 256 MiB per connection, room for every stream's at
  once, so responses nobody reads (a paused film) never hold up the
  others, and hold 256 MiB at most; a ping when the connection is
  quiet for 15 s, and the connection dropped if a ping goes unanswered for
  10 s. At most 250 requests at once on the link, every visitor's
  together (the backend's limit, as the HTTP/2 server; Go's own default,
  stated so a new Go can't change it): one past it waits for a stream to
  end.
- **Each visitor request is its own stream**, with its own window: a slow
  or stalled visitor fills only their stream's window.
- **Cancellation:** the gateway forwards with the visitor's request context.
  A visitor who leaves resets the stream; the backend's handler sees its
  context end, and so does its request to Jellyfin.
- No time or size limit on a stream: a film is never cut off (rule 13).

## 6. Visitors' requests

**What passes** (rule 124). The allowlist is built at start from the API
specification of the same release (`api/openapi.yaml`): each operation, by
method and path under `/api/v1`, except those marked `x-status: planned`;
`GET` routes also take `HEAD`. Pages: `GET`/`HEAD` on any other path, except
those under `/api`, `/admin`, `/apps` and `/link` (by prefix, slash or not:
`/links` is refused too). Everything else: 404, plain text, `Not found`.

**One spelling of a path.** Before routing, the gateway refuses (404) a
path with an encoded slash or backslash (`%2F`, `%5C`, any case), a
backslash, an empty segment (`//`), a dot segment (`.`, `..`, encoded or
not), or a control character. What's left reads the same to the gateway's
routes and refusals and to the backend's. Go's HTTP server itself already
answers 400 to two `Content-Length`s, two `Host`s, a null in the target,
and 501 to any `Transfer-Encoding` but `chunked` alone; with `chunked` and
a length, the length is dropped. Whatever follows a request's body on the
connection is read as the next request, checked as any other.

**In order**, outermost first: drop internal headers → the visitor's
address → rate limit → one spelling of a path → allowlist → the session's first check (non-public
operations) → the image cache (`/api/v1/images/`) → the link.

**Headers.**
- Dropped from visitors: `Overfin-Check`, `Overfin-Client-Addr`,
  `X-Forwarded-For`, `X-Forwarded-Host`, `X-Forwarded-Proto`, `Forwarded`,
  `X-Real-Ip`; `Upgrade` and `Connection` (nothing upgrades through the
  link). Each name goes whatever number of times it was sent.
- Set by the gateway: `Overfin-Client-Addr` (section 12);
  `X-Forwarded-Proto: https` when the visitor came over TLS, to the
  gateway or to a trusted proxy in front of it that says so (its last
  `X-Forwarded-Proto`).
- The backend sees the visitor's `Host` (the gateway's own requests say
  `backend`).
- A request's body (64 KiB at most, the backend's cap) is read whole at
  the gateway, within 20 s, before the request takes a stream: a visitor
  sending a byte now and then holds its own connection only (a 400
  otherwise).
- The backend never reads a visitor's bytes: the gateway reads the request
  once and writes it again over HTTP/2, so the two can't split a request
  differently (no smuggling between them).

**On the backend**, every request from the link: `Overfin-Client-Addr`
becomes the request's remote address when it parses, and is dropped either
way; `Overfin-Check` (section 10) makes the backend answer status and
headers only. In backend mode the site has no other listener, so these two
headers are trusted only from the link.

**Errors.** No backend connected: the gateway's own page (section 13). The
backend not answering: the same page, logged. The visitor gone: nothing.

## 7. The backend's link-only routes

Under `/link/`, which the gateway never forwards from a visitor; checked
before the site:

| Route | Answer |
|---|---|
| `GET /link/v1/egress?addr=<host:port>` | 204 if the backend asked for that address in the last minute, else 403 (section 11) |
| `GET /link/v1/events?since=<n>&at=<hex>` | the events stream (section 8); `at`, the first 8 bytes of revocation n's hash, once one was heard |
| `GET /link/v1/artwork/{id}?width=…` | a title's TMDB artwork at that width, without a profile's check, for the cache's prefetch (section 10) |

## 8. Events

The backend tells the gateway what it must know as it happens: one JSON
object a line (`application/x-ndjson`), on a stream the gateway keeps open.

```
{"type":"reset"}
{"type":"revoked","seq":12,"hash":"<SHA-256 of the login cookie, hex>","expires":1794000000}
{"type":"new","images":["/api/v1/images/tmdbart_poster_…", …]}
{"type":"access"}
{"type":"ping"}
```

- **`revoked`**: a sign-in that ended before its time (signed out, a new
  password, removed by an admin). A database trigger notes every live
  login deleted, whatever code deletes it, numbered (`seq`). The gateway
  refuses that cookie at its first check (section 9) until it would have
  expired anyway.
- **Resuming:** the gateway asks for those after the highest `seq` it saw;
  after a break, it reconnects within 2 s from there, so none is missed.
  A restarted gateway starts from 0 and gets every revocation still live.
- **`reset`**: the gateway asked from past the last number the backend's
  database ever gave (it was restored from a backup, its numbers start
  again lower), or from a number whose revocation isn't the one it heard
  (`at`, the first 8 bytes of that one's hash: restored, then given as
  many new ones as were lost). The backend sends `reset` first, then every revocation
  still live from its first; the gateway counts from those numbers. Without
  it, revocations made after the restore would carry numbers the gateway
  thinks it has heard of, and wait for its next restart.
- **`new`**: new titles' artwork, for the cache to fetch ahead. Best effort:
  those during a break aren't sent again.
- **`access`**: what a profile may see changed (its libraries, tags,
  categories, kind or ages; its account's template; a template or a
  category changed or deleted). Database triggers note it, whatever code
  makes it; the backend sends the event when the last number grows. The
  gateway's cache forgets every access answer it keeps (section 10). It also
  forgets them each time the stream starts, so a change during a break
  isn't missed.
- **`ping`** after 15 s without anything else. The gateway doesn't watch for
  it: HTTP/2's own pings find a dead link.
- The backend looks every second, 500 revocations at a time.

## 9. The session's first check

The backend's sign-in sets two cookies: the login (`overfin-login`) and its
**edge signature** (`overfin-edge`), with `__Host-` prefixes over HTTPS.

```
signature = Ed25519(backend's link key, "overfin-edge v1\n" ‖ SHA-256(login cookie))
```

base64url, set by the backend on any request with a signed-in identity whose
edge cookie is missing or wrong (HttpOnly, SameSite Lax, 400 days).

The gateway, on every API operation that needs a session: both cookies
present with the same prefix, a backend key pinned, a valid signature, the
login not revoked (section 8). Otherwise 401 `not-authenticated`, before
anything reaches the backend. Pages and public operations aren't checked
here. **The backend checks every session itself, as if the gateway weren't
there** (rule 123): this check only keeps noise off the link.

## 10. The image cache

- **Cacheable:** `GET /api/v1/images/…`, no `Range`, from a signed-in
  visitor. Stored when the backend answered 200, `Cache-Control` with
  `immutable`, at most 16 MiB.
- **On disk:** `<data>/cache/<2 hex>/<SHA-256 of path?query>`, mode 0600;
  emptied at each start (the index of what's there is in memory).
- **Access, before every hit** (rule 128): the gateway asks the backend the
  same request with `Overfin-Check: 1`; the backend runs its usual check
  and answers without the bytes (204 for 200). A yes is kept 5 minutes, in
  memory, for that session (the SHA-256 of the login cookie) and that
  image. A no, of any status: 404. Every answer kept is forgotten when what
  a profile may see changes, and when the events stream starts again
  (section 8); an answer to a check that began before that isn't kept.
- **What's kept:** worth = hits (halving each day) × time to fetch ÷ size;
  images five sessions or more asked for within a day are kept longest.
  Within `OVERFIN_CACHE_MAX` (10 GB by default), leaving a tenth of the disk
  free.
- **Fetched ahead:** new titles' artwork (section 8), at the widths visitors
  asked for before, from `/link/v1/artwork/`, a few at a time. Served only
  after the same check.

## 11. Egress, for federation

The backend's calls to paired servers go out through its gateway, so paired
servers see only the gateway (rule 132):

1. The backend notes the address it's about to reach (kept a minute).
2. It opens a TLS 1.3 connection to its gateway with ALPN `overfin-egress`,
   checks the gateway's key is the pinned one, and sends
   `OVFLINK1 egress <version> <host:port>`.
3. The gateway checks the version, then that the key is its backend's, and
   asks the backend over the link (`/link/v1/egress?addr=`, 4 s) whether it
   asked for that address. It looks the name up (3 s) and checks every
   address the name gives (an IPv4 address written as IPv6 read as IPv4):
   one it never reaches for its backend refuses the whole request:
   loopback (its own machine), link-local (a cloud's metadata service among
   them), unspecified, multicast, and IPv4's "this network" (0.0.0.0/8),
   reserved block (240.0.0.0/4) and broadcast. Private ranges stay, since
   paired servers may be on a LAN (section 16 says what that allows).
   Loopback is allowed only with the gateway's `federation.allow_loopback`.
   This is the same check a single Overfin makes on its own connections
   to peers (`internal/outbound`, SECURITY.md rule 136). It
   dials the address it checked (5 s), never looking it up again, and
   answers `OK`. All of it within the handshake's 15 s.
4. From then on the gateway copies bytes both ways. The backend's own TLS
   with the paired server runs inside, end to end: the gateway sees none of
   it. The federation key stays at the backend.

## 12. The visitor's address

The gateway works out the visitor's address and sends it in
`Overfin-Client-Addr`; anything a visitor sends under that name is dropped.

- **Directly connected:** the connection's address.
- **Behind a TCP proxy** (`OVERFIN_PROXY_PROTOCOL_FROM`): the PROXY protocol,
  v1 or v2, read only from those addresses, within 5 s; v1 `UNKNOWN`, v2
  `LOCAL` and other families keep the proxy's address. Exactly the header is
  read, nothing after it. v1's two addresses must be of the family it
  names (`TCP4`, `TCP6`), without a zone, with both ports; an IPv4 address
  written as IPv6 (`::ffff:…`, from a proxy listening on both) is the IPv4
  one, in v2 too.
- **Behind an HTTP proxy** (`OVERFIN_TRUSTED_PROXIES`): `X-Forwarded-For`,
  read from the right, the first address that isn't a trusted proxy.

A compromised gateway could lie about addresses; the backend's per-account
and per-email limits don't depend on them (rule 126).

## 13. Pages and limits

- **When the backend is away:** 503, `Retry-After: 20`, `no-store`. "Being
  updated" while a backend of another version is trying to connect (until
  two minutes after its last try), "Out of reach" otherwise; English or French by the visitor's first language; HTML with a
  20 s refresh and a strict CSP, or problem JSON under `/api/`. Neither page
  names what's behind the gateway.
- **Rate limit:** 50 requests a second per address (an IPv6 /64 counting as one), bursts of 400, in
  memory (100,000 addresses at most); 429 with `Retry-After: 5` past it.
- **Servers:** 10 s to read request headers, 2 minutes idle; no limit on
  writing (films). HSTS for two years over HTTPS.
- **The link:** 512-byte preamble line, 15 s handshake (egress included:
  4 s check, 3 s lookup, 5 s dial).

## 14. Guarantees, and their tests

Each line is a property a test can check. **Tested** names the test today;
**not yet** is a test to write.

| Guarantee | Test |
|---|---|
| Only the specification's operations and pages pass; `/admin*`, `/apps*`, `/link*`, `/api/*` not listed get 404 | `TestAllowlist` |
| A visitor's `Overfin-Check` and `Overfin-Client-Addr` never reach the backend | `TestInternalHeadersDropped` |
| `X-Forwarded-*`, `Forwarded`, `X-Real-Ip` are always the gateway's | `TestInternalHeadersDropped`, `TestClientAddress` |
| The visitor's address: spoofed headers ignored, the last untrusted hop behind a trusted proxy | `TestClientAddress` |
| PROXY v1 and v2 read exactly, garbage refused, only from configured addresses | `TestProxyHeaders`, `TestProxyListener`, `FuzzProxyHeader` |
| PROXY v1 `UNKNOWN`, v2 `LOCAL` and other families keep the proxy's address | `TestProxyHeadersWithoutVisitor` |
| A pairing proof binds both keys; a wrong code is refused | `TestPairAndReconnect`, `TestRefusals` |
| A code pairs once, every code used remembered | `TestCodeUsedOnce` |
| Eight backends pairing at once with one code: one pairs | `TestCodeUsedOnceAtOnce` |
| A code that can't be marked used pairs nothing, no key kept | `TestCodeSpentFirst` |
| A gateway whose key changed is refused | `TestGatewayKeyChanged` |
| Another version is refused first, and visitors are told it's updating while it tries | `TestRefusals` |
| A malformed or over-long preamble is refused | `TestPreambleRefused` |
| A second connection replaces the first | `TestSecondConnection` |
| Many large transfers at once over the link | `TestParallelTransfers` (8 × 16 MiB) |
| The link shares the HTTPS port by ALPN | `TestSharedPort` |
| `Overfin-Client-Addr` becomes the backend's remote address, a bad one ignored | `TestClientAddr` |
| Check-only answers: 200 becomes 204, no body | `TestCheckOnly` |
| Every session route needs a valid edge signature for that login; revoked logins refused | `TestSessionFilter`, `TestRevocations`, `TestSignVerify`, `TestPair` |
| The cache checks access once per session and image, remembers it, 404 on a refusal | `TestCacheServesWithCheck` |
| An access change, or the events stream starting, forgets every answer; one asked before isn't kept | `TestCacheForgetsAccess`, `TestAccessEvents`, `TestEventsAccess`, `TestAccessChanges` |
| The cache's 5-minute expiry, 16 MiB cap, `Range` passing through, emptied at start | `TestCacheAnswerExpires`, `TestCacheLimits` |
| Eviction keeps what's worth most | `TestCacheEviction` |
| Artwork fetched ahead is served only after a check | `TestFetchAhead` |
| Egress reaches only what the backend asked for, TLS end to end | `TestEgress` |
| Egress never dials loopback, link-local, unspecified or multicast addresses | `TestEgressTarget`, `TestRefused` (internal/outbound) |
| Reconnecting waits vary by a fifth | `TestJittered` |
| Egress refused for another version, an unpaired key, an unreachable address; the minute expires | `TestEgressRefusals` |
| Events: revocations numbered, resumed after a number; a ping when quiet; asked from past the last number ever given, or from one whose hash isn't the one heard, all again | `TestEventsRevokedAndPing` |
| After a reset, the gateway counts from the backend's numbers: none missed; it tells back the hash it heard | `TestRevocationsReset` |
| The down pages name nothing behind the gateway | `TestDownPages` |
| Rate limits, 429 with `Retry-After` | `TestLimits`, `TestLimitAnswer` |

## 15. Known gaps, and where this is going

Found while writing this, or raised by review (2026-11-13).

**Gaps found on 2026-11-13, fixed the same day:**
1. A profile's access change now reaches the gateway (the `access` event).
2. Every pairing code used is remembered, not only the last.
3. Egress checks the version first.
4. Egress stays within its 15 s deadline.
5. Egress refuses loopback, link-local, unspecified and multicast addresses.
6. "Being updated" lasts only while a backend of another version tries.
7. Reconnecting has jitter.

**Raised by a review of this document, fixed 2026-11-15:**
1. A code is spent before the backend's key is kept, and a code that can't
   be marked used pairs nothing (section 4): a crash or a failed write no
   longer leaves it usable twice. Many pairings at once with one code: one
   pairs (it was already one step under a lock; now tested).
2. A backend database restored from a backup no longer makes the gateway
   miss revocations: the `reset` event (section 8).
3. The link's limit of concurrent requests is stated (section 5), not left
   to Go's default.
4. The image cache keys sessions by the whole SHA-256 of the login cookie,
   not 8 bytes of it (section 10).

**Raised by a second review of the same document, done 2026-11-15:** egress
refuses IPv4's "this network", reserved block and broadcast too (section
11); wrong pairing codes are logged once, then once a minute (section 4);
sections 16 to 18 say what each part can do if taken over, how keys are
made and replaced, and the protocol's invariants with their tests. Using
the backend's link key for edge signatures too is safe: the signed message
starts `overfin-edge v1\n`, TLS 1.3's starts with 64 spaces, so neither
signature can pass as the other. Not done, by choice: a pairing code
expiring by itself (remove it from the gateway's settings once paired,
section 17); versions matched exactly (both reviews say to keep it).

**Request parsing tested, 2026-11-15** (`TestRequestParsing`, 29 cases):
paths with an encoded slash or a backslash (`/%2fadmin/`, `/\admin/`)
reached the backend before, as pages (Go's router there sent them to the
site, not the panel); they're refused now (section 6). A visitor's
`Upgrade` reached the backend too; it's dropped now. Over HTTP/2
(`TestRequestParsingHTTP2`, 25 cases, written frame by frame): all held.

**Fuzzed, 2026-11-15:** the preamble (7.3 M inputs), the pairing code
(2.7 M), the PROXY header (6.3 M), the events (4.9 M). The PROXY reader
took `TCP4` with an IPv6 address, and an address with a zone (`%eth0`);
both refused now (section 12), kept among the fuzzer's seeds. Each ran
90 s (the code 60 s) on Go 1.26.11, linux/amd64, 16 workers, from its
seeds; the corpus each built stays in Go's cache on that machine, not in
the repository (only the seeds, and inputs that failed, are kept there).
A count of inputs isn't a proof of coverage.

Checked, and already so: egress checks every address a name resolves to
(IPv4-mapped ones unwrapped) and dials the one checked (section 11);
files are written whole, renamed into place (section 4). Kept by choice:
private networks reachable by egress (paired servers on a LAN), versions
matched exactly, the 5-minute image answer cut short by `access`.

**Failure tests** (a review's list, 2026-11-13):

| Failure | Test | Result |
|---|---|---|
| The link cut while several transfers run | `TestLinkCutMidway` | every transfer ends at once, none hangs; requests work once the backend is back |
| One stream stalled (never read) beside others | `TestStalledStream` | small requests answer at once; 64 MiB beside it in well under 10 s |
| Hostile requests at the gateway: unlisted methods, climbing or encoded paths, 2 MiB headers, garbage | `TestHostileRequests` | plain answers (404, 401, 431, 400); `/admin`, `/apps`, `/link` never reach the backend (whose routes match case exactly: `/LINK/…` is a page there) |
| A federation request replayed 50 times at once | `TestReplayAtOnce` | exactly one accepted |
| A federation request signed before a restart, replayed after it | `TestReplayAfterRestart` | refused; one signed after the start passes |
| A server signing in another's name; the same nonce under two keys | `TestAnotherServersName` | refused; two separate requests |
| A pairing ended between two requests | `TestUnpairedMidway` | the next is answered as from a server no longer known; a replay is refused |
| The gateway failing five times in a row | `TestGatewayFailingRepeatedly` (long) | the backend comes back each time, about 11 s on average |
| 8 × 1 GiB through the link at once | `TestLongTransfersMemory` (long) | the heap stayed under 16 MiB |

Long tests run with `OVERFIN_LONG_TESTS=1 go test ./internal/link`, before
a release, and the fuzzers too, each a while (`go test -run '^$' -fuzz
'^FuzzPreamble$' -fuzztime 2m ./internal/link`; `FuzzCode` there,
`FuzzProxyHeader` and `FuzzEvents` in `./internal/gateway`). Their seeds
run with every `go test`; an input that fails is kept as one. The race detector finds nothing in `internal/link`,
`internal/gateway`, `internal/edge` and `msfp` (federation).

**The gateway stopped during real playback** (Firefox, the test install
split into a gateway and a backend, 2026-11-13):

| Outage | HLS (repackaged) | Direct play |
|---|---|---|
| 10 s | never stopped: played from its buffer, the failed requests retried, carried on | not tried |
| 40 s | the buffer ran out after about 36 s; the restart failed (no server), and the player showed "an error occurred", for good, even once the gateway was back | played on from its buffer; once the gateway was back, its progress reports got 503 while the backend reconnected, the playback was stopped and the video emptied, with no message |

So the protocol held, but the player didn't ride out an outage longer
than its buffer. **Fixed the same day** (site v0.99.5's player): a stream
that fails while the server can't be reached (no answer, or 502, 503,
504) has the player wait, its place kept, with "Connection lost. Trying
again…", ask every 3 s, and start over there once the server answers; a
direct stream that ends before its title did is a failure, not the end.
The same 40 s outage, again in Firefox: HLS paused at 252.9 s and went on
from 252.9 s; direct play paused at 349 s and went on from 349 s, each
some 15 s after the gateway was back (the backend reconnecting, then the
next try).

**No independent security review yet.** This document is meant as its
starting point.

## 16. Each part taken over

What an attacker holding one part can and can't do. The backend is the
application: it holds the accounts, the data, Jellyfin, the federation
keys. The gateway faces the internet and holds none of them.

| Taken over | Can | Can't |
|---|---|---|
| **The gateway** (its machine) | See visitors' traffic, their login cookies included (HTTPS ends there), and act as any visitor it saw; give the backend any visitor address (`Overfin-Client-Addr`); refuse or alter what it forwards; stop the site | Read the backend's data or keys; sign in without a visitor's cookie; reach the control panel or the apps' API (not on the link); act as the server towards paired servers (their TLS runs end to end inside egress) |
| **The backend** | Everything Overfin does; reach through its gateway any address it names, private ranges included (the gateway refuses only its own machine, link-local, unspecified, multicast and reserved addresses) | Make the gateway reach its own machine or a cloud's metadata service |
| **The gateway's key** (`gateway.key`) | Pose as the gateway to the backend, if it can also make the backend dial it (the backend dials the address it paired with) | Anything without that network position |
| **The backend's key** (`backend.key`) | Connect to the gateway as its backend, and so answer visitors; sign edge cookies the gateway accepts (the backend still checks every session itself) | Read the real backend's data |
| **An unused pairing code** | Pair its own backend with the gateway, before the real one: the real one is then refused (`code-used`), which shows | Use it after it's spent; pair with another gateway |
| **The backend's database** (a copy) | Read the data | Sign in: logins are kept as SHA-256 of their cookies |

## 17. Keys, from making to replacing

- **Made** at first start, when the file is missing: `gateway.key` and
  `backend.key`, Ed25519, PKCS#8 PEM, mode 0600, in `<data>/link/`. Never
  sent anywhere; only their public halves are pinned by the other side.
- **Pinned** at pairing, each side the other's public key; nothing else
  (address, name, certificate dates) is trusted.
- **Backed up** with the data folder. A copy restored on another machine is
  the same identity: protect backups like the keys themselves.
- **Replaced, the gateway:** reinstalled without its data, or its key
  deleted, it has a new key; the backend refuses it (`TestGatewayKeyChanged`)
  until paired again: `overfin-backend -pair <host:port>`, the code set on
  the gateway (`OVERFIN_PAIRING`). The backend keeps the old gateway pinned
  until the new pairing succeeds.
- **Replaced, the backend:** a new backend key (data folder lost, or the key
  deleted after a theft) is refused (`NO not-paired`) until paired again;
  pairing replaces the pinned key, and the old one is never accepted again.
- **A key stolen:** delete it on its side (a new one is made at the next
  start) and pair again: the stolen one stops working at once on the
  other side.
- **The pairing code:** spent at its first use, and remembered spent
  (`used-code`, a SHA-256 a line, a line per pairing ever made). Remove it
  from the gateway's settings once paired; an unused code stays valid while
  it's there.

## 18. Invariants

Each holds as the code stands, and has a test that fails if it breaks.

| Invariant | Tests |
|---|---|
| No visitor request reaches `/link`, `/admin` or `/apps`, whatever its method, case, encoding or path | `TestHostileRequests`, `TestRequestParsing`, `TestRequestParsingHTTP2` |
| The gateway and the backend read a request the same way: one spelling of a path passes, the backend gets it rewritten over HTTP/2, and both route with Go's `net/http`, same release | `TestRequestParsing`, `TestCanonicalPath` |
| A request hidden in another's body, two lengths or encodings, a target naming its own host, the gateway's own headers sent twice: answered plainly, or one clean request reaches the backend | `TestRequestParsing` |
| Over HTTP/2, pseudo-headers missing, twice, unknown, out of place, or headers HTTP/2 forbids: the stream refused or answered at the gateway | `TestRequestParsingHTTP2` |
| Whatever the backend's line, the gateway never reads past it, answers OK only to the paired backend's `link` at its version, and spends no code and keeps no key without the proof | `FuzzPreamble` |
| A pairing code reads back to its 20 bytes however it's typed; nothing else is one | `FuzzCode` |
| An events line that isn't one changes nothing; a revocation is kept and counted from; a reset starts the count again | `FuzzEvents` |
| No backend links without the pinned key or an unused code's proof bound to its key | `TestRefusals`, `TestPairAndReconnect`, `TestGatewayKeyChanged` |
| A pairing code pairs once, even tried at once, even after a crash or a failed write | `TestCodeUsedOnce`, `TestCodeUsedOnceAtOnce`, `TestCodeSpentFirst` |
| No access answer survives an access change; none asked before one is kept | `TestCacheForgetsAccess`, `TestAccessEvents`, `TestEventsAccess` |
| No revocation is missed across breaks, gateway restarts or a restored database | `TestEventsRevokedAndPing`, `TestRevocationsReset`, `TestRevocations` |
| Egress reaches only an address the backend asked for within the minute, never a refused range, dialing the address it checked | `TestEgress`, `TestEgressRefusals`, `TestEgressTarget` |
