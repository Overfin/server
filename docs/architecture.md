# How Overfin is built

Overfin runs as two parts, from two images of the same version.

- **The gateway** (`ghcr.io/overfin/overfin-gateway`, about 13 MB) is the
  only part the internet reaches. It handles HTTPS, passes on only the
  routes Overfin's API lists, turns away requests without a valid session,
  limits how fast one address may ask, keeps a cache of images, and shows
  its own page when the backend is away or being updated.
- **The backend** (`ghcr.io/overfin/overfin-backend`) is everything else:
  Overfin's data and secrets, its API, the control panel, the setup
  wizard, the apps' API, and Overfin's own Jellyfin with Overfin's plugin
  for it, which the backend runs and keeps running. It sits next to your
  media and your GPU.

On one machine, both run from one compose file and pair by themselves.
They can also run on two machines: the gateway on a small server facing
the internet (a VPS), the backend at home with the media.

## Why two parts

- **Nothing worth taking faces the internet.** The gateway holds no
  accounts, no secrets, nothing about who watches what: its disk has its own
  key, its backend's, its certificates, and a cache of images; its logs say
  nothing about visitors. A gateway on a rented server you don't fully
  trust exposes none of your users' data.
- **Nothing at home is opened.** The backend connects out to its gateway
  and keeps that connection open; the gateway sends visitors' requests
  back over it. No port forwarding, and it works behind CGNAT. Paired
  servers (federation) talk to your gateway only, and the backend reaches
  them through it.
- **The heavy work stays next to the media.** Video, images, subtitles and
  Jellyfin run where the disks and the GPU are; the gateway only passes
  bytes along, and keeps the images many people ask for.
- **A flaw in the part facing the internet reaches little.** The gateway
  is a small program in its own container, on a minimal image; the
  backend checks every session itself, as if the gateway weren't there.

## How the two talk

The backend opens a TLS connection to the gateway and keeps it open: the
**link**. Each side has its own key; they pair once with a one-time code
the backend makes, each proves it knows the code, and from then on each
accepts only the other's key, with no certificate authority involved.
Over the link the roles flip: the gateway sends HTTP/2 requests, the
backend answers them. The link shares the gateway's HTTPS port, told apart
by its protocol name, so nothing else needs opening.

A second kind of connection, also opened by the backend, is how it
reaches paired servers: the gateway connects there for it and passes the
bytes, without seeing inside (TLS runs end to end).

The backend also tells the gateway what it needs to know as it happens:
sign-ins that ended (the gateway refuses them at once), and new titles
(the gateway fetches their pictures ahead, so the first visitor doesn't
wait).

## Sessions

Every request to the API that needs a session carries, besides the
sign-in cookie, the backend's signature of it. The gateway checks that
signature with the backend's key and sends nothing across without it. The
backend still checks every session itself: the gateway's check only keeps
noise off the link.

## The image cache

The gateway keeps images on its disk, within a size you set and leaving a
tenth of the disk free. Before serving a cached image, it asks the backend
whether this session may see it (the backend runs its usual check and
answers without the bytes), and remembers the answer for a few minutes.
What's kept is what's worth most: what's asked for often, slow to fetch
and small, with what many sessions ask for (the home page's pictures)
kept longest.

## The control panel

The control panel and the setup wizard are the backend's, on its own port
(8081), and answer only private addresses: your LAN or a VPN. They never
go through the gateway. The panel has its own login, apart from the
site's accounts.

## The protocol

Every message between the two, each check and limit, the guarantees a test
can check, and what's known missing: [link-protocol.md](link-protocol.md).

---

[← Back to the README](../README.md)
