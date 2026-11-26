// Tests for overfin.js, with Node's own test runner:
//   node --test sdk/js/overfin.test.js
import { test, mock } from 'node:test';
import assert from 'node:assert/strict';
import {
  Overfin, OverfinError, Player, deviceEnv, chooseDelivery, audioMenu, subtitleMenu, forcedFor, subtitlesFor,
  copyLabel, audioLabel, subtitleLabel, playRequest, streamUrls, previewSheets, fallbacks, dolbyVisionCodecs,
} from './overfin.js';

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

test('recommendations come page by page', async () => {
  const calls = fakeServer({
    'GET /api/v1/titles/recommended': () => [200, { items: [{ id: 'title_movie_tmdb_949' }], nextCursor: 'c2' }],
  });
  const overfin = new Overfin();
  const page = await overfin.recommendedPage('recent', { limit: 40, cursor: 'c1' });
  assert.equal(page.items[0].id, 'title_movie_tmdb_949');
  assert.equal(page.nextCursor, 'c2');
  assert.match(calls[0].url, /scope=recent/);
  assert.match(calls[0].url, /limit=40/);
  assert.match(calls[0].url, /cursor=c1/);
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

// plainPage is a title's page with one copy every browser plays as it is
// (an MP4 of H.264 and AAC), 1000 seconds long.
function plainPage(more = {}) {
  return {
    id: 'title_movie_tmdb_1', limits: { originalFile: true }, copies: [{
      id: 'vplain', etag: 'e', durationMs: 1_000_000, mediaType: 'video/mp4', size: 100_000_000,
      video: { codec: 'h264', codecs: 'avc1.640028', width: 1920, height: 1080, bitDepth: 8 },
      audioTracks: [{ id: 'a1', codec: 'aac', codecs: 'mp4a.40.2', channels: 2, language: 'en', default: true }],
    }], ...more,
  };
}

// answer is the server's answer to a play: MSFP's playback object.
function answer(n, body, protocol = 'file', more = {}) {
  return { id: `pb${n}`, ticket: `t${n}`, protocol, method: protocol === 'file' ? 'direct' : 'remux', etag: 'e',
    positionMs: body.startPositionMs, versionId: body.versionId, audioTrack: body.audioTrack || 'a1', subtitles: [],
    title: { id: body.titleId, name: 'Film' }, ...more };
}

function playbackServer() {
  let n = 0;
  const state = { gone: null };
  const calls = fakeServer({
    'GET /api/v1/titles/title_movie_tmdb_1': () => [200, plainPage()],
    'POST /api/v1/playback': (_, body) => [201, answer(++n, body)],
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
    startMs: 0, env: chrome(), onError: (e) => errors.push(e),
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
    startMs: 0, env: chrome(), onError: (e) => errors.push(e),
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
    startMs: 0, env: chrome(), onTakenOver: (device) => { takenBy = device; },
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
    'GET /api/v1/titles/title_movie_tmdb_1': () => [200, plainPage({ userState: { positionMs: 42000 } })],
    'POST /api/v1/playback': (_, body) => [201, answer(1, body)],
    'POST /api/v1/playback/pb1/stop': () => [204, null],
  });
  const player = await new Overfin().play(fakeVideo(), 'title_movie_tmdb_1', { env: chrome() });
  assert.equal(calls.find((c) => c.url === '/api/v1/playback').body.startPositionMs, 42000);
  await player.close();
});

test('Player is exported for clients that build their own', () => {
  assert.equal(typeof Player, 'function');
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

test('subtitles: the language kept', async () => {
  const calls = fakeServer({ 'PUT /api/v1/me/subtitles': () => [204, null] });
  await new Overfin().setSubtitleLanguage('fr');
  assert.deepEqual(calls[0].body, { language: 'fr' });
});

test('two quick changes leave one playback: the overtaken one is stopped', async () => {
  const { calls } = playbackServer();
  const player = await new Overfin().play(fakeVideo(), 'title_movie_tmdb_1', { startMs: 0, env: chrome() });
  await Promise.all([player.setMaxBitrate(4000), player.setMaxBitrate(2000)]);
  await settle();
  const started = calls.filter((c) => c.method === 'POST' && c.url === '/api/v1/playback').length;
  const stopped = calls.filter((c) => c.url.endsWith('/stop')).map((c) => c.url);
  // The first change, overtaken before it asked the server, asks nothing.
  assert.equal(started, 2);
  assert.equal(player.playback.id, 'pb2');
  assert.ok(stopped.includes('/api/v1/playback/pb1/stop'), stopped);
  await player.close();
});

test('closed while starting: what the start gets is stopped, nothing plays', async () => {
  const { calls } = playbackServer();
  const player = new Player(new Overfin(), fakeVideo(), 'title_movie_tmdb_1', { startMs: 0, env: chrome() });
  // Closed while the server starts it (the title's page read first).
  const ask = globalThis.fetch;
  let closing;
  globalThis.fetch = (url, init) => {
    if (url === '/api/v1/playback' && !closing) closing = player.close();
    return ask(url, init);
  };
  const starting = player.start();
  assert.equal(await starting, null);
  await closing;
  globalThis.fetch = ask;
  await settle();
  assert.equal(player.playback, null);
  assert.ok(calls.some((c) => c.url === '/api/v1/playback/pb1/stop'));
});

// ranges is a video's buffered ranges.
const ranges = (list) => ({ length: list.length, start: (i) => list[i][0], end: (i) => list[i][1] });

test('a stream waiting in a hole before its first pictures jumps to them, when near', () => {
  mock.timers.enable({ apis: ['setTimeout'] });
  try {
    const video = fakeVideo();
    const player = new Player(new Overfin(), video, 'title_movie_tmdb_1', { startMs: 0, env: chrome() });
    const waitAt = (kind, t, list) => {
      player.playback = { delivery: { kind } };
      video.currentTime = t;
      video.buffered = ranges(list);
      player.jumpHole();
      mock.timers.tick(1500);
      return video.currentTime;
    };
    // A copy cut in equal parts: the part asked for starts at 6 s, its
    // first keyframe comes at 12.54 s.
    assert.equal(waitAt('transcode', 9.67, [[12.54, 46.1]]), 12.59);
    // Already where pictures are, or nothing near: it stays.
    assert.equal(waitAt('remux', 20, [[12.54, 46.1]]), 20);
    assert.equal(waitAt('transcode', 100, [[130, 140]]), 100);
    // A file is the browser's to fill.
    assert.equal(waitAt('file', 9.67, [[12.54, 46.1]]), 9.67);
    // Started there, autoplay waiting for pictures (paused until then): it
    // jumps too.
    Object.assign(video, { paused: true, autoplay: true, readyState: 1 });
    assert.equal(waitAt('transcode', 9.67, [[12.54, 46.1]]), 12.59);
    // Paused by the viewer: nothing moves.
    Object.assign(video, { autoplay: false, readyState: 4 });
    assert.equal(waitAt('transcode', 9.67, [[12.54, 46.1]]), 9.67);
  } finally {
    mock.timers.reset();
  }
});

test('quality: a bitrate limit restarts the stream at the same place with it', async () => {
  const { calls } = playbackServer();
  const player = await new Overfin().play(fakeVideo(), 'title_movie_tmdb_1', { startMs: 0, env: chrome() });
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

test('subtitles: forced ones whenever none are chosen; a choice shows at once, in stretches, never restarts, kept for the title', async () => {
  const env = chrome();
  // The copy: French audio; English subtitles (s2), French ones as
  // pictures (s3), forced French ones (s4).
  const page = plainPage();
  page.copies[0].audioTracks[0].language = 'fr';
  page.copies[0].subtitleTracks = [{ id: 's2', format: 'subrip', language: 'en' },
    { id: 's3', format: 'hdmv_pgs_subtitle', language: 'fr' }, { id: 's4', format: 'subrip', language: 'fr', forced: true }];
  const calls = fakeServer({
    'GET /api/v1/titles/title_movie_tmdb_1': () => [200, page],
    'POST /api/v1/playback': (_, body) => [201, answer(1, body, 'file',
      { subtitles: [{ id: 's2', form: 'text' }, { id: 's3', form: 'pgs' }, { id: 's4', form: 'text' }] })],
    'PUT /api/v1/playback/pb1/tracks': () => [204, null],
    'POST /api/v1/playback/pb1/stop': () => [204, null],
  });
  const api = globalThis.fetch;
  const stretches = [];
  const root = '/api/v1/stream/t1/';
  globalThis.fetch = async (url, init) => {
    const part = url.startsWith(root) ? url.slice(root.length) : '';
    if (/^(subtitles|pictures)\/s\d\/\d+$/.test(part)) stretches.push(part);
    if (part.startsWith('pictures/s3/')) return { ok: true, status: 200, arrayBuffer: async () => new Uint8Array(displaySet(0, RUNS)).buffer };
    // Each stretch starts a little early: the line at 1 s comes twice.
    if (part.startsWith('subtitles/')) return { ok: true, status: 200, text: async () => 'WEBVTT\n\n00:01.000 --> 00:02.000\nHi\n' };
    return api(url, init);
  };
  globalThis.VTTCue = class { constructor(start, end, text) { Object.assign(this, { start, end, text }); } };
  const realDocument = globalThis.document;
  globalThis.document = {
    createElement: () => ({ dataset: {}, track: { mode: 'disabled', cues: [], addCue(c) { this.cues.push(c); } }, dispatchEvent() {} }),
  };
  globalThis.requestAnimationFrame = () => 0;
  globalThis.cancelAnimationFrame = () => {};
  const video = fakeVideo();
  const els = [];
  video.append = (el) => els.push(el);
  video.querySelectorAll = () => els;
  const mode = () => Object.fromEntries(els.map((el) => [el.dataset.overfin, el.track.mode]));
  const canvas = { style: {}, width: 0, height: 0, getContext: () => ({ clearRect() {}, putImageData() {} }) };
  const player = await new Overfin().play(video, 'title_movie_tmdb_1', { startMs: 0, env, pictureCanvas: canvas });
  // Text tracks are there at once, their cues coming in stretches: none
  // chosen, the forced ones, in the audio's language, the stretch playing
  // and the next, each line once.
  assert.equal(player.subtitleTrack, null);
  assert.equal(player.forcedTrack, 's4');
  assert.deepEqual(mode(), { s2: 'disabled', s4: 'showing' });
  await settle();
  assert.deepEqual(stretches, ['subtitles/s4/0', 'subtitles/s4/1']);
  assert.equal(els.find((el) => el.dataset.overfin === 's4').track.cues.length, 1);
  await player.setSubtitleTrack('s2');
  await settle();
  assert.deepEqual(mode(), { s2: 'showing', s4: 'disabled' });
  assert.equal(player.forcedTrack, null);
  assert.deepEqual(stretches.slice(2), ['subtitles/s2/0', 'subtitles/s2/1']);
  // Pictures: drawn on the canvas, the text tracks off; overlapping
  // stretches merge.
  await player.setSubtitleTrack('s3');
  await settle();
  assert.deepEqual(mode(), { s2: 'disabled', s4: 'disabled' });
  assert.deepEqual(stretches.slice(4), ['pictures/s3/0', 'pictures/s3/1']);
  assert.equal(player.pictures.cues.length, 1);
  await player.setSubtitleTrack(null);
  assert.deepEqual(mode(), { s2: 'disabled', s4: 'showing' });
  assert.equal(player.pictures.cues.length, 0);
  await settle();
  const kept = calls.filter((c) => c.url === '/api/v1/playback/pb1/tracks').map((c) => c.body.subtitleTrack);
  assert.deepEqual(kept, ['s2', 's3', '']);
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
    'GET /api/v1/titles/title_movie_tmdb_1': () => [200, plainPage()],
    'POST /api/v1/playback': (_, body) => {
      state.started.push(body.startPositionMs);
      return [201, answer(++n, body, 'hls')];
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
    startMs: 0, env: chrome(), Hls: fakeHls(video, seen), onTakenOver: () => {},
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
    startMs: 0, env: chrome(), Hls: fakeHls(video, []),
  });
  video.currentTime = 40;
  const first = player.restart();
  const second = player.restart();
  await Promise.all([first, second]);
  // The first, overtaken before it asked the server, asks nothing.
  assert.deepEqual(state.started.slice(1), [40000]);
  await player.close();
});

test('a stream hls.js gives up on starts over at the same place, then says so', async () => {
  const state = { started: [], gone: null };
  hlsServer(state);
  const video = fakeVideo();
  const seen = [];
  const errors = [];
  const player = await new Overfin().play(video, 'title_movie_tmdb_1', {
    startMs: 0, env: chrome(), Hls: fakeHls(video, seen), onError: (e) => errors.push(e),
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

test('a stream failed with the server out of reach: the player waits for it, then starts over at the same place', async () => {
  const state = { started: [], down: true };
  const calls = hlsServer(state);
  const api = globalThis.fetch;
  globalThis.fetch = async (url, init = {}) => {
    if (state.down && url.startsWith('/api/v1/playback/pb1') && (init.method || 'GET') === 'GET') {
      throw new TypeError('NetworkError when attempting to fetch resource.');
    }
    return api(url, init);
  };
  const video = fakeVideo();
  const seen = [];
  const errors = [];
  const reconnecting = [];
  const player = await new Overfin().play(video, 'title_movie_tmdb_1', {
    startMs: 0, env: chrome(), Hls: fakeHls(video, seen), onError: (e) => errors.push(e),
    onReconnecting: (on) => reconnecting.push(on),
  });
  video.currentTime = 211;
  seen.current.fail(null, { fatal: true, details: 'fragLoadError' });
  await new Promise((r) => setTimeout(r, 3500)); // a try while still down
  assert.deepEqual(reconnecting, [true]);
  assert.deepEqual(state.started, [0], 'no restart while the server is away');
  assert.ok(video.paused);
  state.down = false;
  await new Promise((r) => setTimeout(r, 3500));
  assert.deepEqual(reconnecting, [true, false]);
  assert.deepEqual(state.started, [0, 211000]);
  assert.deepEqual(errors, []);
  assert.ok(calls.length > 0);
  await player.close();
  globalThis.fetch = api;
});

test('a direct stream cut short is not the end of the title: it starts over at the same place', async () => {
  const { calls } = playbackServer();
  const video = fakeVideo();
  const ended = [];
  const player = await new Overfin().play(video, 'title_movie_tmdb_1', {
    startMs: 0, env: chrome(), onEnded: () => ended.push(true),
  });
  video.duration = 1000;
  video.currentTime = 371;
  video.emit('ended');
  await settle();
  assert.deepEqual(ended, []);
  assert.equal(player.playback.id, 'pb2');
  const restart = calls.filter((c) => c.method === 'POST' && c.url === '/api/v1/playback')[1];
  assert.equal(restart.body.startPositionMs, 371000);
  assert.ok(!calls.some((c) => c.url.endsWith('/progress') && c.body?.ended), 'not reported as ended');
  // The real end still is one.
  video.currentTime = 999;
  video.emit('ended');
  await settle();
  assert.deepEqual(ended, [true]);
  await player.close();
});

// ---- Choosing a copy and how it plays -----------------------------------------

// A browser of a family, faked from what it answers: the containers its
// video element plays, the codecs it decodes (MSE and canPlayType alike),
// the screen's range, Overfin's app or not, audioTracks or not.
function browser({ ua, containers, video, audio, hdr = false, app = null, audioTracks = false, hls = 'hls.js', decode }) {
  const decodes = (codecs) => codecs.split(',').map((c) => c.trim()).every((c) => video.some((p) => c.startsWith(p)) || audio.includes(c));
  const parse = (type) => {
    const [container, rest] = type.split(';');
    const m = /codecs="([^"]*)"/.exec(rest || '');
    return { container: container.trim(), codecs: m ? m[1] : '' };
  };
  const canPlayType = (type) => {
    if (type === 'application/vnd.apple.mpegurl') return hls === 'native' ? 'maybe' : '';
    const { container, codecs } = parse(type);
    if (container.startsWith('audio/mp4') && codecs) return decodes(codecs) ? 'probably' : '';
    if (!containers.includes(container)) return '';
    if (!codecs) return 'maybe';
    return decodes(codecs) ? 'probably' : '';
  };
  Object.defineProperty(globalThis, 'navigator', {
    value: { userAgent: ua, mediaCapabilities: { decodingInfo: async (c) => (decode ? decode(c) : { supported: decodes(parse(c.video.contentType).codecs) }) } },
    configurable: true,
  });
  globalThis.document = { createElement: () => (audioTracks ? { canPlayType, audioTracks: [] } : { canPlayType }) };
  globalThis.MediaSource = { isTypeSupported: (type) => { const { container, codecs } = parse(type); return (container === 'video/mp4' || container === 'audio/mp4') && !!codecs && decodes(codecs); } };
  globalThis.matchMedia = (q) => ({ matches: q === '(dynamic-range: high)' && hdr });
  globalThis.OverfinApp = app ? { hdr: () => JSON.stringify(app) } : undefined;
  return deviceEnv({ Hls: hls === 'hls.js' ? { isSupported: () => true } : { isSupported: () => false } });
}

const UA = {
  chrome: 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36',
  firefox: 'Mozilla/5.0 (X11; Linux x86_64; rv:145.0) Gecko/20100101 Firefox/145.0',
  safari: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.1 Safari/605.1.15',
  android: 'Mozilla/5.0 (Linux; Android 14; Pixel 8; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/142.0.0.0 Mobile Safari/537.36',
};
const chrome = (more = {}) => browser({ ua: UA.chrome, containers: ['video/mp4', 'video/webm', 'video/x-matroska'],
  video: ['avc1', 'hvc1', 'hev1', 'av01', 'vp09'], audio: ['mp4a.40.2', 'mp4a.40.5', 'mp4a.40.34', 'Opus', 'opus', 'fLaC', 'flac'], ...more });
const firefox = (more = {}) => browser({ ua: UA.firefox, containers: ['video/mp4', 'video/webm', 'video/x-matroska'],
  video: ['avc1', 'av01', 'vp09'], audio: ['mp4a.40.2', 'mp4a.40.34', 'Opus', 'opus', 'fLaC', 'flac', 'vorbis'], ...more });
const safari = (more = {}) => browser({ ua: UA.safari, containers: ['video/mp4', 'video/quicktime'], hls: 'hls.js', audioTracks: true,
  video: ['avc1', 'hvc1', 'dvh1', 'av01'], audio: ['mp4a.40.2', 'mp4a.40.34', 'ac-3', 'ec-3', 'fLaC', 'Opus', 'alac'], ...more });
const androidApp = (more = {}) => browser({ ua: UA.android, containers: ['video/mp4', 'video/webm', 'video/x-matroska'], app: ['hdr10'],
  video: ['avc1', 'hvc1', 'hev1', 'vp09'], audio: ['mp4a.40.2', 'mp4a.40.34', 'ac-3', 'ec-3', 'Opus', 'fLaC'], ...more });

// Copies as a title's page gives them.
let copyN = 0;
function copy({ mediaType = 'video/matroska', video = {}, audio = [{ codec: 'aac', codecs: 'mp4a.40.2', channels: 2, language: 'en', default: true }],
  subtitles = [], minutes = 120, mbps = 8, ...more } = {}) {
  const id = more.id || `c${++copyN}`;
  return {
    id, etag: `e${id}`, durationMs: minutes * 60_000, mediaType, size: Math.round((mbps * 1e6 / 8) * minutes * 60),
    video: video === null ? undefined : { codec: 'h264', codecs: 'avc1.640028', width: 1920, height: 1080, bitDepth: 8, fps: '24000/1001', ...video },
    audioTracks: audio.map((a, i) => ({ id: `a${i + 1}`, ...a })),
    subtitleTracks: subtitles.map((s, i) => ({ id: `s${i + 1 + audio.length}`, ...s })),
    ...more,
  };
}
const hevcHDR10 = { codec: 'hevc', codecs: 'hev1.2.4.L153.90', width: 3840, height: 2160, bitDepth: 10, colour: { primaries: 9, transfer: 16, matrix: 9 } };
const encoders = { video: ['h264', 'hevc'], audio: ['aac', 'ac3'], ranges: ['sdr', 'keep'], toneMapping: true };
const noMemory = { memory: null };

test('an H.264 and AAC file plays as it is in every browser', async () => {
  const c = copy({ mediaType: 'video/mp4' });
  for (const env of [chrome(), firefox(), safari(), androidApp()]) {
    const d = await chooseDelivery([c], env, { encoders, ...noMemory });
    assert.equal(d.delivery, 'file', env.family);
    assert.equal(d.reason, 'as-is');
    assert.deepEqual(d.converts, { video: false, audio: false });
  }
});

test('Matroska: the file in Chrome, a remux in Firefox (its quirk), in fragmented MP4', async () => {
  const c = copy();
  assert.equal((await chooseDelivery([c], chrome(), noMemory)).delivery, 'file');
  const env = firefox();
  assert.ok(env.quirks.includes('firefox-matroska'));
  const d = await chooseDelivery([c], env, noMemory);
  assert.deepEqual([d.delivery, d.mediaType, d.reason], ['remux', 'video/mp4', 'quirk']);
  // A container no browser knows is remuxed, never tried as a file.
  const flv = await chooseDelivery([copy({ mediaType: 'video/x-flv' })], chrome(), noMemory);
  assert.deepEqual([flv.delivery, flv.reason], ['remux', 'container']);
});

test('audio a browser can\'t play is converted alone, the picture kept; TrueHD and DTS never as they are', async () => {
  const c = copy({ video: hevcHDR10, audio: [{ codec: 'eac3', channels: 6, language: 'en', default: true }] });
  const d = await chooseDelivery([c], chrome(), { encoders, ...noMemory });
  assert.deepEqual([d.delivery, d.transcode, d.reason], ['transcode', { videoCodec: 'copy', audioCodec: 'aac' }, 'audio-codec']);
  assert.deepEqual(d.converts, { video: false, audio: true });
  // Safari plays E-AC-3 and maps HDR itself: a remux (Matroska it doesn't read).
  const s = await chooseDelivery([c], safari(), { encoders, ...noMemory });
  assert.deepEqual([s.delivery, s.mediaType, s.reason], ['remux', 'video/mp4', 'container']);
  // TrueHD: whatever the browser claims.
  const env = safari({ audio: ['mp4a.40.2', 'mlpa', 'dtsh'] });
  for (const codec of [{ codec: 'truehd', codecs: 'mlpa' }, { codec: 'dts', codecs: 'dtsh' }, { codec: 'pcm_s24le' }]) {
    const t = await chooseDelivery([copy({ audio: [{ ...codec, channels: 8, default: true }] })], env, { encoders, ...noMemory });
    assert.equal(t.reason, 'audio-codec', codec.codec);
    assert.equal(t.transcode?.audioCodec, 'aac');
  }
});

test('a picture the browser can\'t decode is converted; HDR kept on an HDR screen, else made SDR', async () => {
  const c = copy({ video: hevcHDR10 });
  // Firefox on Linux: no HEVC, no HDR mapping, a standard screen.
  const d = await chooseDelivery([c], firefox(), { encoders, ...noMemory });
  assert.deepEqual([d.delivery, d.transcode.videoCodec, d.transcode.range, d.reason], ['transcode', 'h264', 'sdr', 'video-codec']);
  assert.equal(d.converts.video, true);
  // On an HDR screen, with AV1 encoded by the server: HDR kept in AV1.
  const hdr = await chooseDelivery([c], firefox({ hdr: true }), { encoders: { ...encoders, video: ['h264', 'av1'] }, ...noMemory });
  assert.deepEqual([hdr.transcode.videoCodec, hdr.transcode.range], ['av1', 'keep']);
  // Without encoders known: the baseline.
  const base = await chooseDelivery([c], firefox(), noMemory);
  assert.deepEqual([base.delivery, base.reason, base.level], ['', 'in-doubt', 4]);
  // No tone mapping and an SDR screen that decodes HEVC: played as it is.
  const noMap = await chooseDelivery([copy({ video: hevcHDR10, mediaType: 'video/mp4' })], androidApp({ app: [] }), { encoders: { ...encoders, toneMapping: false }, ...noMemory });
  assert.equal(noMap.delivery, 'file');
});

test('HDR shown by the screen: Overfin\'s app says it; elsewhere the media query, or the browser maps it', async () => {
  const c = copy({ mediaType: 'video/mp4', video: { ...hevcHDR10, codecs: 'hvc1.2.4.L153.90' } });
  assert.equal((await chooseDelivery([c], androidApp(), { encoders, ...noMemory })).delivery, 'file');
  const sdrApp = await chooseDelivery([c], androidApp({ app: [] }), { encoders, ...noMemory });
  assert.deepEqual([sdrApp.reason, sdrApp.transcode.range], ['picture-range', 'sdr']);
  // HLG in an app that says hdr10 only.
  const hlg = copy({ mediaType: 'video/mp4', video: { ...hevcHDR10, codecs: 'hvc1.2.4.L153.90', colour: { transfer: 18 } } });
  assert.equal((await chooseDelivery([hlg], androidApp(), { encoders, ...noMemory })).reason, 'picture-range');
  // Chrome on a desktop maps HDR itself; decodingInfo saying no wins.
  assert.equal((await chooseDelivery([c], chrome(), { encoders, ...noMemory })).delivery, 'file');
  const refused = chrome({ decode: (cfg) => ({ supported: !cfg.video.transferFunction }) });
  assert.equal((await chooseDelivery([c], refused, { encoders, ...noMemory })).reason, 'picture-range');
});

test('Dolby Vision: profile 5 only where the browser takes its dvh1 string; profile 8 by its base layer', async () => {
  const dv5 = copy({ mediaType: 'video/mp4', video: { ...hevcHDR10, codecs: 'hvc1.2.4.L153.90', dv: { profile: 5, level: 6, compatibility: 0 } } });
  assert.equal(dolbyVisionCodecs('hvc1.2.4.L153.90', { profile: 5, level: 6 }), 'dvh1.05.06');
  assert.equal(dolbyVisionCodecs('hev1.2.4.L153.90', { profile: 8, level: 9 }), 'dvhe.08.09');
  assert.equal(dolbyVisionCodecs('av01.0.13M.10', { profile: 10, level: 9 }), 'dav1.10.09');
  const s = await chooseDelivery([dv5], safari(), { encoders, ...noMemory });
  assert.equal(s.delivery, 'file');
  const c = await chooseDelivery([dv5], chrome(), { encoders, ...noMemory });
  assert.deepEqual([c.reason, c.transcode?.range], ['picture-range', 'sdr']);
  // Overfin's app never says Dolby Vision.
  assert.equal((await chooseDelivery([dv5], androidApp({ video: ['avc1', 'hvc1', 'dvh1'] }), { encoders, ...noMemory })).reason, 'picture-range');
  // Profile 8.1 in Chrome: its HDR10 base, in segments (the file keeps its Dolby Vision entry).
  const dv8 = copy({ video: { ...hevcHDR10, dv: { profile: 8, level: 6, compatibility: 1 } } });
  const d8 = await chooseDelivery([dv8], chrome(), { encoders, ...noMemory });
  assert.deepEqual([d8.delivery, d8.mediaType], ['remux', 'video/mp4']);
});

test('Vorbis a browser plays in WebM only: a WebM file as it is; in Matroska, no remux, the audio converted', async () => {
  // Firefox-like: Vorbis in WebM, never in MP4.
  const env = firefox({ containers: ['video/mp4', 'video/webm', 'audio/webm'], audio: ['mp4a.40.2', 'vorbis', 'Opus', 'opus'] });
  const { mse, canPlay } = env;
  const noMp4Vorbis = (t) => /^(audio|video)\/mp4/.test(t) && /vorbis/i.test(t);
  env.mse = (t) => !noMp4Vorbis(t) && mse(t);
  env.canPlay = (t) => (noMp4Vorbis(t) ? '' : canPlay(t));
  const vp9 = { codec: 'vp9', codecs: 'vp09.00.40.08', width: 1920, height: 1080, bitDepth: 8 };
  const audio = [{ codec: 'vorbis', channels: 2, language: 'en', default: true }];
  const webm = copy({ mediaType: 'video/webm', video: vp9, audio });
  assert.equal((await chooseDelivery([webm], env, { encoders, ...noMemory })).delivery, 'file');
  const mkv = copy({ mediaType: 'video/matroska', video: { codec: 'h264', codecs: 'avc1.640028', width: 1920, height: 1080, bitDepth: 8 }, audio });
  const d = await chooseDelivery([mkv], env, { encoders, ...noMemory });
  assert.notEqual(d.delivery, 'remux');
  assert.equal(d.transcode?.audioCodec, 'aac');
});

test('HDR10+ refused by decodingInfo is shown as HDR10: the picture copied, the audio alone converted', async () => {
  // Chromium: smpteSt2094-40 unsupported, smpteSt2086 fine, no E-AC-3.
  const env = chrome({ decode: (cfg) => ({ supported: cfg.video.hdrMetadataType !== 'smpteSt2094-40' }) });
  const re = copy({ mbps: 4.5, video: { ...hevcHDR10, codecs: 'hvc1.2.4.H150.90', width: 3832, height: 1600, hdr: ['hdr10plus'], dv: { profile: 8, level: 6, compatibility: 1 } },
    audio: [{ codec: 'eac3', channels: 6, language: 'fr', default: true }, { codec: 'eac3', channels: 6, language: 'en' }] });
  const d = await chooseDelivery([re], env, { encoders, ...noMemory });
  assert.deepEqual([d.level, d.delivery, d.transcode?.videoCodec, d.transcode?.audioCodec], [2, 'transcode', 'copy', 'aac']);
  // Refused as HDR10 too: the picture converted.
  const none = chrome({ decode: (cfg) => ({ supported: !cfg.video.transferFunction }) });
  assert.equal((await chooseDelivery([re], none, { encoders, ...noMemory })).level, 3);
});

test('the audio-track rule: a file only when the browser plays the chosen track, else a remux', async () => {
  const c = copy({ mediaType: 'video/mp4', audio: [
    { codec: 'aac', codecs: 'mp4a.40.2', channels: 2, language: 'en', default: true, original: true },
    { codec: 'aac', codecs: 'mp4a.40.2', channels: 2, language: 'fr' }] });
  const en = await chooseDelivery([c], chrome(), { language: 'en', ...noMemory });
  assert.deepEqual([en.delivery, en.audioTrack], ['file', 'a1']);
  const fr = await chooseDelivery([c], chrome(), { audio: 'fr', ...noMemory });
  assert.deepEqual([fr.delivery, fr.audioTrack, fr.reason], ['remux', 'a2', 'audio-track']);
  // Safari switches a file's tracks itself (audioTracks).
  assert.equal((await chooseDelivery([c], safari(), { audio: 'fr', ...noMemory })).delivery, 'file');
  // No track marked default: the first is the one the browser plays.
  const unmarked = copy({ mediaType: 'video/mp4', audio: [{ codec: 'aac', codecs: 'mp4a.40.2', language: 'en' }, { codec: 'aac', codecs: 'mp4a.40.2', language: 'fr' }] });
  assert.equal((await chooseDelivery([unmarked], chrome(), { audio: 'en', ...noMemory })).delivery, 'file');
  // No wish found: the original, else the default.
  assert.equal((await chooseDelivery([c], chrome(), { audio: 'de', language: 'ja', ...noMemory })).audioTrack, 'a1');
  // A region variant, else the language.
  assert.equal((await chooseDelivery([c], chrome(), { audio: 'fr-CA', ...noMemory })).audioTrack, 'a2');
});

test('a copy over the quality chosen or the operator\'s cap is converted, the cap passed on', async () => {
  const c = copy({ mediaType: 'video/mp4', mbps: 30 });
  const d = await chooseDelivery([c], chrome(), { encoders, maxBitrateKbps: 8500, ...noMemory });
  assert.deepEqual([d.delivery, d.transcode, d.reason], ['transcode', { videoCodec: 'h264', maxBitrateKbps: 8000, audioCodec: 'copy' }, 'bitrate']);
  const op = await chooseDelivery([c], chrome(), { encoders, limits: { maxBitrateKbps: 20000, originalFile: true }, ...noMemory });
  assert.equal(op.transcode.maxBitrateKbps, 20000);
  // The operator's switch off: never the file.
  const off = await chooseDelivery([copy({ mediaType: 'video/mp4' })], chrome(), { limits: { originalFile: false }, ...noMemory });
  assert.equal(off.delivery, 'remux');
});

test('conservative: an unknown token, a missing codecs string, a minimal copy are never played as they are', async () => {
  const vvc = await chooseDelivery([copy({ video: { codec: 'vvc', codecs: 'vvc1.1.L51.CQA' } })], chrome(), { encoders, ...noMemory });
  assert.deepEqual([vvc.delivery, vvc.transcode.videoCodec, vvc.reason], ['transcode', 'h264', 'video-codec']);
  const noCodecs = await chooseDelivery([copy({ video: { codecs: undefined } })], chrome(), { encoders, ...noMemory });
  assert.deepEqual([noCodecs.delivery, noCodecs.reason], ['transcode', 'video-codec']);
  const minimal = await chooseDelivery([copy({ video: null, audio: [] })], chrome(), { encoders, ...noMemory });
  assert.deepEqual([minimal.delivery, minimal.reason], ['', 'in-doubt']);
  const strange = await chooseDelivery([copy({ audio: [{ codec: 'wmapro', default: true }] })], chrome(), { encoders, ...noMemory });
  assert.equal(strange.reason, 'audio-codec');
  for (const video of [{ refFrames: 16 }, { interlaced: true }, { fps: '120/1' }]) {
    assert.equal((await chooseDelivery([copy({ mediaType: 'video/mp4', video })], chrome(), { encoders, ...noMemory })).delivery,
      video.refFrames ? 'file' : 'transcode', JSON.stringify(video));
  }
});

test('D11: own copies that play as they are first, the best picture; then a paired server\'s; then own, converted', async () => {
  const hd = copy({ mediaType: 'video/mp4' });
  const uhd = copy({ mediaType: 'video/mp4', video: { ...hevcHDR10, codecs: 'hvc1.2.4.L153.90' }, mbps: 40 });
  assert.equal((await chooseDelivery([hd, uhd], chrome(), { encoders, ...noMemory })).copy.id, uhd.id);
  // Firefox can't play the 4K one: the 1080p as it is.
  assert.equal((await chooseDelivery([uhd, hd], firefox(), { encoders, ...noMemory })).copy.id, hd.id);
  // Only a paired server's plays as it is: that one.
  const peer = copy({ mediaType: 'video/mp4' });
  const d = await chooseDelivery([uhd, peer], firefox(), { encoders, peers: [peer.id], ...noMemory });
  assert.equal(d.copy.id, peer.id);
  // Both play: own first.
  const own = copy({ mediaType: 'video/mp4' });
  assert.equal((await chooseDelivery([peer, own], chrome(), { peers: [peer.id], ...noMemory })).copy.id, own.id);
  // Nothing plays as it is: own first, converted.
  assert.equal((await chooseDelivery([uhd, { ...uhd, id: 'peer4k' }], firefox(), { encoders, peers: ['peer4k'], ...noMemory })).copy.id, uhd.id);
  // The audio wished for wins over the picture.
  const french = copy({ mediaType: 'video/mp4', audio: [{ codec: 'aac', codecs: 'mp4a.40.2', language: 'fr', default: true }] });
  assert.equal((await chooseDelivery([uhd, french], chrome(), { audio: 'fr', ...noMemory })).copy.id, french.id);
  // A version chosen is that one.
  assert.equal((await chooseDelivery([hd, uhd], chrome(), { versionId: hd.id, ...noMemory })).copy.id, hd.id);
});

function memoryStorage() {
  const m = new Map();
  return { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), removeItem: (k) => m.delete(k) };
}

test('the fallback ladder\'s memory: by the copy\'s facts, per device, with a reset; nothing breaks without storage', async () => {
  globalThis.localStorage = memoryStorage();
  const c = copy({ mediaType: 'video/mp4' });
  assert.equal((await chooseDelivery([c], chrome())).delivery, 'file');
  fallbacks.remember(c, 'a1', 2);
  // Another copy with the same facts: the same memory (never by ID).
  const twin = { ...c, id: 'twin', etag: 'other' };
  const d = await chooseDelivery([twin], chrome(), { encoders });
  assert.deepEqual([d.delivery, d.transcode, d.reason, d.level], ['transcode', { videoCodec: 'copy', audioCodec: 'aac' }, 'fallback', 2]);
  assert.equal(fallbacks.count(), 1);
  fallbacks.reset();
  assert.equal(fallbacks.count(), 0);
  assert.equal((await chooseDelivery([c], chrome())).delivery, 'file');
  // Storage that throws (private browsing, blocked): nothing remembered, nothing fails.
  globalThis.localStorage = { getItem() { throw new Error('denied'); }, setItem() { throw new Error('denied'); }, removeItem() { throw new Error('denied'); } };
  fallbacks.remember(c, 'a1', 3);
  assert.equal(fallbacks.level(c, 'a1'), 0);
  fallbacks.reset();
  delete globalThis.localStorage;
});

test('tracks offered: one per language and role, the one that plays first, then the most channels; forced subtitles paired with the audio', () => {
  const c = copy({ audio: [
    { codec: 'truehd', codecs: 'mlpa', channels: 8, language: 'en', objects: 'dolby-atmos', default: true },
    { codec: 'ac3', codecs: 'ac-3', channels: 6, language: 'en' },
    { codec: 'aac', codecs: 'mp4a.40.2', channels: 2, language: 'en' },
    { codec: 'aac', codecs: 'mp4a.40.2', channels: 2, language: 'en', role: 'commentary' },
    { codec: 'ac3', codecs: 'ac-3', channels: 6, language: 'fr' }],
  subtitles: [
    { language: 'fr', format: 'hdmv_pgs_subtitle' }, { language: 'fr', format: 'subrip' },
    { language: 'en', format: 'subrip', hearingImpaired: true }, { language: 'en', format: 'subrip' },
    { language: 'fr', format: 'subrip', forced: true }, { language: 'de', format: 'dvd_subtitle' }] });
  // Without a device: the most channels.
  assert.deepEqual(audioMenu(c).map((t) => t.id), ['a1', 'a4', 'a5']);
  // Safari plays AC-3, not TrueHD: the 5.1 AC-3 beside the 7.1.
  assert.deepEqual(audioMenu(c, safari()).map((t) => t.id), ['a2', 'a4', 'a5']);
  const subs = subtitleMenu(c);
  assert.deepEqual(subs.tracks.map((t) => t.id), ['s7', 's9']);
  assert.deepEqual(subs.forced.map((t) => t.id), ['s10']);
  assert.equal(forcedFor(c, 'a5')?.id, 's10');
  assert.equal(forcedFor(c, 'a1'), null);
  assert.equal(subtitlesFor(c, 'en')?.id, 's9');
  // Only what the playback serves.
  assert.deepEqual(subtitleMenu(c, [{ id: 's6', form: 'picture' }]).tracks.map((t) => t.id), ['s6']);
});

test('labels in English and French, from facts only', () => {
  const uhd = copy({ video: { ...hevcHDR10, hdr: ['hdr10plus'] }, edition: 'directors' });
  assert.equal(copyLabel(uhd, 'en'), '4K · HDR10+ · Director\'s cut');
  assert.equal(copyLabel(uhd, 'fr'), '4K · HDR10+ · Version du réalisateur');
  assert.equal(copyLabel(copy({ video: { ...hevcHDR10, dv: { profile: 8, level: 6, compatibility: 1 } } }), 'en'), '4K · Dolby Vision');
  assert.equal(copyLabel(copy({ video: { width: 1920, height: 800 }, threeD: 'sbs', edition: 'fan-edit' }), 'en'), '1080p · 3D');
  assert.equal(copyLabel(copy({ video: { width: 720, height: 576 } }), 'fr'), 'SD');
  assert.equal(copyLabel(copy({ video: null }), 'fr'), 'Version');
  // The codec only where two copies differ in nothing else.
  const a = copy(), b = copy({ video: { codec: 'hevc', codecs: 'hvc1.1.6.L120.90' } }), cc = copy({ video: hevcHDR10 });
  assert.deepEqual([a, b, cc].map((x) => copyLabel(x, 'en', [a, b, cc])), ['1080p · H.264', '1080p · HEVC', '4K · HDR10']);
  const fr = new Intl.DisplayNames(['fr-CA'], { type: 'language' }).of('fr-CA');
  const name = fr.charAt(0).toLocaleUpperCase('fr-CA') + fr.slice(1);
  assert.equal(audioLabel({ language: 'fr-CA', channels: 6, objects: 'dolby-atmos', original: true }, 'en'), `${name} · 5.1 · Atmos · original`);
  assert.equal(audioLabel({ language: 'en', channels: 2, role: 'commentary' }, 'fr'), 'English · stéréo · commentaire');
  assert.equal(audioLabel({ channels: 8, objects: 'dts-x' }, 'fr'), 'Langue inconnue · 7.1 · DTS:X');
  assert.equal(audioLabel({ language: 'ja', channels: 1, role: 'description' }, 'en'), '日本語 · mono · audio description');
  assert.equal(subtitleLabel({ language: 'en', forced: true }, 'fr'), 'English · forcés');
  assert.equal(subtitleLabel({ language: 'en', hearingImpaired: true }, 'en'), 'English · SDH');
  assert.equal(subtitleLabel({ language: 'en', hearingImpaired: true, role: 'easyreader' }, 'fr'), 'English · SME · lecture facile');
  assert.equal(subtitleLabel({ language: 'xx-invalid-' }, 'en'), 'Unknown language');
});

test('the play request carries the decision and the reason; the addresses come from the ticket', async () => {
  const c = copy({ video: hevcHDR10, audio: [{ codec: 'eac3', channels: 6, language: 'en', default: true }], previewLayout: { tileWidth: 320, tileHeight: 180, columns: 10, rows: 10, intervalMs: 10000, tileCount: 250 } });
  const d = await chooseDelivery([c], chrome(), { encoders, ...noMemory });
  assert.deepEqual(playRequest(d, { titleId: 'title_movie_tmdb_1', startPositionMs: 1234.4, maxBitrateKbps: 0 }), {
    titleId: 'title_movie_tmdb_1', versionId: c.id, startPositionMs: 1234, audioTrack: 'a1', delivery: 'transcode',
    transcode: { videoCodec: 'copy', audioCodec: 'aac' }, reason: 'audio-codec' });
  const base = playRequest({ copy: c, audioTrack: '', delivery: '', reason: 'in-doubt' }, { titleId: 't' });
  assert.deepEqual(base, { titleId: 't', versionId: c.id, startPositionMs: 0, reason: 'in-doubt' });
  const urls = streamUrls('/api/v1', { ticket: 'tk_1', protocol: 'hls' });
  assert.equal(urls.stream, '/api/v1/stream/tk_1/master.m3u8');
  assert.equal(streamUrls('/api/v1', { ticket: 'tk_1', protocol: 'file' }).stream, '/api/v1/stream/tk_1/file');
  assert.equal(urls.subtitles('s3'), '/api/v1/stream/tk_1/subtitles/s3');
  assert.equal(urls.pictures('s4'), '/api/v1/stream/tk_1/pictures/s4');
  const sheets = previewSheets(c.previewLayout, urls.sheet);
  assert.deepEqual(sheets.sheets, ['/api/v1/stream/tk_1/trickplay/0', '/api/v1/stream/tk_1/trickplay/1', '/api/v1/stream/tk_1/trickplay/2']);
  assert.equal(previewSheets(undefined, urls.sheet), undefined);
});

// A server with a title's page (its copies) and the play request.
function copyServer(page, { refuse = () => null } = {}) {
  let n = 0;
  const calls = fakeServer({
    'GET /api/v1/playback/pb1': () => [200, {}],
    [`GET /api/v1/titles/${page.id}`]: () => [200, page],
    'POST /api/v1/playback': (_, body) => refuse(body) || [201, {
      id: `pb${++n}`, ticket: `tk${n}`, protocol: body.delivery === 'file' ? 'file' : 'hls', method: 'direct', etag: 'e',
      positionMs: body.startPositionMs, versionId: body.versionId, audioTrack: body.audioTrack || 'a1',
      subtitles: [{ id: 's2', form: 'text' }], title: { id: page.id, name: 'Film' } }],
    'POST /api/v1/playback/pb1/stop': () => [204, null],
    'POST /api/v1/playback/pb2/stop': () => [204, null],
    'POST /api/v1/playback/pb3/stop': () => [204, null],
    'POST /api/v1/playback/pb1/progress': () => [204, null],
    'PUT /api/v1/playback/pb1/tracks': () => [204, null],
  });
  return calls;
}

test('the player plays a copy it chose: MSFP\'s play request, the stream from the ticket, labels and tracks from the copy', async () => {
  const env = chrome();
  globalThis.localStorage = memoryStorage();
  const c = copy({ mediaType: 'video/mp4', audio: [{ codec: 'aac', codecs: 'mp4a.40.2', channels: 6, language: 'en', default: true }],
    subtitles: [{ language: 'fr', format: 'subrip' }], segments: [{ kind: 'intro', startMs: 1000, endMs: 20000 }] });
  const page = { id: 'title_movie_tmdb_1', copies: [c], limits: { originalFile: true }, wishes: { subtitles: 'fr' }, userState: { positionMs: 5000 } };
  const calls = copyServer(page);
  globalThis.document = { createElement: () => ({ dataset: {}, track: { mode: 'disabled', cues: [], addCue() {} }, dispatchEvent() {} }) };
  globalThis.fetch = ((api) => (url, init) => (url.includes('/subtitles/') ? Promise.reject(new Error('later')) : api(url, init)))(globalThis.fetch);
  const player = await new Overfin().play(fakeVideo(), page.id, { env, language: 'fr' });
  const body = calls.find((x) => x.url === '/api/v1/playback').body;
  assert.deepEqual(body, { titleId: page.id, versionId: c.id, startPositionMs: 5000, audioTrack: 'a1', delivery: 'file', reason: 'as-is' });
  const pb = player.playback;
  assert.equal(pb.stream.url, '/api/v1/stream/tk1/file');
  assert.equal(pb.audioTracks[0].label, 'English · 5.1');
  assert.equal(pb.subtitleTracks[0].url, '/api/v1/stream/tk1/subtitles/s2');
  assert.equal(pb.subtitleTrack, 's2');
  assert.equal(pb.versions[0].name, '1080p');
  assert.equal(pb.delivery.kind, 'file');
  assert.equal(pb.durationMs, c.durationMs);
  assert.equal(player.segmentAt(2000).kind, 'intro');
  await player.close();
  delete globalThis.localStorage;
});

test('refused as asked (409 delivery-refused): the player steps down a level and asks again', async () => {
  const c = copy({ mediaType: 'video/mp4' });
  const page = { id: 'title_movie_tmdb_2', copies: [c], encoders };
  const calls = copyServer(page, { refuse: (b) => (b.delivery === 'file' ? [409, { type: 'delivery-refused', status: 409, detail: 'original-file-off' }] : null) });
  const v = fakeVideo();
  const player = await new Overfin().play(v, page.id, { env: chrome(), startMs: 0, fallbacks: null, Hls: fakeHls(v, []) });
  const asked = calls.filter((x) => x.url === '/api/v1/playback').map((x) => x.body.delivery);
  assert.deepEqual(asked, ['file', 'remux']);
  assert.equal(player.playback.delivery.reason, 'fallback');
  await player.close();
});

test('the ladder: a play with no audio heard in its first seconds steps down once, remembered for this device', async () => {
  globalThis.localStorage = memoryStorage();
  const c = copy({ mediaType: 'video/mp4' });
  const page = { id: 'title_movie_tmdb_3', copies: [c], encoders };
  const calls = copyServer(page);
  const video = fakeVideo();
  const player = await new Overfin().play(video, page.id, { env: chrome(), startMs: 0, Hls: fakeHls(video, []) });
  assert.equal(player.playback.delivery.kind, 'file');
  // It plays, a picture shows, but nothing of its audio was decoded.
  Object.assign(video, { currentTime: 4, videoWidth: 1920, webkitAudioDecodedByteCount: 0 });
  video.emit('timeupdate');
  await settle();
  const asked = calls.filter((x) => x.url === '/api/v1/playback').map((x) => x.body);
  assert.deepEqual(asked.map((b) => b.delivery), ['file', 'remux']);
  assert.equal(asked[1].reason, 'fallback');
  assert.equal(asked[1].startPositionMs, 4000);
  assert.equal(player.playback.delivery.fallback, 'no-audio');
  assert.equal(fallbacks.level(c, 'a1'), 1);
  // Heard this time: no further step.
  Object.assign(video, { currentTime: 9, webkitAudioDecodedByteCount: 50_000 });
  video.emit('timeupdate');
  await settle();
  assert.equal(calls.filter((x) => x.url === '/api/v1/playback').length, 2);
  await player.close();
  delete globalThis.localStorage;
});

test('the ladder: a media error in a play\'s first seconds steps down; later, it starts over as before', async () => {
  const c = copy({ mediaType: 'video/mp4' });
  const page = { id: 'title_movie_tmdb_4', copies: [c], encoders };
  const calls = copyServer(page);
  const video = fakeVideo();
  const player = await new Overfin().play(video, page.id, { env: chrome(), startMs: 0, fallbacks: null, Hls: fakeHls(video, []) });
  video.error = { code: 3 }; // a decode error
  video.emit('error');
  await settle();
  assert.deepEqual(calls.filter((x) => x.url === '/api/v1/playback').map((x) => x.body.delivery), ['file', 'remux']);
  assert.equal(player.playback.delivery.fallback, 'media-error');
  await player.close();
});

test('the ladder: a play that never gets going steps down; a file\'s chosen audio is turned on in the video', async (t) => {
  const c = copy({ mediaType: 'video/mp4', audio: [
    { codec: 'aac', codecs: 'mp4a.40.2', language: 'en', default: true }, { codec: 'aac', codecs: 'mp4a.40.2', language: 'fr' }] });
  const page = { id: 'title_movie_tmdb_5', copies: [c], encoders, wishes: { audio: 'fr' } };
  const calls = copyServer(page);
  // Safari: audioTracks, so the French track plays from the file itself.
  const video = fakeVideo();
  video.audioTracks = [{ enabled: true }, { enabled: false }];
  const player = await new Overfin().play(video, page.id, { env: safari(), startMs: 0, fallbacks: null, Hls: fakeHls(video, []) });
  assert.equal(player.playback.delivery.kind, 'file');
  video.emit('loadedmetadata');
  assert.deepEqual(video.audioTracks.map((a) => a.enabled), [false, true]);
  await player.close();
  // A file that plays nothing steps down after a while: autoplay still
  // waiting for its first pictures (the video says paused until then).
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const stuck = Object.assign(fakeVideo(), { paused: true, autoplay: true, readyState: 1 });
  const p2 = await new Overfin().play(stuck, 'title_movie_tmdb_5', { env: chrome(), startMs: 0, fallbacks: null, Hls: fakeHls(stuck, []), audioTrack: 'a1' });
  assert.equal(p2.playback.delivery.kind, 'file');
  t.mock.timers.tick(15_000);
  t.mock.timers.reset();
  await settle();
  const asked = calls.filter((x) => x.url === '/api/v1/playback').map((x) => x.body.delivery);
  assert.deepEqual(asked.slice(-2), ['file', 'remux']);
  assert.equal(p2.playback.delivery.fallback, 'stalled');
  await p2.close();
});

test('the ladder leaves alone what isn\'t the device\'s failure: a network error, a playback taken over', async () => {
  const c = copy({ mediaType: 'video/mp4' });
  const page = { id: 'title_movie_tmdb_6', copies: [c], encoders };
  const calls = copyServer(page);
  const video = fakeVideo();
  const player = await new Overfin().play(video, page.id, { env: chrome(), startMs: 0, fallbacks: null, Hls: fakeHls(video, []), onTakenOver: () => {} });
  video.error = { code: 2 }; // the network
  video.emit('error');
  await settle();
  assert.ok(calls.filter((x) => x.url === '/api/v1/playback').every((x) => x.body.delivery === 'file'));
  // Taken over: a decode error then is the takeover's, never a step down.
  globalThis.fetch = ((api) => (url, init) => (url === '/api/v1/playback/pb1' || url === '/api/v1/playback/pb2'
    ? Promise.resolve({ status: 410, ok: false, json: async () => ({ type: 'playback-taken-over', status: 410 }) }) : api(url, init)))(globalThis.fetch);
  video.error = { code: 3 };
  video.emit('error');
  await settle();
  assert.ok(calls.filter((x) => x.url === '/api/v1/playback').every((x) => x.body.delivery === 'file'));
  await player.close();
});
