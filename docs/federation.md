# Federation: sharing libraries between servers

Pair your server with a friend's, and each of you picks which of
your libraries the other gets. Their films and series then show on your
site beside your own, and your people play them there, with the sign-in,
profiles, history and kids limits they already have. Nobody needs an
account on the other server, and nobody has to install anything.

It works between servers whose operators trust each other: each pairing is
made by hand, one server with one other, or among friends through a
[group](#groups). There is no directory, no open network, and nothing is
shared until both of you say so.

**The other server doesn't have to be Overfin.** Federation in Overfin is
[MSFP](https://github.com/msfp-io/msfp-spec), the Media Server Federation
Protocol: an open protocol of its own, with a public specification, test
vectors and a conformance suite. Any media server that implements it
can pair with yours. Today that's other Overfin servers; MSFP is built
so that other servers can join, and Overfin's own implementation is an
engine others can use ([below](#building-a-server-that-pairs)). Choosing
Overfin doesn't tie your friends to Overfin.

## What your people see

- **Their titles beside yours.** Each library you take from a paired
  server gets its own menu entry, named after it and the server ("Films
  (Alex's server)"); you can rename it. With categories (Films, Anime,
  Kids…), their titles go into yours by the same rules: kind, genres,
  language, keywords, age. Search, genres, counts and title pages include
  them.
- **One title, wherever it is.** A film you both have is one title, not
  two, with every copy of it: yours and the other server's. The player
  chooses among them for the device, as it does for yours: your copies
  that play as they are first, then a paired server's that does, then
  yours converted; never one without the person's language when another
  has it.
- **Playing as from your own.** The other server's titles play with
  their audio tracks, subtitles, skip markers and seek-bar previews, all
  through your server.
- **Everything personal stays with you.** What they watch, where they
  stopped, their favorites, their list, their wishes: all kept on your
  server only. Kids profiles see a paired server's titles only within
  their age, as for yours.

## What the other server sees

- **Never your people.** A browser never connects to the other server: a
  paired server's title plays through yours, which fetches it with a
  ticket the browser never sees. The other server sees your server only
  (its remote gateway, if it has one), never a viewer's address, browser, account or name.
- **Titles, not files.** Servers tell each other their titles by their
  TMDB ID, the IMDb and TheTVDB IDs TMDB gives them, and, for a file
  holding two episodes, that it holds both; and each title's copies as
  facts read from the files (length, container, picture size and range,
  codecs, audio and subtitle tracks with their languages, chapters, skip
  markers): never file names, paths, track or version names, or people. A title known here only by
  another site's ID than TMDB's isn't shared. The IDs a server gives
  others (its libraries', its versions', its playbacks' and their
  tickets) are its own, and tell nothing of its files or of Jellyfin.
- **Only what's yours to share.** A server only ever offers its own
  libraries. What it gets from one paired server is never passed on to
  another.

## Pairing two servers

You need your site's address set, in HTTPS (`https://host` as `site.url`
in the configuration, or an HTTPS domain): it's how the other server
reaches yours. Servers pair over HTTPS only; a site on plain `http://`
has no federation (the panel says so), and a code for an `http://`
address is refused. A server nothing can reach needs no address, and
pairs too, to watch only (see [A server that can't be
called](#a-server-that-cant-be-called)). In the control panel, under
**Federation**:

1. One of you clicks **New code** and sends it to the other however you
   like, or from the panel by email (**Email it to**, once emails are set
   up under **Server**). A code starts with `msfp-pair-0.`. It works once and for a day,
   unless you choose otherwise: up to 100 servers, for up to 30 days. Each
   server that uses it still waits for you to accept it.
2. The other pastes it under **Use a code**.
3. The first server now lists the second. Before accepting, compare the
   **fingerprints** both panels show, by phone or a message you trust: if
   they differ, the code was changed on the way, so refuse it.
4. Once accepted, each of you opens **Sharing** and picks, library by
   library, what you **offer** and what you **want**. Only what one offers and the other wants is shared. A pairing
   where one side offers nothing works too.

Other servers know your libraries by IDs of their own, made when you
first offer each one; never Jellyfin's.

**Pairings made before November 2026 are gone.** Overfin then spoke a
first, test version of the protocol, and the update that followed removed
the pairings made under it. Pair again with a new code: your own
libraries, and the IDs other servers know them by, stay.

**MSFP is now version 0**, a draft until version 1 (it was numbered 1
before). Servers of the former numbering and of version 0 can't talk: a
pairing with one shows "It speaks another version of the federation
protocol" until it's updated too, and works again then. Codes made before
the update no longer work: make new ones.

Ending a pairing (**End**) is one click, and it ends on both servers at once.

## Your server's key

Each server is known to the others by its key, a pair of numbers made on
its first start: the fingerprint you compared when pairing is the key's.
Overfin also makes, in advance, the key it will move to next, and tells
paired servers which one it is (only a fingerprint of it travels, never
the key). Under **Federation**, **This server** shows that next key's
fingerprint.

**Keep the next key offline.** **Export the next key** shows it once, as
text starting with `msfp-next-key-1.`, with its fingerprint. Keep it
somewhere safe away from the server (a password manager, or on paper),
then **Delete it from this server**. Someone who copies your server's
storage then gets its key, but can't move your paired servers to a key of
theirs. Changing the key later asks for the copy you kept.

**Changing the key.** **Change this server's key** moves your server to
its next key, and makes a new next key. Paired servers are told at once,
and move by themselves: they know it as the next one, from pairing or
your last change. A server that knows no next key of yours (one paired
before Overfin required it in pairing, which once learnt it from a later
exchange instead) asks its operator to confirm the new fingerprint with
you first. A paired server never moves back to a key it moved from.
Until a server moved, yours keeps the old key for it, and tells it again
at each contact and at least daily; after 30 days the old key is deleted,
and a server still on it shows **Pair again**: remove it and pair again.

**If your server's key is stolen** (the server or a backup copied, say),
use **This server's key was stolen**. Every paired server is told at once
that the key is revoked, and stops hearing it. With the next key (on the
server, or pasted from the copy you kept), your server then moves to it,
and the paired servers follow by themselves: the thief can't do the same,
not holding the next key. Without it (or if you tick **Don't move to the
next key**, because the thief may hold it too), your server gets a new
key, and every pairing ends: pair again with each server, with new codes
and comparing fingerprints. For 30 days, a server it was paired with that
calls yours is answered, under the stolen key, only that the key is
revoked: one your first warning didn't reach (it was away, or can't be
called) learns it so, and stops hearing the thief.

**When a paired server changes its key**, the list shows it:

- **Current**: nothing to do. Its next key is known (from pairing, or its
  last change).
- **To confirm**: it moved to a key it hadn't announced. Call its operator
  and compare the new fingerprint, then **Confirm key**; if they differ,
  end the pairing.
- A warning says when someone tried to move it to another key than the
  one it announced: refused, and its own operator should hear of it.
- **Suspended**: it said its key was stolen. Nothing is shared with it
  until it moves to the key it announced, and the stolen key is never
  heard again; after 30 days without that, it is gone, and you remove
  it.
- **Changing**: it hasn't moved to your server's new key yet.

## Changing your server's address

Your server's address is your site's (`site.url`, or its HTTPS domain).
Change it in the configuration and restart, as for any setting: paired
servers follow by themselves, without pairing again.

- **Paired servers are told once the new address answers.** Your
  server first calls itself at the new address, through the same rules
  as any call to a paired server (below), and tells paired servers only
  once it gets its own answer back there: until then, it keeps naming the
  old address, tries again (sooner at first, then every 5 minutes), and
  the panel shows the move as waiting for the new address to answer. Your
  server must therefore reach itself at its new address (not on loopback,
  which is refused here too).
- **Keep the old address working for a while.** Paired servers keep
  calling it until they've checked the new one, so serve both (two names
  on your reverse proxy, or the old port still open) until the panel
  says every server moved. Overfin accepts calls made for either address
  for 30 days, or until every paired server has moved, whichever comes
  first; after that, the old one only.
- **What the panel shows.** Under **Federation**, **This server** says
  the address changed, and how many paired servers moved. A server that
  still uses the old one says so in the list, and a warning shows after
  3 days: ask its operator whether it can reach the new address.
- **On the other side**, nothing to do. A paired server that says it
  moved isn't believed on its word: yours calls it at its new address,
  and moves to it only once the server there answers with the key it
  knows. Until then, and if that fails, yours keeps using the old
  address, tries again every hour, and warns you after a day. The
  addresses Overfin never connects to (below) apply to a new address
  too: it's refused, and the old one stays.
- **Playback** goes on through the move: a playback started before keeps
  its old address until it ends, and a new one's streams are on the
  address the paired server called.
- **Certificates.** During the move, your server names to paired servers
  the certificates of both addresses, until the move ends.

Moving back works the same way. Someone holding your server's key could
move your paired servers to an address of theirs: that's one more reason
to revoke a stolen key at once (above).

## A server that can't be called

Some servers can reach out, but nothing can reach them: at home behind a
router, without a forwarded port, or behind your internet provider's
shared address. Such a server can still pair, to **watch** what other
servers share with it; it **shares nothing**.

Under **Federation**, turn on **This server can't be called**. It works
without a site address, too. Then:

- **Pairing.** Your server makes no codes: ask the other operator for one,
  and paste it under **Use a code**. Their server lists yours as one that
  can't be called.
- **Your server asks, they never call.** Paired servers never connect to
  yours, for anything. Yours asks each of them by itself: every 20 seconds
  while their operator hasn't accepted yet, every 5 minutes once paired,
  every hour once a server says it no longer knows yours. That's how it
  learns it was accepted, that their key changed (it follows by itself,
  as above, within 5 minutes or so) or was stolen, and where they moved.
- **What you watch.** Their titles show on your site and play as from any
  paired server. Of yours, they get nothing: what you offer in **Sharing**
  is kept for when your server can be called again.
- **Its address** becomes one that leads nowhere,
  `https://msfp-` and 26 letters and digits, then `.invalid`, made once
  for your server. Other servers know it by its key, as always; nothing
  ever connects to that address, and it never points into their network.
- **A hint.** When a paired server says its calls to yours couldn't
  connect, its row says so, and suggests this setting. Nothing changes by
  itself: it's your choice.

Turn it off once your server can be reached (a port forwarded, a domain
pointed at it): paired servers are told its site address at once, check
that your server answers there under its key, and only then call it
again, as for a change of address (above). Until they can reach it, they
go on as before.

**On the other side**, a server that can't be called shows as such in
your list: "it can watch what you share with it, and can share nothing
with you". Its libraries can't be picked in **Sharing**, and **Check**
isn't offered: it asks yours.

## Groups

A group lets friends' servers pair with each other without a code for
each pair. Create one under **Federation**, **Groups**: you are its
owner. Then:

1. **Invite with a code.** **Invite** (or **New code**, choosing the
   group under **Leads to**) makes a code for the group. A server that
   uses it pairs with yours, and its operator's panel says what the code
   leads to ("Join the group Friends?"). You accept it after comparing
   fingerprints, as for any code.
2. **It joins.** Once accepted, it's a member: it gets the group's list
   from your server, then asks every other member to pair.
3. **Each member answers for itself.** A request waits under the group's
   **Requests** for its operator: **Pair** or **Refuse**. No fingerprint
   to compare: the group vouches for its members. A refused server isn't
   told, and its requests are dropped until you **Hear it again**; you can
   also ask a member yourself (**Ask to pair**). In **Sharing**, **Accept
   new members by itself** lets your server pair by itself with a member
   that has been in the group a few days (3, a group setting).

**What members see:** each member's name, fingerprint and role, who
invited it and when, and whether you're paired with it. What a server
offers, and its address, stay between servers paired with each other.

**Sharing with a group.** In the group's **Sharing**, what you offer the
group goes to every member you're paired with through it, as if the
group were one server, beside what you share with a member by hand. The
limits there are one pool, for all the group's members together: a
playback counts against the pairing's own limits first, then against the
pools of its groups, the first with room. Two servers in several groups
together are paired once.

**Roles.** The owner names and removes admins, names an heir, changes the
group's name and settings, lifts bans, and may give the group away or
dissolve it. Admins and the owner invite and ban (an admin bans members
only). If the owner gives the group away, it goes to whom it chooses, or
by default to its heir, else the oldest admin, else the oldest member.
Settings: how many days before members accept a newcomer by themselves (0
to 30), and how many servers each inviter may bring in a day (1 to 100),
so a code that leaks can't flood the group.

**Leaving, blocking, banning, dissolving** (in the group's window, and a
member's **More**):

- **Leave**: your pairings made through the group stay unless you tick
  them; one only the group held becomes a pairing by hand, keeping what
  the group gave it.
- **Block** is yours alone, and nobody is told: your server ends its
  pairing with that server, whatever made it, and ignores it in all your
  groups. It can block the servers that one invited too, and theirs.
- **Ban** acts for the whole group: the server is out for every member,
  told why if a reason is given, and pairings it held only through the
  group end. Only the owner lifts a ban.
- **Dissolve** (the owner): every member removes the group, keeping the
  pairings it made or ending them, as the owner chooses.

Nothing about a group lives anywhere but on its members' servers: each
keeps its own copy of the group's signed list and passes changes on.
There is no central server, and a group needs none of its members to stay
up.

A server already paired with yours uses your group's code the same way:
it joins through your pairing, and waits under the group's **Requests**
for you to **Accept** it (or **Refuse**: nothing is sent, and it gives up
on its side, or after 30 days).

## Limits

Each pairing has its own limits, set by the server that shares:

- **Streams**: how many of the other server's people may play from yours
  at the same time (2 at first).
- **Transcodes**: of those, how many may have their picture converted,
  which is what costs (1 at first). A stream whose picture is copied
  (repackaged, or only its sound converted) counts as a stream only.
- **Bitrate**: an optional ceiling.
- **Downloads**: how many of your files the other server may download at
  the same time to keep (none at first), how many a day, and how fast.
- **Scans**: how many of your files a day the other server may read for
  its own checks (none at first).

Subtitles are never burned into the picture for a paired server unless
you turn it on in the configuration (`federation.burn_in`, off); DVD and
DVB subtitles, which are pictures only a converted picture could carry,
aren't served apart.

Under **Features & limits**, **Transcodes kept from other servers** keeps
some of your transcodes for your own people, whatever the pairings allow
together (1 at first). The other server learns your limits and doesn't
offer its people what yours would refuse.

Besides those, Overfin protects itself from a paired server that calls
too much, by fault or on purpose, with fixed limits nobody needs to set:
federation calls per address (an IPv6 /64 counting as one) before any signature (50 a second, bursts
of 400), per paired server (20 a second, bursts of 200), for all of them
together (200 a second, bursts of 2,000), `play` per paired server (30 a
minute, 20 at once), and requests at once (8 per paired server, 64 in
all, each with a short queue). Media has its own: 8 fetches at once per
playback and 64 per paired server, on both sides. Normal use (catalog
reads, playback, seek-bar previews) stays far below them. Past one, the
other server is told to wait a moment, signed, and waits; Overfin waits
the same way when another server tells it to, and, when one doesn't
answer, asks it less often by itself, up to once an hour.

## Security

Servers never trust each other by default. Each proves who it is with the
key exchanged when pairing, never with a web certificate, a name or an
address alone. Every request and answer between them is signed (Ed25519,
a nonce used once, within 5 minutes), over HTTPS; a server that isn't
paired gets a plain 404. Nonces are kept in memory, so a request signed
before a server started is refused: a paired server whose clock runs
behind is refused for that long after each start (keep both clocks set by
the network). A compromised paired server can't reach your data or your
people. The full rules are SECURITY.md's 94 to 100, 136, for keys, 137
and 138, for addresses, 139, and for servers that can't be called, 140.

Paired servers say, each time they check on each other, which versions of
the protocol and of its parts (catalog, playback, media, video) they
speak, and use those both speak. A server that speaks none of yours shows
in the panel as speaking another version; it can't share with yours
until one of you updates: until then, the two only check on each other.

## Which addresses Overfin connects to

A paired server tells yours where to reach it: in its code, its pairing
request, an address change, and its streams' URLs. Overfin never connects,
for a paired server, to:

- its own machine (loopback: `127.0.0.0/8`, `::1`);
- link-local addresses (`169.254.0.0/16`, `fe80::/10`), where a cloud's
  metadata service answers;
- unspecified, multicast and reserved addresses (`0.0.0.0/8`,
  `240.0.0.0/4`, broadcast).

An IPv6 address that maps an IPv4 one (`::ffff:127.0.0.1`) counts as that
IPv4 address. Private ranges (`10.0.0.0/8`, `192.168.0.0/16`, `fd00::/8`…)
are fine: two servers on a LAN pair as usual. A name is looked up once per
connection, every address it has must pass (one refused refuses the name),
and Overfin connects to the address it checked, never looking the name up
again. With a remote gateway, the gateway makes these connections and
checks the same way; without one, the backend makes them from its own
machine. Redirects are never followed.

To pair with a server on the same machine, allow loopback (also on the
remote gateway, if you run one). Link-local addresses stay refused either way:

```toml
[federation]
allow_loopback = true
```

The same applies when Overfin reads its own certificate at its own address
(below): if that address is this machine's (an `/etc/hosts` line, say),
name the certificate in the configuration instead, or allow loopback.

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

## Testing two servers on one machine

Plain `http://` between servers is outside the protocol, but two servers
tried on one machine may have no certificate. For that only, a test
setting lets a server pair over plain http, its own address and those it
is given; with it on, the panel says so. Never on a server in use:

```toml
[federation]
test_plain_http = true
allow_loopback = true   # both on this machine
```

## Building a server that pairs

Building your own server that pairs with Overfin? Servers speak MSFP, the
Media Server Federation Protocol, version 0 (its core, and the parts
catalog, playback, media, video and groups; Overfin has no music): its
specification and test vectors are at
[github.com/msfp-io/msfp-spec](https://github.com/msfp-io/msfp-spec).
Overfin follows version 0 (a draft; numbered 1 before 2026-11-20). It does key rotation and revocation
(`rotate`, `revoke`, the next key commitment required in `pair` both
ways; a `hello`'s is checked and ignored; the 30 days of revocation-only
answers after a stolen key with no next key), address changes (core
section 13) and
servers that can't be called (core section 7.5: `inbound`, the scheduled
`hello`s, `knownKey`, the rotations and revocations pulled in `hello`
answers, `callsFailed`), and of the shared parts: episodes by TMDB's
own IDs, `with` and `ids` sent and read (a paired server's `with` marks a
file's episodes watched together; its `ids` aren't kept), `versionId` in
`play`, track IDs without leading zeros, WebVTT rebuilt and PGS checked
both ways (an empty part, for a stretch with no pictures, served and
relayed as such), any change of what a title carries, `ids` and `with`
included, sent again in changes readings. A paired server's discovery
document (core section 11) is read when it pairs or moves, so a server
under a path pairs with it; Overfin's own calls are always at
`/api/v1/federation/`, and it serves its own document at
`/.well-known/msfp` only when its operator turns it on (off by default:
a server that serves it tells anyone it federates):

```toml
[federation]
discovery = true
```

Overfin's federation is the MSFP engine for Go,
[msfp-io/msfp-go](https://github.com/msfp-io/msfp-go) (package `msfp`,
with `wire`, the protocol's toolkit): Overfin
hosts it, with its records in Overfin's database, its own libraries and
titles, its playback and media, and its connections to other servers
(through the remote gateway, when there's one). Each copy of a title goes
to paired servers as MSFP's copy object, facts only; the viewer's server
chooses one, its audio and its delivery (the file itself, a remux, a
transcode, or MSFP's baseline), and Overfin makes exactly that through
the same executor as for its own players, or refuses it with MSFP's
code. Text subtitles are served in stretches and whole
(`subtitles/<id>/whole`), PGS in stretches; ASS and SSA as ASS, and
fonts, aren't served yet (their text comes as WebVTT). Seek-bar previews
are Overfin's own, under a playback's ticket or, before a play, a
previews ticket.

## Where it stands

Pairing, sharing, the titles on the site and playback both ways work, and
were tested between two servers in a real browser, and between two servers
over the internet. Other servers than Overfin can pair once they implement
MSFP; none does yet. Not done yet:

- watched and in-progress filters for series only a paired server has;
- one server numbering a series' episodes differently from the other:
  a series in TheTVDB's order is joined to TMDB's through its episodes'
  IDs, and an episode that matches none is left out.
