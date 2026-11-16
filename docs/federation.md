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
plain 404. A compromised paired server can't reach your data or your
people.

Between servers on plain `http://`, requests are still signed, so neither
can be impersonated, but what they say (titles, tickets) can be read on
the way. The panel says so. The full rules are SECURITY.md's 94 to 100.

## Where it stands

Pairing, sharing, the titles on the site and playback both ways work, and
were tested between two servers in a real browser. Not done yet:

- watched and in-progress filters for series only a paired server has;
- one server numbering a series' episodes differently from the other.

Networks of many servers, run by a host, are being thought about
([ROADMAP.md](../ROADMAP.md), milestone 6); nothing is decided.
