// Tests for overfin.js, with Node's own test runner:
//   node --test sdk/js/overfin.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Overfin, OverfinError, Player, capabilities } from './overfin.js';

// fakeServer answers fetch() from a table of handlers, and records calls.
function fakeServer(handlers) {
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ method: init.method, url, body });
    const key = `${init.method} ${url.split('?')[0]}`;
    const handler = handlers[key];
    const [status, data] = handler ? handler(url, body) : [404, { type: 'not-found', title: 'Not found', status: 404 }];
    return {
      status,
      ok: status >= 200 && status < 300,
      json: async () => data,
    };
  };
  return calls;
}

globalThis.location = { pathname: '/', search: '', hash: '#/item/item_1', href: '' };

test('errors are typed problems', async () => {
  fakeServer({ 'POST /api/v1/profiles/p1/activate': () => [403, { type: 'pin-incorrect', title: 'Wrong PIN', status: 403 }] });
  const overfin = new Overfin();
  await assert.rejects(overfin.activateProfile('p1', '0000'), (err) => {
    assert.ok(err instanceof OverfinError);
    assert.equal(err.type, 'pin-incorrect');
    assert.equal(err.status, 403);
    return true;
  });
});

test("a success whose body can't be read fails; an error's still says its status", async () => {
  globalThis.fetch = async (url) => ({
    status: url.includes('/broken-error') ? 502 : 200,
    ok: !url.includes('/broken-error'),
    json: async () => { throw new SyntaxError('Unexpected end of JSON input'); },
  });
  const overfin = new Overfin();
  // A title page cut short isn't a title without an ID (which put
  // "/title/undefined" in the address bar).
  await assert.rejects(overfin.title('title_movie_tmdb_1'), SyntaxError);
  await assert.rejects(overfin.similar('title_movie_tmdb_1'), SyntaxError);
  await assert.rejects(overfin.request('GET', '/broken-error'), (err) => err instanceof OverfinError && err.status === 502);
});

test('changes without a body still go as JSON', async () => {
  let sent;
  globalThis.fetch = async (url, init = {}) => {
    sent = init;
    return { status: 200, ok: true, json: async () => ({ favorite: true }) };
  };
  await new Overfin().setFavorite('title_movie_tmdb_1');
  assert.equal(sent.method, 'PUT');
  assert.equal(sent.headers['Content-Type'], 'application/json');
  assert.equal(sent.body, '{}');
  await new Overfin().setFavorite('title_movie_tmdb_1', false);
  assert.equal(sent.method, 'DELETE');
  assert.equal(sent.body, undefined);
});

test('a lapsed login goes through the login and back', async () => {
  fakeServer({ 'GET /api/v1/me': () => [401, { type: 'not-authenticated', status: 401 }] });
  let sentTo = '';
  const overfin = new Overfin({ onLoginRequired: (url) => { sentTo = url; } });
  await assert.rejects(overfin.me());
  assert.equal(sentTo, '/api/v1/login?return=%2F%23%2Fitem%2Fitem_1');
});

test('queries repeat list values and skip empty ones', async () => {
  const calls = fakeServer({ 'GET /api/v1/titles': () => [200, { items: [] }] });
  await new Overfin().titles({ kind: ['movie', 'series'], genre: '', sort: 'name', limit: 10 });
  assert.equal(calls[0].url, '/api/v1/titles?kind=movie&kind=series&sort=name&limit=10');
});

test('pages follow cursors and keep the shuffle seed', async () => {
  const calls = fakeServer({
    'GET /api/v1/titles': (url) => {
      const q = new URL(url, 'http://x').searchParams;
      if (!q.get('cursor')) return [200, { items: [{ id: 'a' }], nextCursor: 'c2', seed: 's1' }];
      return [200, { items: [{ id: 'b' }] }];
    },
  });
  const seen = [];
  for await (const page of new Overfin().pages({ sort: 'random' })) seen.push(...page.items.map((i) => i.id));
  assert.deepEqual(seen, ['a', 'b']);
  assert.match(calls[1].url, /cursor=c2/);
  assert.match(calls[1].url, /seed=s1/);
});

test('onWatchlist is a yes or no', async () => {
  fakeServer({
    'GET /api/v1/me/watchlist/item_on': () => [200, { on: true }],
    'GET /api/v1/me/watchlist/item_off': () => [200, { on: false }],
    // Before 0.5.
    'GET /api/v1/me/watchlist/item_old_on': () => [204, null],
    'GET /api/v1/me/watchlist/item_old_off': () => [404, { type: 'not-found', status: 404 }],
  });
  const overfin = new Overfin();
  assert.equal(await overfin.onWatchlist('item_on'), true);
  assert.equal(await overfin.onWatchlist('item_off'), false);
  assert.equal(await overfin.onWatchlist('item_old_on'), true);
  assert.equal(await overfin.onWatchlist('item_old_off'), false);
});

test('discovery asks for its list and filters', async () => {
  const calls = fakeServer({
    'GET /api/v1/discover': () => [200, { items: [{ id: 'title_movie_tmdb_949', availability: 'missing' }] }],
    'GET /api/v1/titles/title_movie_tmdb_949': () => [200, { id: 'title_movie_tmdb_949', genres: ['Crime'] }],
  });
  const overfin = new Overfin();
  const page = await overfin.discover('trending', 'movie', { availability: 'missing', limit: 20 });
  assert.equal(page.items[0].id, 'title_movie_tmdb_949');
  assert.match(calls[0].url, /list=trending/);
  assert.match(calls[0].url, /mediaKind=movie/);
  assert.match(calls[0].url, /availability=missing/);
  assert.doesNotMatch(calls[0].url, /cursor=/);
  assert.deepEqual((await overfin.title('title_movie_tmdb_949')).genres, ['Crime']);
});

test('marks are PUT or DELETE on the title', async () => {
  const calls = fakeServer({
    'PUT /api/v1/titles/title_movie_tmdb_1/played': () => [200, { played: true }],
    'DELETE /api/v1/titles/title_movie_tmdb_1/favorite': () => [200, { favorite: false }],
    'DELETE /api/v1/titles/title_movie_tmdb_1/progress': () => [200, { positionMs: 0 }],
    'DELETE /api/v1/me/history': () => [204, null],
  });
  const overfin = new Overfin();
  assert.equal((await overfin.setPlayed('title_movie_tmdb_1')).played, true);
  assert.equal((await overfin.setFavorite('title_movie_tmdb_1', false)).favorite, false);
  await overfin.forgetProgress('title_movie_tmdb_1');
  assert.equal(await overfin.clearHistory(), null);
  assert.deepEqual(calls.map((c) => c.method), ['PUT', 'DELETE', 'DELETE', 'DELETE']);
});

test('no content is null', async () => {
  fakeServer({ 'DELETE /api/v1/profiles/p1/pin': () => [204, null] });
  assert.equal(await new Overfin().removePin('p1'), null);
});

// ---- Player ------------------------------------------------------------------

function fakeVideo() {
  const listeners = {};
  return {
    currentTime: 0, paused: false, autoplay: false, src: '',
    addEventListener(ev, fn) { (listeners[ev] ||= new Set()).add(fn); },
    removeEventListener(ev, fn) { listeners[ev]?.delete(fn); },
    emit(ev) { for (const fn of listeners[ev] || []) fn(); },
    querySelectorAll: () => [],
    append() {},
    canPlayType: () => '',
    removeAttribute() {},
    load() {},
    pause() { this.paused = true; },
  };
}

function playbackServer() {
  let n = 0;
  const state = { gone: null };
  const calls = fakeServer({
    'POST /api/v1/playback': (_, body) => [201, {
      id: `pb${++n}`, title: { id: body.titleId, name: 'Film' }, method: 'direct',
      stream: { url: `/api/v1/stream/t${n}/file`, protocol: 'file' }, positionMs: body.startPositionMs,
    }],
    'POST /api/v1/playback/pb1/progress': () => state.gone || [204, null],
    'POST /api/v1/playback/pb1/stop': () => [410, { type: 'playback-expired', status: 410 }],
    'POST /api/v1/playback/pb2/stop': () => [204, null],
    'GET /api/v1/playback/pb1': () => state.gone || [200, {}],
  });
  return { calls, state };
}

const settle = () => new Promise((r) => setTimeout(r, 10));

test('the player restarts a freed stream at the same place, quietly', async () => {
  const { calls, state } = playbackServer();
  const video = fakeVideo();
  const errors = [];
  const player = await new Overfin().play(video, 'title_movie_tmdb_1', {
    startMs: 0, capabilities: {}, onError: (e) => errors.push(e),
  });
  assert.equal(player.playback.id, 'pb1');

  video.currentTime = 754.2;
  state.gone = [410, { type: 'playback-expired', status: 410 }];
  video.emit('seeked'); // reports progress, which finds the playback gone
  await settle();

  assert.equal(player.playback.id, 'pb2');
  const restart = calls.filter((c) => c.method === 'POST' && c.url === '/api/v1/playback')[1];
  assert.equal(restart.body.startPositionMs, 754200);
  assert.deepEqual(errors, []);
  await player.close();
});

test('after a night on pause, a playback the server forgot restarts too', async () => {
  const { calls, state } = playbackServer();
  const video = fakeVideo();
  const errors = [];
  const player = await new Overfin().play(video, 'title_movie_tmdb_1', {
    startMs: 0, capabilities: {}, onError: (e) => errors.push(e),
  });
  video.currentTime = 1200;
  video.emit('pause');
  await settle();
  player.pausedAt = Date.now() - 8 * 3600_000;
  state.gone = [404, { type: 'not-found', status: 404 }];
  video.paused = false;
  video.emit('play'); // a long pause: checks the playback, which is unknown
  await settle();

  assert.equal(player.playback.id, 'pb2');
  const restart = calls.filter((c) => c.method === 'POST' && c.url === '/api/v1/playback')[1];
  assert.equal(restart.body.startPositionMs, 1200000);
  assert.equal(video.autoplay, true);
  assert.deepEqual(errors, []);
  await player.close();
});

test('a takeover is reported, and continueHere takes it back', async () => {
  const { state } = playbackServer();
  const video = fakeVideo();
  let takenBy = null;
  const player = await new Overfin().play(video, 'title_movie_tmdb_1', {
    startMs: 0, capabilities: {}, onTakenOver: (device) => { takenBy = device; },
  });
  video.currentTime = 30;
  state.gone = [410, { type: 'playback-taken-over', takenOverBy: 'Firefox on Linux', status: 410 }];
  video.emit('pause');
  await settle();
  assert.equal(takenBy, 'Firefox on Linux');
  assert.ok(video.paused);

  await player.continueHere();
  assert.equal(player.playback.id, 'pb2');
  await player.close();
});

test('without a start position, the player resumes where the profile left off', async () => {
  const calls = fakeServer({
    'GET /api/v1/titles/title_movie_tmdb_1': () => [200, { id: 'title_movie_tmdb_1', userState: { positionMs: 42000 } }],
    'POST /api/v1/playback': (_, body) => [201, {
      id: 'pb1', title: { id: 'title_movie_tmdb_1', name: 'Film' }, method: 'direct',
      stream: { url: '/x', protocol: 'file' }, positionMs: body.startPositionMs,
    }],
    'POST /api/v1/playback/pb1/stop': () => [204, null],
  });
  const player = await new Overfin().play(fakeVideo(), 'title_movie_tmdb_1', { capabilities: {} });
  assert.equal(calls.find((c) => c.url === '/api/v1/playback').body.startPositionMs, 42000);
  await player.close();
});

test('Player is exported for clients that build their own', () => {
  assert.equal(typeof Player, 'function');
});

// A browser, as capabilities() sees it: its name, and what it says it
// plays. The screen is a standard one.
function asBrowser(userAgent, answers) {
  Object.defineProperty(globalThis, 'navigator', { value: { userAgent }, configurable: true });
  globalThis.document = { createElement: () => ({ canPlayType: (mime) => answers[mime] || '' }) };
  globalThis.screen = { width: 3840, height: 2160 };
  globalThis.matchMedia = () => ({ matches: false });
  globalThis.MediaSource = undefined;
}

const plays = {
  'video/mp4; codecs="avc1.640028"': 'probably',
  'video/mp4; codecs="hvc1.2.4.L153.90"': 'probably', // Main 10 only
  'video/x-matroska': 'maybe',
};

test('Chrome gets Matroska, 10-bit HEVC and HDR, whatever the screen', () => {
  asBrowser('Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36', plays);
  const c = capabilities();
  assert.ok(c.containers.includes('mkv'), c.containers);
  assert.ok(c.videoCodecs.includes('hevc'), c.videoCodecs);
  assert.deepEqual(c.hdr, ['hdr10', 'hlg']);
});

test('Firefox gets neither Matroska nor HDR on Linux, whatever it says', () => {
  asBrowser('Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0', plays);
  const c = capabilities();
  assert.ok(!c.containers.includes('mkv'), c.containers);
  assert.deepEqual(c.hdr, []);
});


test('wishes are put on the wishlist by title, and ordered', async () => {
  const calls = fakeServer({
    'PUT /api/v1/me/wishlist/title_series_tmdb_1399': () => [204, null],
    'PUT /api/v1/me/wishlist': () => [204, null],
    'GET /api/v1/me/wishlist': () => [200, { on: true, items: [{ title: { id: 'title_series_tmdb_1399_s1' }, state: 'waiting' }], arrived: [] }],
  });
  const overfin = new Overfin();
  assert.equal(await overfin.addToWishlist('title_series_tmdb_1399'), null);
  assert.equal(await overfin.orderWishlist(['title_series_tmdb_1399_s1']), null);
  assert.deepEqual(calls[1].body, { titles: ['title_series_tmdb_1399_s1'] });
  assert.equal((await overfin.wishlist()).items[0].state, 'waiting');
});

test('batch: several calls in one, each answered or failed on its own', async () => {
  const calls = fakeServer({
    'POST /api/v1/batch': (_, body) => [200, { responses: [
      { status: 200, body: { items: [1] } },
      { status: 404, body: { type: 'not-found', title: 'Not found', status: 404 } },
    ] }],
  });
  const overfin = new Overfin();
  const [first, second] = await overfin.batch([['/titles/continue', { limit: 20 }], '/titles/x']);
  assert.deepEqual(calls[0].body, { requests: ['/api/v1/titles/continue?limit=20', '/api/v1/titles/x'] });
  assert.deepEqual(first, { items: [1] });
  assert.ok(second instanceof OverfinError && second.type === 'not-found');
});

test('subtitles: the language kept, and those the server turned on shown', async () => {
  const calls = fakeServer({
    'PUT /api/v1/me/subtitles': () => [204, null],
    'POST /api/v1/playback': (_, body) => [201, {
      id: 'pb1', title: { id: body.titleId, name: 'Film' }, method: 'direct', stream: { url: '/x', protocol: 'file' },
      positionMs: 0, subtitleTrack: 's3', subtitleTracks: [{ id: 's3', label: 'Français', url: '/vtt' }],
    }],
    'POST /api/v1/playback/pb1/stop': () => [204, null],
  });
  const realDocument = globalThis.document;
  globalThis.document = { createElement: () => ({ dataset: {} }) }; // the <track> elements
  const overfin = new Overfin();
  await overfin.setSubtitleLanguage('fr');
  assert.deepEqual(calls[0].body, { language: 'fr' });
  // The app's language goes with the start; the subtitles the server
  // turned on show.
  const player = await overfin.play(fakeVideo(), 'title_movie_tmdb_1', { startMs: 0, capabilities: {}, audioLanguage: 'fr' });
  assert.equal(calls.find((c) => c.url === '/api/v1/playback').body.audioLanguage, 'fr');
  assert.equal(player.subtitleTrack, 's3');
  await player.close();
  globalThis.document = realDocument;
});

test('two quick changes leave one playback: the overtaken one is stopped', async () => {
  const { calls } = playbackServer();
  const player = await new Overfin().play(fakeVideo(), 'title_movie_tmdb_1', { startMs: 0, capabilities: {} });
  await Promise.all([player.setMaxBitrate(4000), player.setMaxBitrate(2000)]);
  await settle();
  const started = calls.filter((c) => c.method === 'POST' && c.url === '/api/v1/playback').length;
  const stopped = calls.filter((c) => c.url.endsWith('/stop')).map((c) => c.url);
  assert.equal(started, 3);
  assert.equal(player.playback.id, 'pb3');
  assert.ok(stopped.includes('/api/v1/playback/pb1/stop') && stopped.includes('/api/v1/playback/pb2/stop'), stopped);
  await player.close();
});

test('closed while starting: what the start gets is stopped, nothing plays', async () => {
  const { calls } = playbackServer();
  const player = new Player(new Overfin(), fakeVideo(), 'title_movie_tmdb_1', { startMs: 0, capabilities: {} });
  const starting = player.start();
  await player.close();
  assert.equal(await starting, null);
  await settle();
  assert.equal(player.playback, null);
  assert.ok(calls.some((c) => c.url === '/api/v1/playback/pb1/stop'));
});

test('quality: a bitrate limit restarts the stream at the same place with it', async () => {
  const { calls } = playbackServer();
  const player = await new Overfin().play(fakeVideo(), 'title_movie_tmdb_1', { startMs: 0, capabilities: {} });
  player.video.currentTime = 120;
  await player.setMaxBitrate(4000);
  const starts = calls.filter((c) => c.method === 'POST' && c.url === '/api/v1/playback');
  assert.equal(starts[1].body.maxBitrateKbps, 4000);
  assert.equal(starts[1].body.startPositionMs, 120000);
  assert.equal(player.maxBitrate, 4000);
  await player.setMaxBitrate(0); // automatic again
  assert.equal(calls.filter((c) => c.url === '/api/v1/playback').at(-1).body.maxBitrateKbps, undefined);
  await player.close();
});

test('collections: the list, then one with its films', async () => {
  const calls = fakeServer({
    'GET /api/v1/collections': () => [200, { items: [{ id: 'collection_10', name: 'Saga', inLibrary: 1, films: 3 }] }],
    'GET /api/v1/collections/collection_10': () => [200, { id: 'collection_10', name: 'Saga', inLibrary: 1, films: 3, items: [] }],
  });
  const of = new Overfin();
  const all = await of.collections();
  assert.equal(all[0].id, 'collection_10');
  const one = await of.collection(all[0].id);
  assert.deepEqual(one.items, []);
  assert.deepEqual(calls.map((c) => c.url), ['/api/v1/collections', '/api/v1/collections/collection_10']);
});

// ---- Picture subtitles -----------------------------------------------------

// A PGS segment: magic, times (ms, as 90 kHz), type, size, body.
function segment(type, body, ms = 0) {
  const pts = ms * 90;
  return [0x50, 0x47, (pts >>> 24) & 255, (pts >>> 16) & 255, (pts >>> 8) & 255, pts & 255, 0, 0, 0, 0, type, body.length >> 8, body.length & 255, ...body];
}
// A display set at ms: 1920×1080, one picture (id 1) at 100,900 when runs
// are given, else a clearing one.
function displaySet(ms, runs, { epoch = true, forced = false, fragments = 1 } = {}) {
  const shown = runs ? [0, 1, 0, forced ? 0x40 : 0, 0, 100, 3, 132] : [];
  const out = [...segment(0x16, [7, 128, 4, 56, 16, 0, 0, epoch ? 0x80 : 0, 0, 0, runs ? 1 : 0, ...shown], ms)];
  if (runs) {
    // Palette 0: entry 1 white, opaque (Y 235, Cr 128, Cb 128).
    out.push(...segment(0x14, [0, 0, 1, 235, 128, 128, 255], ms));
    const head = [0, 1, 0, 0, 0, 0, runs.length + 4, 0, 3, 0, 2];
    if (fragments === 1) {
      head[3] = 0xc0;
      out.push(...segment(0x15, [...head, ...runs], ms));
    } else {
      head[3] = 0x80;
      out.push(...segment(0x15, [...head, ...runs.slice(0, 2)], ms));
      out.push(...segment(0x15, [0, 1, 0, 0x40, ...runs.slice(2)], ms));
    }
  }
  out.push(...segment(0x80, [], ms));
  return out;
}
// 3 by 2: a white pixel, 2 clear, end of line; 3 white, end of line.
const RUNS = [1, 0, 2, 0, 0, 0, 0x83, 1, 0, 0];

test('a PGS track reads into cues, each ending when the next display set comes', async () => {
  const { decodePGS, pgsPixels, cueAt } = await import('./overfin.js');
  const cues = decodePGS(new Uint8Array([...displaySet(1000, RUNS), ...displaySet(3000, null), ...displaySet(5000, RUNS, { forced: true, fragments: 2 })]));
  assert.equal(cues.length, 2);
  assert.deepEqual([cues[0].startMs, cues[0].endMs, cues[1].startMs, cues[1].endMs], [1000, 3000, 5000, Infinity]);
  assert.deepEqual([cues[0].width, cues[0].height, cues[0].forced, cues[1].forced], [1920, 1080, false, true]);
  const p = cues[0].pictures[0];
  assert.deepEqual([p.x, p.y, p.width, p.height], [100, 900, 3, 2]);
  const px = pgsPixels(p);
  // White and opaque where colour 1 is, clear elsewhere.
  assert.deepEqual([...px.slice(0, 4)], [255, 255, 255, 255]);
  assert.deepEqual([...px.slice(4, 12)], [0, 0, 0, 0, 0, 0, 0, 0]);
  assert.deepEqual([...px.slice(12)].filter((_, i) => i % 4 === 3), [255, 255, 255]);
  assert.deepEqual([...pgsPixels(cues[1].pictures[0])], [...px], 'fragments join');
  assert.equal(cueAt(cues, 999), null);
  assert.equal(cueAt(cues, 1000), cues[0]);
  assert.equal(cueAt(cues, 2999), cues[0]);
  assert.equal(cueAt(cues, 4000), null);
  assert.equal(cueAt(cues, 9e6), cues[1]);
});

test('a malformed PGS track or picture throws', async () => {
  const { decodePGS, pgsPixels } = await import('./overfin.js');
  const good = displaySet(0, RUNS);
  for (const bad of [good.slice(0, -3), [0x50, 0x58, ...good.slice(2)], [...good, ...segment(0x42, [])]]) {
    assert.throws(() => decodePGS(new Uint8Array(bad)));
  }
  const [cue] = decodePGS(new Uint8Array(displaySet(0, [0, 0x84, 1])));
  assert.throws(() => pgsPixels(cue.pictures[0]), 'a line too long');
  // A huge screen: a canvas of a gigabyte.
  const huge = displaySet(0, null);
  huge[13] = 0x40; huge[14] = 0; huge[15] = 0x40; huge[16] = 0;
  assert.throws(() => decodePGS(new Uint8Array(huge)), 'a huge screen');
});

// Real tracks read and draw, when there are some to try (PGS_SAMPLES).
test('real PGS tracks', { skip: !process.env.PGS_SAMPLES }, async () => {
  const { decodePGS, pgsPixels } = await import('./overfin.js');
  const { readdirSync, readFileSync, statSync } = await import('node:fs');
  const dir = process.env.PGS_SAMPLES;
  for (const name of readdirSync(dir)) {
    if (!statSync(`${dir}/${name}`).isFile()) continue;
    const raw = readFileSync(`${dir}/${name}`);
    if (raw[0] !== 0x50 || raw[1] !== 0x47) continue;
    const cues = decodePGS(new Uint8Array(raw));
    assert.ok(cues.length > 10, `${name}: ${cues.length} cues`);
    let opaque = 0;
    for (const cue of cues) for (const p of cue.pictures) {
      const px = pgsPixels(p);
      for (let i = 3; i < px.length; i += 4) if (px[i]) opaque++;
    }
    assert.ok(opaque > 0, name);
  }
});

test('subtitles: forced ones whenever none are chosen; a choice shows at once, never restarts, kept for the title', async () => {
  const calls = fakeServer({
    'POST /api/v1/playback': (_, body) => [201, {
      id: 'pb1', title: { id: body.titleId, name: 'Film' }, method: 'direct', stream: { url: '/x', protocol: 'file' },
      positionMs: 0, audioTrack: 'a1',
      subtitleTracks: [{ id: 's3', label: 'English', url: '/vtt3' }, { id: 's4', label: 'Français', pictures: true, url: '/pgs4' }],
      forcedTracks: [{ id: 's5', label: 'Français', forced: true, url: '/vtt5' }], forcedTrack: 's5',
    }],
    'PUT /api/v1/playback/pb1/tracks': () => [204, null],
    'POST /api/v1/playback/pb1/stop': () => [204, null],
  });
  const api = globalThis.fetch;
  globalThis.fetch = async (url, init) => url === '/pgs4'
    ? { ok: true, status: 200, arrayBuffer: async () => new Uint8Array(displaySet(0, RUNS)).buffer }
    : api(url, init);
  const realDocument = globalThis.document;
  globalThis.document = { createElement: () => ({ dataset: {}, track: { mode: 'disabled' } }) };
  globalThis.requestAnimationFrame = () => 0;
  globalThis.cancelAnimationFrame = () => {};
  const video = fakeVideo();
  const els = [];
  video.append = (el) => els.push(el);
  video.querySelectorAll = () => els;
  const mode = () => Object.fromEntries(els.map((el) => [el.dataset.overfin, el.track.mode]));
  const canvas = { style: {}, width: 0, height: 0, getContext: () => ({ clearRect() {}, putImageData() {} }) };
  const player = await new Overfin().play(video, 'title_movie_tmdb_1', { startMs: 0, capabilities: {}, pictureCanvas: canvas });
  // Text tracks join the video once they're loaded: playback never waits
  // for them.
  assert.deepEqual(mode(), {});
  await settle();
  // None chosen: the forced ones, in the audio's language.
  assert.equal(player.subtitleTrack, null);
  assert.equal(player.forcedTrack, 's5');
  assert.deepEqual(mode(), { s3: 'disabled', s5: 'showing' });
  await player.setSubtitleTrack('s3');
  assert.deepEqual(mode(), { s3: 'showing', s5: 'disabled' });
  assert.equal(player.forcedTrack, null);
  // Pictures: drawn on the canvas, the text tracks off.
  await player.setSubtitleTrack('s4');
  await settle();
  assert.deepEqual(mode(), { s3: 'disabled', s5: 'disabled' });
  assert.equal(player.pictures.cues.length, 1);
  await player.setSubtitleTrack(null);
  assert.deepEqual(mode(), { s3: 'disabled', s5: 'showing' });
  assert.equal(player.pictures.cues.length, 0);
  await settle();
  const kept = calls.filter((c) => c.url === '/api/v1/playback/pb1/tracks').map((c) => c.body.subtitleTrack);
  assert.deepEqual(kept, ['s3', 's4', '']);
  assert.equal(calls.filter((c) => c.url === '/api/v1/playback').length, 1, 'never restarted');
  await player.close();
  globalThis.document = realDocument;
});

// hls.js, as far as the player sees it: destroying it resets the video.
function fakeHls(video, seen) {
  return class {
    static Events = { ERROR: 'hlsError' };
    static isSupported() { return true; }
    constructor(config) { seen.push(config); seen.current = this; }
    on(event, fn) { this.fail = fn; }
    loadSource() {}
    attachMedia() {}
    destroy() { video.currentTime = 0; }
  };
}

function hlsServer(state) {
  let n = 0;
  return fakeServer({
    'POST /api/v1/playback': (_, body) => {
      state.started.push(body.startPositionMs);
      return [201, { id: `pb${++n}`, title: { id: body.titleId, name: 'Film' }, method: 'transcode',
        stream: { url: `/api/v1/stream/t${n}/master.m3u8`, protocol: 'hls' }, positionMs: body.startPositionMs }];
    },
    'POST /api/v1/playback/pb1/progress': () => state.gone || [204, null],
    'POST /api/v1/playback/pb1/stop': () => [204, null],
    'POST /api/v1/playback/pb2/stop': () => [204, null],
    'POST /api/v1/playback/pb3/stop': () => [204, null],
    'POST /api/v1/playback/pb4/stop': () => [204, null],
    'POST /api/v1/playback/pb5/stop': () => [204, null],
    'GET /api/v1/playback/pb1': () => [200, {}],
    'GET /api/v1/playback/pb2': () => [200, {}],
    'GET /api/v1/playback/pb3': () => [200, {}],
    'GET /api/v1/playback/pb4': () => [200, {}],
  });
}

test('continueHere takes back at the same place, though hls.js reset the video', async () => {
  const state = { started: [], gone: null };
  hlsServer(state);
  const video = fakeVideo();
  const seen = [];
  const player = await new Overfin().play(video, 'title_movie_tmdb_1', {
    startMs: 0, capabilities: {}, Hls: fakeHls(video, seen), onTakenOver: () => {},
  });
  video.currentTime = 30;
  state.gone = [410, { type: 'playback-taken-over', takenOverBy: 'Firefox', status: 410 }];
  video.emit('pause');
  await settle();
  assert.equal(video.currentTime, 0, 'the video was reset');
  await player.continueHere();
  assert.deepEqual(state.started, [0, 30000]);
  assert.equal(player.positionMs, 30000, 'held until the stream has its place');
  video.currentTime = 30.5;
  video.emit('timeupdate');
  assert.equal(player.positionMs, 30500);
  await player.close();
});

test('two track changes in a row keep the place', async () => {
  const state = { started: [], gone: null };
  hlsServer(state);
  const video = fakeVideo();
  const player = await new Overfin().play(video, 'title_movie_tmdb_1', {
    startMs: 0, capabilities: {}, Hls: fakeHls(video, []),
  });
  video.currentTime = 40;
  const first = player.restart();
  const second = player.restart();
  await Promise.all([first, second]);
  assert.deepEqual(state.started.slice(1), [40000, 40000]);
  await player.close();
});

test('a stream hls.js gives up on starts over at the same place, then says so', async () => {
  const state = { started: [], gone: null };
  hlsServer(state);
  const video = fakeVideo();
  const seen = [];
  const errors = [];
  const player = await new Overfin().play(video, 'title_movie_tmdb_1', {
    startMs: 0, capabilities: {}, Hls: fakeHls(video, seen), onError: (e) => errors.push(e),
  });
  video.currentTime = 50;
  for (let i = 0; i < 3; i++) {
    seen.current.fail(null, { fatal: true, details: 'bufferStalledError' });
    await settle();
  }
  assert.deepEqual(state.started, [0, 50000, 50000, 50000]);
  assert.deepEqual(errors, []);
  seen.current.fail(null, { fatal: true, details: 'bufferStalledError' });
  await settle();
  assert.equal(state.started.length, 4, 'no more restarts');
  assert.equal(errors.length, 1);
  assert.ok(video.paused);
  await player.close();
});
