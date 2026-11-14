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
until the new pairing succeeds. On one machine (`OVERFIN_GATEWAY_ADDRESS`
and `OVERFIN_PAIRING_FILE` set), the backend makes the code itself and
writes it to a file only the two containers share; it deletes it once
paired.

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
4. compares the proof in constant time: `NO wrong-code` (counted in the
   log, no lockout);
5. keeps the backend's key (`backend.pub`) and the code's hash, answers
   with its own proof.

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
  4 MiB − 1 per stream and per connection; a ping when the connection is
  quiet for 15 s, and the connection dropped if a ping goes unanswered for
  10 s. Concurrent streams: Go's default.
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

**In order**, outermost first: drop internal headers → the visitor's
address → rate limit → allowlist → the session's first check (non-public
operations) → the image cache (`/api/v1/images/`) → the link.

**Headers.**
- Dropped from visitors: `Overfin-Check`, `Overfin-Client-Addr`,
  `X-Forwarded-For`, `X-Forwarded-Host`, `X-Forwarded-Proto`, `Forwarded`,
  `X-Real-Ip`.
- Set by the gateway: `Overfin-Client-Addr` (section 12);
  `X-Forwarded-Proto: https` when the visitor came over TLS.
- The backend sees `Host: backend` on every request.

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
| `GET /link/v1/events?since=<n>` | the events stream (section 8) |
| `GET /link/v1/artwork/{id}?width=…` | a title's TMDB artwork at that width, without a profile's check, for the cache's prefetch (section 10) |

## 8. Events

The backend tells the gateway what it must know as it happens: one JSON
object a line (`application/x-ndjson`), on a stream the gateway keeps open.

```
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
there** (rule 125): this check only keeps noise off the link.

## 10. The image cache

- **Cacheable:** `GET /api/v1/images/…`, no `Range`, from a signed-in
  visitor. Stored when the backend answered 200, `Cache-Control` with
  `immutable`, at most 16 MiB.
- **On disk:** `<data>/cache/<2 hex>/<SHA-256 of path?query>`, mode 0600;
  emptied at each start (the index of what's there is in memory).
- **Access, before every hit** (rule 128): the gateway asks the backend the
  same request with `Overfin-Check: 1`; the backend runs its usual check
  and answers without the bytes (204 for 200). A yes is kept 5 minutes, in
  memory, for that session (an 8-byte hash of the login cookie) and that
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
   asked for that address. It looks the name up (3 s) and refuses addresses
   it never reaches for its backend: loopback (its own machine), link-local
   (a cloud's metadata service among them), unspecified, multicast; private
   ranges stay, since paired servers may be on a LAN. It dials the address
   it checked (5 s), never looking it up again, and answers `OK`. All of it
   within the handshake's 15 s.
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
  read, nothing after it.
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
- **Rate limit:** 50 requests a second per address, bursts of 400, in
  memory; 429 with `Retry-After: 5` past it.
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
| A visitor's `Overfin-Check` and `Overfin-Client-Addr` never reach the backend | not yet |
| `X-Forwarded-*`, `Forwarded`, `X-Real-Ip` are always the gateway's | `TestClientAddress` (in part) |
| The visitor's address: spoofed headers ignored, the last untrusted hop behind a trusted proxy | `TestClientAddress` |
| PROXY v1 and v2 read exactly, garbage refused, only from configured addresses | `TestProxyHeaders`, `TestProxyListener` |
| PROXY v1 `UNKNOWN`, v2 `LOCAL` keep the proxy's address | not yet |
| A pairing proof binds both keys; a wrong code is refused | `TestPairAndReconnect`, `TestRefusals` |
| A code pairs once, every code used remembered | `TestCodeUsedOnce` |
| A gateway whose key changed is refused | `TestGatewayKeyChanged` |
| Another version is refused first, and visitors are told it's updating while it tries | `TestRefusals` |
| A malformed or over-long preamble is refused | not yet |
| A second connection replaces the first | not yet |
| Many large transfers at once over the link | `TestParallelTransfers` (8 × 16 MiB) |
| The link shares the HTTPS port by ALPN | `TestSharedPort` |
| `Overfin-Client-Addr` becomes the backend's remote address, a bad one ignored | `TestClientAddr` |
| Check-only answers: 200 becomes 204, no body | not yet |
| Every session route needs a valid edge signature for that login; revoked logins refused | `TestSessionFilter`, `TestRevocations`, `TestSignVerify`, `TestPair` |
| The cache checks access once per session and image, remembers it, 404 on a refusal | `TestCacheServesWithCheck` |
| An access change, or the events stream starting, forgets every answer; one asked before isn't kept | `TestCacheForgetsAccess`, `TestAccessEvents`, `TestEventsAccess`, `TestAccessChanges` |
| The cache's 5-minute expiry, 16 MiB cap, `Range` passing through, emptied at start | not yet |
| Eviction keeps what's worth most | `TestCacheEviction` |
| Artwork fetched ahead is served only after a check | `TestFetchAhead` |
| Egress reaches only what the backend asked for, TLS end to end | `TestEgress` |
| Egress never dials loopback, link-local, unspecified or multicast addresses | `TestUnreached` |
| Reconnecting waits vary by a fifth | `TestJittered` |
| Egress refused for another version, an unpaired key, an unreachable address; the minute expires | not yet |
| Events: revocations resume after a break; ping after 15 s | not yet (the backend's side) |
| The down pages name nothing behind the gateway | `TestDownPages` |
| Rate limits | `TestLimits` |

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

**Not tested yet:** the lines marked "not yet" in section 14, and the
failure tests a review proposed:
- the gateway killed during HLS and direct playback;
- the link cut while images, a direct stream, an HLS conversion and a
  federation playback all run;
- one HTTP/2 stream stalled while others run;
- malformed and oversized requests at the gateway;
- federation messages replayed concurrently with the original; an unpairing
  while requests are in flight; a peer reusing another's context;
- long concurrent streaming (memory, latency);
- repeated gateway failures and reconnecting.

**No independent security review yet.** This document is meant as its
starting point.
