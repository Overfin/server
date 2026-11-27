# Overfin JavaScript SDK

One ES module, no dependencies, no build step. It wraps Overfin's API and
carries the behavior every client needs, so your site only handles screens:

- **Login:** when the login lapses, the browser goes through it and comes
  back to the same page.
- **Errors:** every error the server answers is an `OverfinError` with a
  stable `type` (`pin-incorrect`, `step-up-required`, …) to branch on. A
  request that doesn't complete (no network, an answer cut short) rejects
  with the browser's own error instead: never an empty answer.
- **Paging:** `pages()` follows cursors for you, and keeps a shuffle's seed.
- **Choosing how to play:** a title's page gives its copies as MSFP's copy
  objects (facts only). `deviceEnv()` reads what the device plays (Media
  Source, `canPlayType`, `mediaCapabilities`, the screen's range) and
  `chooseDelivery()` picks the copy, its audio track and the cheapest
  delivery: the file itself, a remux, the audio alone converted, the
  picture converted, or MSFP's baseline when in doubt. `QUIRKS` lists the
  devices that play otherwise than they say (each with its date and how it
  was found); a play that fails in its first seconds, or plays with no
  audio, steps down once, remembered on the device (`fallbacks`, with a
  `reset()`). `copyLabel()`, `audioLabel()` and `subtitleLabel()` name
  copies and tracks in English or French, from their facts.
- **Playback:** `play()` chooses as above and asks for exactly that with
  MSFP Media's play request (`playRequest()`; Overfin makes it or refuses
  it, and a refused delivery or target steps down a level), builds the stream's
  addresses from the answer's ticket (`streamUrls()`), attaches the
  stream (with [hls.js](https://github.com/video-dev/hls.js), which your page
  provides, as a global `Hls` or the `Hls` option, where the browser lacks
  native HLS) and subtitles, reports progress (every `progressSeconds`
  of the answer, and at once on a pause or a resume), restarts a stream
  the server freed at the same place without a word, switches audio,
  copy and quality with a new play that replaces the old one
  (`replaces`), switches subtitles, and reports a takeover by another
  device.
- **Picture subtitles (PGS):** `decodePGS()` reads a track Overfin serves,
  and `PictureSubtitles` draws it on a canvas over the video, so the video
  is never converted for them.

```js
import { Overfin } from './overfin.js';

const overfin = new Overfin();

const me = await overfin.me();                 // account and active profile
const libraries = await overfin.libraries();
const { items } = await overfin.titles({ kind: 'movie', library: libraries[0].id, sort: 'addedAt', order: 'desc' });
img.src = overfin.imageUrl(items[0].images.primary, 'small');

const player = await overfin.play(videoElement, items[0].id, {
  onTakenOver: (device) => showMessage(`Playing on ${device}`),  // then player.continueHere()
  onError: (err) => showMessage(err.message),
});
player.setSubtitleTrack(player.playback.subtitleTracks[0].id);
// leaving the page:
await player.close();
```

Errors:

```js
try {
  await overfin.activateProfile(id, pin);
} catch (err) {
  if (err.type === 'pin-incorrect') …
  if (err.type === 'step-up-required') overfin.stepUp(err); // log in again, come back
}
```

Copy `overfin.js` (and `overfin.d.ts` for TypeScript) into your site.
[overfin-web](https://github.com/overfin/web) is built on it and shows every part in
use. The API itself is specified in [api/openapi.yaml](../../api/openapi.yaml).

Tests: `node --test sdk/js/overfin.test.js`.
