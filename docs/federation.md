# Federation: sharing libraries between servers

Pair your Overfin server with a friend's, and each of you picks which of
your libraries the other gets. Their films and series then show on your
site beside your own, and your people play them there, with the sign-in,
profiles, history and kids limits they already have. Nobody needs an
account on the other server, and nobody has to install anything.

It works between servers whose operators trust each other: each pairing is
made by hand, one server with one other. There is no directory, no open
network, and nothing is shared until both of you say so.

## What your people see

- **Their titles beside yours.** Each library you take from a paired
  server gets its own menu entry, named after it and the server ("Films
  (Alex's server)"); you can rename it. With categories (Films, Anime,
  Kids…), their titles go into yours by the same rules: kind, genres,
  language, keywords, age. Search, genres, counts and title pages include
  them.
- **One title, wherever it is.** A film you both have is one title, not
  two. It plays from your copy, unless only the other server has one in
  the person's language: Overfin never picks a version without their
  language when another has it.
- **Everything personal stays with you.** What they watch, where they
  stopped, their favorites, their list, their wishes: all kept on your
  server only. Kids profiles see a paired server's titles only within
  their age, as for yours.

## What the other server sees

- **Never your people.** A browser never connects to the other server: a
  paired server's title plays through yours, which fetches it with a
  ticket the browser never sees. The other server sees your server only
  (its gateway), never a viewer's address, browser, account or name.
- **Titles, not files.** Servers tell each other their titles by their
  TMDB ID, with their audio and subtitle languages: never file names,
  paths or people.
- **Only what's yours to share.** A server only ever offers its own
  libraries. What it gets from one paired server is never passed on to
  another.

## Pairing two servers

You need your site's address set (`http(s)://host` in the configuration,
or an HTTPS domain): it's how the other server reaches yours. In the
control panel, under **Federation**:

1. One of you clicks **New code** and sends it to the other however you
   like. A code works once, within 24 hours.
2. The other pastes it under **Use a code**.
3. The first server now lists the second. Before accepting, compare the
   **fingerprints** both panels show, by phone or a message you trust: if
   they differ, the code was changed on the way, so refuse it.
4. Once accepted, each of you opens **Sharing** and picks, library by
   library, what you **offer** and what you **want**. Only what one offers and the other wants is shared. A pairing
   where one side offers nothing works too.

Ending a pairing (**End**) is one click, and it ends on both servers at once.

## Limits

Each pairing has its own limits, set by the server that shares:

- **Streams**: how many of the other server's people may play from yours
  at the same time (2 at first).
- **Transcodes**: of those, how many may be re-encoded, which is what
  costs (1 at first).
- **Bitrate**: an optional ceiling.

Under **Features & limits**, **Transcodes kept from other servers** keeps
some of your transcodes for your own people, whatever the pairings allow
together (1 at first). The other server learns your limits and doesn't
offer its people what yours would refuse.

## Security

Servers never trust each other by default. Each proves who it is with the
key exchanged when pairing, never with a web certificate, a name or an
address alone. Every request and answer between them is signed (Ed25519,
a nonce used once, within 5 minutes); a server that isn't paired gets a
plain 404. Nonces are kept in memory, so a request signed before a server
started is refused: a paired server whose clock runs behind is refused for
that long after each start (keep both clocks set by the network). A compromised paired server can't reach your data or your
people.

Between servers on plain `http://`, requests are still signed, so neither
can be impersonated, but what they say (titles, tickets) can be read on
the way. The panel says so. The full rules are SECURITY.md's 94 to 100.

## HTTPS certificates

Use HTTPS when you can: the media and its ticket have only HTTPS to
protect them. A certificate from a public authority (Let's Encrypt, as
Overfin's own HTTPS domain or Caddy gets one) needs nothing more.

A certificate of your own (self-signed, or from your own authority, such
as Caddy's `tls internal`) works too, without anything to set in most
setups. Your server names its certificate to paired servers in its signed
hellos, and they accept that one, besides any publicly trusted one. To
know which certificate your address serves, even behind a reverse proxy,
Overfin connects to its own address now and then, and reads it there. A
renewed certificate is named again within a minute, and the other server
asks for it again by itself.

If your server can't reach its own address (a home router without hairpin
NAT), it can't tell, and other servers can't reach yours over HTTPS with a
certificate of your own. Name it in the configuration then, by its pin,
which you get from the certificate file:

```sh
openssl x509 -in cert.pem -pubkey -noout | openssl pkey -pubin -outform der \
  | openssl dgst -sha256 -binary | basenc --base64url | tr -d '='
```

```toml
[site]
certificates = ["<the 43 characters it prints>"]
```

List up to 8, the current one and the next, when you renew with a new key.
Overfin then names these and looks at nothing.

What this protects: before your certificate is named, someone between
the two servers could stand in with a certificate of theirs for the
pairing and hello calls only, which are signed and carry nothing secret:
they can't change what is said, nor have their own certificate named.
Every other call, and the media, wants a publicly trusted certificate or
the one named. Overfin trusts what it finds at its own address, so
someone who could redirect your server's own connection to itself (by
its DNS, say) could have their certificate named; naming it in the
configuration avoids that.

Building your own server that pairs with Overfin? Servers speak MSFP, the
Media Server Federation Protocol: its specification and test vectors are
at [github.com/C0PT3R/msfp-spec](https://github.com/C0PT3R/msfp-spec).

## Where it stands

Pairing, sharing, the titles on the site and playback both ways work, and
were tested between two servers in a real browser. Not done yet:

- watched and in-progress filters for series only a paired server has;
- one server numbering a series' episodes differently from the other.

Networks of many servers, run by a host, are being thought about
([ROADMAP.md](../ROADMAP.md), milestone 6); nothing is decided.
