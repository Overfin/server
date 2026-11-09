// Overfin JavaScript SDK.
//
// One ES module, no dependencies, no build step: import it straight from a
// browser. It wraps the API (/api/v1) and carries the behavior every client
// needs: following the login when it lapses, typed errors, telling Overfin
// what the browser can play, and a player that reports progress, recovers
// streams the server freed, and handles takeovers.
//
//   import { Overfin } from './overfin.js';
//   const overfin = new Overfin();
//   const me = await overfin.me();
//
// Types are in overfin.d.ts; the API itself is specified in
// api/openapi.yaml.

/** An error from the API: an RFC 9457 problem. Branch on `type`. */
export class OverfinError extends Error {
  constructor(status, problem) {
    super(problem.detail || problem.title || `HTTP ${status}`);
    this.name = 'OverfinError';
    this.status = status;
    this.type = problem.type || 'internal-error';
    this.problem = problem;
  }
  /** Seconds to wait before trying again, for rate-limited and pin-locked. */
  get retryAfter() { return this.problem.retryAfter; }
}

export class Overfin {
  /**
   * @param {object} [options]
   * @param {string} [options.base] Where the API is; '/api/v1' by default.
   * @param {(loginUrl: string) => void} [options.onLoginRequired] Called when
   *   the login lapsed. By default, the browser goes through the login and
   *   comes back to the current page.
   */
  constructor({ base = '/api/v1', onLoginRequired } = {}) {
    this.base = base;
    this.onLoginRequired = onLoginRequired || ((url) => { location.href = url; });
  }

  // ---- Requests ----------------------------------------------------------

  /** Calls the API. Resolves with the JSON body, or null for no content. */
  async request(method, path, body, { keepalive = false } = {}) {
    const init = { method, headers: {}, keepalive };
    // The server takes changes as JSON only: a change with nothing to say
    // sends an empty object.
    if (body === undefined && (method === 'PUT' || method === 'POST' || method === 'PATCH')) body = {};
    if (body !== undefined) {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
    const res = await fetch(this.base + path, init);
    if (res.status === 204) return null;
    // A body that can't be read: an error's still says its status; a
    // success's (cut short: the page left, the network dropped) is no
    // answer, not an empty one.
    let data;
    try {
      data = await res.json();
    } catch (err) {
      if (res.ok) throw err;
      data = {};
    }
    if (!res.ok) {
      const err = new OverfinError(res.status, data);
      if (err.type === 'not-authenticated') this.onLoginRequired(this.loginUrl());
      throw err;
    }
    return data;
  }

  /**
   * Makes several GET calls in one request: each is a path as the methods
   * here use them ('/titles/continue'), with its query as an object or
   * not. Resolves, in order, with each call's body, or its OverfinError
   * (resolved, not thrown: one failing call doesn't fail the others). At
   * most 20 calls; images, streams and logins can't be batched.
   */
  async batch(calls) {
    const prefix = new URL(this.base, 'http://overfin.invalid').pathname.replace(/\/$/, '');
    const requests = calls.map((c) => {
      const [path, query] = Array.isArray(c) ? c : [c, {}];
      return prefix + path + toQuery(query);
    });
    const { responses } = await this.request('POST', '/batch', { requests });
    return responses.map(({ status, body }) => (status >= 200 && status < 300 ? (body ?? null)
      : new OverfinError(status, body || {})));
  }

  /** The login address, coming back to returnTo (the current page by default). */
  loginUrl(returnTo = location.pathname + location.search + location.hash) {
    return `${this.base}/login?return=${encodeURIComponent(returnTo)}`;
  }

  /**
   * Confirms a fresh login before a destructive action: the browser goes to
   * the identity provider and comes back to returnTo. Call it after an
   * OverfinError of type 'step-up-required', then retry the action.
   */
  stepUp(error, returnTo = location.pathname + location.search + location.hash) {
    const url = error?.problem?.stepUpUrl;
    if (!url) throw new Error('this server offers no step-up');
    location.href = `${url}?return=${encodeURIComponent(returnTo)}`;
  }

  // ---- Account -----------------------------------------------------------

  /** The account and active profile. Fails with 'account-setup-required' before the first visit. */
  me() { return this.request('GET', '/me'); }
  /** Creates a fresh account. */
  createAccount() { return this.request('POST', '/me', {}); }
  /** Changes the account's language, among those offered. Owner profile only. */
  setLanguage(language) { return this.request('PATCH', '/me', { language }); }
  /** What the account said it likes, and the genres to choose among. Owner profile only, when the operator asks. */
  tastes() { return this.request('GET', '/me/tastes'); }
  /** Keeps what the account likes; nothing said still records it was asked. Owner profile only. */
  setTastes(tastes) { return this.request('PUT', '/me/tastes', tastes); }
  /** Starts a new account with the history of one on the server this site replaces. The password is used once. */
  claimAccount(username, password) { return this.request('POST', '/me/claim', { username, password }); }
  /**
   * Whether the active profile may bring its history from the server this
   * site replaces: migration on, never done for this account, not a kids
   * profile. Resolves with { available }; rejects with 404 when the
   * server has no migration.
   */
  historyCopy() { return this.request('GET', '/me/history/copy'); }
  /**
   * Copies the history of an account on that other server into the active
   * profile, with its username and password there (used once, not kept).
   */
  copyHistory(username, password) { return this.request('POST', '/me/history/copy', { username, password }); }
  /**
   * Deletes the account and everything Overfin holds about it. Owner only;
   * needs a fresh login (step-up). Resolves with null (before 0.8, with
   * what became of its requests in Seerr).
   */
  deleteAccount() { return this.request('DELETE', '/me'); }
  /**
   * What Overfin holds about the account (from the owner profile, after a
   * fresh login) or about this profile only (with its PIN, if it has one),
   * with the watch history.
   */
  exportData(pin) { return this.request('POST', '/me/export', pin ? { pin } : {}); }
  /** Ends this session (and the login). Resolves with { logoutUrl? }. */
  logout() { return this.request('POST', '/logout', {}); }
  /**
   * With managed sign-in only (404 otherwise): how the person signed in
   * signs in. Resolves with { email, hasPassword, reset }, reset telling
   * whether a forgotten password can be reset by email. The account
   * owner's profile only.
   */
  signinInfo() { return this.request('GET', '/me/signin'); }
  /**
   * With Overfin's own sign-in: { email, unconfirmed }, unconfirmed telling
   * whether the person's address still needs confirming (they chose their
   * password from a link an admin copied). The account owner's profile only.
   */
  addressInfo() { return this.request('GET', '/me/address'); }
  /** Emails the link that confirms the address; resolves with { sent }. */
  confirmAddress() { return this.request('POST', '/me/address', {}); }

  /** Whose brand to show: Overfin's, or the operator's own. No sign-in needed. */
  branding() { return this.request('GET', '/branding'); }
  /**
   * Sets or changes the password; needs a fresh login (step-up) and the
   * account owner's profile. Other browsers sign in again.
   */
  setPassword(password) { return this.request('PUT', '/me/password', { password }); }

  // ---- Profiles ----------------------------------------------------------

  async profiles() { return (await this.request('GET', '/profiles')).items; }
  profile(id) { return this.request('GET', `/profiles/${enc(id)}`); }
  /** @param {{name: string, kind?: 'standard'|'kids', maxAge?: number}} profile */
  createProfile(profile) { return this.request('POST', '/profiles', profile); }
  /** Changes name, kind, age, libraries or hidden tags; limits need the owner's `pin` if set. */
  updateProfile(id, changes) { return this.request('PATCH', `/profiles/${enc(id)}`, changes); }
  /** Deletes a profile and its history. Needs a fresh login (step-up). */
  deleteProfile(id) { return this.request('DELETE', `/profiles/${enc(id)}`); }
  /** Switches this session to a profile, with its PIN if it has one. */
  activateProfile(id, pin) { return this.request('POST', `/profiles/${enc(id)}/activate`, pin ? { pin } : {}); }
  setPin(id, pin, ownerPin) { return this.request('PUT', `/profiles/${enc(id)}/pin`, ownerPin ? { pin, ownerPin } : { pin }); }
  removePin(id) { return this.request('DELETE', `/profiles/${enc(id)}/pin`); }
  /** The library's tags, to hide some from a profile (owner only). */
  async tags() { return (await this.request('GET', '/tags')).items; }
  async sessions() { return (await this.request('GET', '/sessions')).items; }
  endSession(id) { return this.request('DELETE', `/sessions/${enc(id)}`); }
  endOtherSessions() { return this.request('DELETE', '/sessions'); }

  // ---- Library -----------------------------------------------------------

  async libraries() { return (await this.request('GET', '/libraries')).items; }
  /** A title's page, in a library or not: a series with its seasons, a season with its episodes. */
  title(id) { return this.request('GET', `/titles/${enc(id)}`); }
  /** The titles that fit the profile best, among all or the newest (since 0.6). */
  async recommended(scope = 'all', limit = 20) { return (await this.request('GET', `/titles/recommended${toQuery({ scope, limit })}`)).items; }
  /** Titles like this one: those the server has first (since 0.6). */
  async similar(id, limit = 20) { return (await this.request('GET', `/titles/${enc(id)}/similar${toQuery({ limit })}`)).items; }
  /** The collections of films (TMDB's series of films) the profile has films of, without their films (since 0.7). */
  async collections() { return (await this.request('GET', '/collections')).items; }
  /** A collection with its films, in release order (since 0.7). */
  collection(id) { return this.request('GET', `/collections/${enc(id)}`); }
  /** Searches titles (in a library or not) and people; the terms travel in the body. */
  search(query) { return this.request('POST', '/search', { query }); }
  /** A person's page, from TMDB, with the titles they're in. */
  person(id) { return this.request('GET', `/people/${enc(id)}`); }

  /**
   * One page of a library's titles. query: library or category, kind
   * ('movie', 'series', or both; required), genre (one or several),
   * yearFrom, yearTo, played, favorite,
   * inProgress, sort (name, addedAt, premiereDate, communityRating,
   * lastPlayed, random), order (asc, desc), seed, limit, cursor. Resolves
   * with { items, nextCursor?, totalCount?, seed? }.
   */
  titles(query = {}) { return this.request('GET', '/titles' + toQuery(query)); }

  /** Every page of titles, one after the other: for await (const page of overfin.pages(q)). */
  async *pages(query = {}) {
    let cursor = query.cursor;
    let seed = query.seed;
    do {
      const page = await this.titles({ ...query, cursor, seed });
      yield page;
      cursor = page.nextCursor;
      seed = seed || page.seed;
    } while (cursor);
  }

  /** Continue Watching: started titles and each followed series' next episode, a library's or not. */
  async continueWatching(limit = 20) { return (await this.request('GET', `/titles/continue${toQuery({ limit })}`)).items; }
  async genres(libraryId, categoryId) { return (await this.request('GET', `/genres${toQuery({ library: libraryId, category: categoryId })}`)).items; }
  /** The operator's categories, and whether the menu shows the libraries. */
  categories() { return this.request('GET', '/categories'); }
  /** Every language Overfin names, each in itself: { languages: [{ tag, name }] }. */
  languages() { return this.request('GET', '/languages'); }
  /** What's new: films and series, a series by its newest episode. */
  async latest(libraryId, limit = 16) { return (await this.request('GET', `/titles/latest${toQuery({ library: libraryId, limit })}`)).items; }

  // ---- What the profile does with titles -----------------------------------

  /** Marks a title watched (a series or a season: its aired episodes), in a library or not, or not. Returns its new state. */
  setPlayed(id, played = true) { return this.request(played ? 'PUT' : 'DELETE', `/titles/${enc(id)}/played`); }
  /** Marks a title a favorite, or not. Returns its new state. */
  setFavorite(id, favorite = true) { return this.request(favorite ? 'PUT' : 'DELETE', `/titles/${enc(id)}/favorite`); }
  /** Takes a title out of Continue Watching; whether it was watched stays. Returns its new state. */
  forgetProgress(id) { return this.request('DELETE', `/titles/${enc(id)}/progress`); }
  /** What playing a series plays: { title (an episode), positionMs }. */
  upNext(seriesId) { return this.request('GET', `/titles/${enc(seriesId)}/up-next`); }
  /** Forgets what the active profile watched. Not for kids profiles. */
  clearHistory() { return this.request('DELETE', '/me/history'); }

  // ---- Watchlist (the watchlist plugin) -------------------------------------

  /** The profile's watchlist: films, series, seasons, episodes, in a library or not; newest first. */
  async watchlist() { return (await this.request('GET', '/me/watchlist')).items; }
  /** Whether a title is on the watchlist. */
  async onWatchlist(id) {
    try {
      const out = await this.request('GET', `/me/watchlist/${enc(id)}`);
      return out ? out.on === true : true; // before 0.5: 204 when on, 404 when off
    } catch (err) {
      if (err.status === 404) return false;
      throw err;
    }
  }
  addToWatchlist(id) { return this.request('PUT', `/me/watchlist/${enc(id)}`); }
  removeFromWatchlist(id) { return this.request('DELETE', `/me/watchlist/${enc(id)}`); }

  // ---- Wishlist (when the server offers it: offers.wishes) -------------------

  /** The profile's wishlist, in its order, with each wish's state, and what arrived since it was last told: { on, items, arrived }. */
  wishlist() { return this.request('GET', '/me/wishlist'); }
  /** Puts a film or a season at the end of the wishlist; for a series, each of its seasons out now not here whole. */
  addToWishlist(id) { return this.request('PUT', `/me/wishlist/${enc(id)}`); }
  /** Takes a film or a season off the wishlist; for a series, all its seasons. */
  removeFromWishlist(id) { return this.request('DELETE', `/me/wishlist/${enc(id)}`); }
  /** Sets the wishlist's order: every title on it, each once, first the one wanted most (a series' seasons stay together, by number). */
  orderWishlist(ids) { return this.request('PUT', '/me/wishlist', { titles: ids }); }
  /** Marks what arrived as told. */
  wishesSeen() { return this.request('POST', '/me/wishlist/seen'); }
  /** Whether a profile may wish (owner profile only): { on }. */
  profileWishing(profileId) { return this.request('GET', `/profiles/${enc(profileId)}/wishing`); }
  setProfileWishing(profileId, on) { return this.request('PUT', `/profiles/${enc(profileId)}/wishing`, { on }); }

  // ---- Discovery (when the server has Seerr connected) ----------------------

  /**
   * A page of trending, popular or upcoming titles. list: 'trending',
   * 'popular' or 'upcoming'; mediaKind: 'movie' or 'series'; availability:
   * 'all' (the default), 'library' or 'missing'. A filtered page may hold
   * fewer titles than asked for; nextCursor says whether there are more.
   */
  discover(list, mediaKind, { availability, limit, cursor } = {}) {
    return this.request('GET', `/discover${toQuery({ list, mediaKind, availability, limit, cursor })}`);
  }

  /** The address of an image reference, in a size: 'small', 'medium' or 'large'. */
  imageUrl(ref, size = 'medium') { return ref ? `${ref.url}?size=${size}` : ''; }

  // ---- Playback ----------------------------------------------------------

  /** Starts a playback. Most clients want play() instead, which does the rest. */
  startPlayback(request) { return this.request('POST', '/playback', request); }

  /**
   * Sets the subtitles the active profile prefers, a language ('' for
   * none), as updateProfile's subtitleLanguage: its next playbacks start
   * with them when the video has them and nothing else was chosen for the
   * title.
   */
  setSubtitleLanguage(language) { return this.request('PUT', '/me/subtitles', { language }); }
  /**
   * Keeps the audio or subtitles chosen while watching for the playback's
   * title ({ audioTrack } or { subtitleTrack }, '' for none): its next
   * playbacks start with them. Player does it for you.
   */
  chooseTracks(id, choice) { return this.request('PUT', `/playback/${enc(id)}/tracks`, choice); }
  playback(id) { return this.request('GET', `/playback/${enc(id)}`); }
  currentPlayback() { return this.request('GET', '/playback/current'); }
  reportProgress(id, report) { return this.request('POST', `/playback/${enc(id)}/progress`, report); }
  stopPlayback(id, positionMs, { keepalive = false } = {}) {
    return this.request('POST', `/playback/${enc(id)}/stop`, { positionMs }, { keepalive });
  }

  /**
   * Plays a title in a <video> element, and keeps it playing: see Player.
   * @param {HTMLVideoElement} video
   * @param {string} titleId
   * @param {PlayerOptions} [options]
   */
  async play(video, titleId, options = {}) {
    const player = new Player(this, video, titleId, options);
    await player.start();
    return player;
  }
}

// ---- Capabilities ---------------------------------------------------------

/**
 * What this browser can play, for Overfin to pick direct play, repackaging or
 * transcoding. Clients can adjust it before passing it on.
 */
export function capabilities() {
  const video = document.createElement('video');
  const mse = globalThis.ManagedMediaSource || globalThis.MediaSource;
  const can = (mime) => Boolean((mse && mse.isTypeSupported(mime)) || video.canPlayType(mime).replace('no', ''));
  const any = (...mimes) => mimes.some(can);
  const pick = (table) => Object.entries(table).filter(([, mimes]) => any(...[].concat(mimes))).map(([name]) => name);
  const { firefox, chromium, safari, mac } = browserFamily();
  // Firefox says it plays Matroska, but loads the whole file before playing
  // it, which streaming can't afford: it gets it repackaged instead.
  const containers = pick({ mp4: 'video/mp4; codecs="avc1.640028"', webm: 'video/webm; codecs="vp9"' });
  if (!firefox && any('video/x-matroska', 'video/mkv')) containers.push('mkv');
  // Desktop Chromium browsers (Chrome, Edge…) and Safari map HDR video onto
  // the screen themselves, whatever the screen; Firefox does only on macOS.
  // Elsewhere, the screen says.
  const hdr = matchMedia('(dynamic-range: high)').matches || chromium || safari || (firefox && mac);
  return {
    containers,
    videoCodecs: pick({
      h264: 'video/mp4; codecs="avc1.640028"',
      // Main (8-bit) or Main 10, which most HDR films are.
      hevc: ['video/mp4; codecs="hvc1.1.6.L120.90"', 'video/mp4; codecs="hev1.1.6.L120.90"',
        'video/mp4; codecs="hvc1.2.4.L153.90"', 'video/mp4; codecs="hev1.2.4.L153.90"'],
      av1: 'video/mp4; codecs="av01.0.08M.08"',
      vp9: 'video/mp4; codecs="vp09.00.10.08"',
    }),
    audioCodecs: pick({
      aac: 'audio/mp4; codecs="mp4a.40.2"',
      mp3: 'audio/mpeg',
      opus: 'audio/mp4; codecs="opus"',
      flac: 'audio/mp4; codecs="flac"',
      ac3: 'audio/mp4; codecs="ac-3"',
      eac3: 'audio/mp4; codecs="ec-3"',
    }),
    // Browsers decode 4K whatever the screen: scaling it down on the server
    // means re-encoding it. The bitrate limits still apply.
    maxHeight: 2160,
    hdr: hdr ? ['hdr10', 'hlg'] : [],
    subtitles: ['vtt'],
    maxAudioChannels: 2,
  };
}

// browserFamily tells the browsers apart where what they say they play
// isn't enough: whether they map HDR themselves, and Firefox's Matroska.
function browserFamily() {
  const ua = navigator.userAgent;
  const firefox = /Firefox\//.test(ua);
  const mobile = /Mobile|Android/.test(ua);
  const chromium = !firefox && /Chrom(e|ium)\//.test(ua) && !mobile;
  const safari = !firefox && !/Chrom(e|ium)\//.test(ua) && /Safari\//.test(ua);
  return { firefox, chromium, safari, mac: /Mac OS X|Macintosh/.test(ua) };
}

// ---- Player ---------------------------------------------------------------

/**
 * @typedef {object} PlayerOptions
 * @property {number} [startMs] Where to start; by default where the profile
 *   left off.
 * @property {string} [audioTrack]
 * @property {string} [subtitleTrack]
 * @property {string} [audioLanguage] The language to start in (the app's),
 *   when the video has it and no audioTrack is given.
 * @property {object} [capabilities] What the browser can play; detected by default.
 * @property {(playback: object) => void} [onStart] Called on every (re)start,
 *   with the playback: tracks, segments, method.
 * @property {(device: string) => void} [onTakenOver] Another device of the
 *   account took over; player.continueHere() takes it back.
 * @property {(error: Error) => void} [onError] The playback can't go on.
 * @property {() => void} [onEnded] Played to the end.
 * @property {object} [Hls] The hls.js constructor, for browsers without
 *   native HLS; globalThis.Hls by default.
 * @property {HTMLCanvasElement} [pictureCanvas] Where subtitles that are
 *   pictures are drawn; by default a canvas laid over the video.
 */

const PROGRESS_EVERY = 10_000;   // ms between progress reports while playing
const LONG_PAUSE = 90_000;       // after this, check the stream still exists

/**
 * Keeps one item playing in a <video> element: reports progress, restarts a
 * stream the server freed (after a long pause or a lost connection) at the
 * same place without a word, switches audio and subtitles, and reports a
 * takeover by another device.
 */
export class Player {
  constructor(client, video, titleId, options = {}) {
    this.client = client;
    this.video = video;
    this.titleId = titleId;
    this.options = options;
    // What the server is asked for (audio, a bitrate), and the subtitles
    // shown: chosen ('' none), or null, the playback's own.
    this.choice = { audioTrack: options.audioTrack };
    this.shown = options.subtitleTrack ?? null;
    this.pictures = null; // PictureSubtitles, once a track of pictures shows
    this.playback = null;
    this.hls = null;
    this.timer = null;
    this.pausedAt = 0;
    this.closed = false;
    // starts counts start() calls: one overtaken by a newer one, or by
    // close(), stops what it started and goes no further.
    this.starts = 0;
    this.handlers = {
      pause: () => { this.pausedAt = Date.now(); this.report(); },
      play: () => {
        if (this.pausedAt && Date.now() - this.pausedAt > LONG_PAUSE) this.check();
        this.pausedAt = 0;
      },
      timeupdate: () => this.settled(),
      seeked: () => { this.settled(); this.report(); },
      ended: () => { this.report({ ended: true }); this.close(); this.options.onEnded?.(); },
    };
  }

  /** The position, in ms. */
  // While a stream is being replaced, the video says 0 (hls.js resets it when
  // it goes): the position from before it (held), so nothing restarts from
  // the beginning or reports it.
  get positionMs() { return this.held ?? Math.round(this.video.currentTime * 1000); }

  /**
   * Starts, or restarts at the current position. Resolves with the
   * playback, or null when close() or a newer start came first.
   */
  async start(startMs = this.options.startMs) {
    this.teardown();
    const run = ++this.starts;
    const overtaken = () => this.closed || run !== this.starts;
    let at = startMs;
    if (at === undefined) {
      const title = await this.client.title(this.titleId);
      if (overtaken()) return null;
      at = title.userState?.positionMs || 0;
    }
    let pb;
    try {
      pb = await this.client.startPlayback({
        titleId: this.titleId,
        capabilities: this.options.capabilities || capabilities(),
        startPositionMs: Math.round(at),
        ...clean({ audioLanguage: this.options.audioLanguage }),
        ...clean(this.choice),
      });
    } catch (err) {
      if (overtaken()) return null;
      this.options.onError?.(err);
      throw err;
    }
    if (overtaken()) {
      // Nobody watches it: the server frees it at once.
      this.client.stopPlayback(pb.id, Math.round(at)).catch(() => {});
      return null;
    }
    this.playback = pb;
    this.attach(pb);
    for (const [event, fn] of Object.entries(this.handlers)) this.video.addEventListener(event, fn);
    this.timer = setInterval(() => { if (!this.video.paused) this.report(); }, PROGRESS_EVERY);
    this.options.onStart?.(pb);
    return pb;
  }

  // attach points the video element at the stream, with its subtitles.
  attach(pb) {
    const video = this.video;
    for (const t of video.querySelectorAll('track[data-overfin]')) t.remove();
    // A track shown holds the video back until it's loaded, and a title's
    // first subtitles can take long (the server reads the whole file for
    // them): each track joins the video once it's here, playback never
    // waits for it.
    for (const t of [...(pb.subtitleTracks || []), ...(pb.forcedTracks || [])]) {
      if (!t.url || t.pictures) continue;
      const add = () => {
        if (this.playback !== pb || this.video !== video) return; // another title by now
        const el = document.createElement('track');
        Object.assign(el, { kind: 'subtitles', src: t.url, srclang: t.language || 'und', label: t.label });
        el.dataset.overfin = t.id;
        video.append(el);
        this.showSubtitles();
      };
      fetch(t.url, { credentials: 'same-origin' }).then(add, add);
    }
    const startAt = (pb.positionMs || 0) / 1000;
    const Hls = this.options.Hls || globalThis.Hls;
    if (pb.stream.protocol === 'hls' && Hls && Hls.isSupported()) {
      this.hls = new Hls({ startPosition: startAt });
      this.hls.on(Hls.Events.ERROR, (_, data) => { if (data.fatal) this.fatal(data); });
      this.hls.loadSource(pb.stream.url);
      this.hls.attachMedia(video);
    } else if (pb.stream.protocol === 'hls' && !video.canPlayType('application/vnd.apple.mpegurl')) {
      throw new Error("this browser can't play HLS; load hls.js");
    } else {
      video.src = pb.stream.url;
      if (startAt) video.addEventListener('loadedmetadata', () => { video.currentTime = startAt; }, { once: true });
    }
    this.showSubtitles();
  }

  /**
   * Switches the audio track: the stream restarts at the same place. The
   * choice is kept for the title.
   */
  async setAudioTrack(id) {
    this.choice.audioTrack = id;
    const pb = await this.restart();
    if (pb) this.client.chooseTracks(pb.id, { audioTrack: id }).catch(() => {});
    return pb;
  }

  /**
   * Limits the bitrate (kbps; 0 or null: automatic, the server's limits
   * only). The stream restarts at the same place; a limit below the file's
   * means converting it.
   */
  setMaxBitrate(kbps) {
    this.choice.maxBitrateKbps = kbps || undefined;
    return this.restart();
  }

  /** The bitrate limit chosen (kbps), or 0 for automatic. */
  get maxBitrate() { return this.choice.maxBitrateKbps || 0; }

  /**
   * Shows a subtitle track, or none (null), at once: text or pictures,
   * never burned into the video. The choice is kept for the title.
   */
  setSubtitleTrack(id) {
    this.shown = id || '';
    this.showSubtitles();
    const pb = this.playback;
    if (pb) this.client.chooseTracks(pb.id, { subtitleTrack: id || '' }).catch(() => {});
    return Promise.resolve();
  }

  /** The subtitle track chosen, or null (forced subtitles aside). */
  get subtitleTrack() { return (this.shown ?? this.playback?.subtitleTrack) || null; }

  /** The forced subtitles on screen (none chosen), or null. */
  get forcedTrack() { return this.subtitleTrack ? null : this.playback?.forcedTrack || null; }

  /** The audio track playing. */
  get audioTrack() { return this.playback?.audioTrack; }

  // showSubtitles shows the subtitles chosen, else the forced ones in the
  // audio's language: text through the video's tracks, pictures on a
  // canvas over the video.
  showSubtitles() {
    const pb = this.playback;
    const id = this.subtitleTrack || this.forcedTrack;
    const track = [...(pb?.subtitleTracks || []), ...(pb?.forcedTracks || [])].find((t) => t.id === id);
    for (const el of this.video.querySelectorAll('track[data-overfin]')) {
      el.track.mode = track && !track.pictures && el.dataset.overfin === id ? 'showing' : 'disabled';
    }
    if (track?.pictures && track.url) {
      if (!this.pictures) this.pictures = new PictureSubtitles(this.video, this.pictureCanvas());
      if (this.pictures.url !== track.url) {
        this.pictures.url = track.url;
        this.pictures.load(track.url).catch(() => {}); // subtitles are a bonus: playback goes on
      }
    } else if (this.pictures) {
      this.pictures.url = '';
      this.pictures.hide();
    }
  }

  // pictureCanvas is where pictures are drawn: the one given in the
  // options, else one laid over the video, following its box.
  pictureCanvas() {
    if (this.options.pictureCanvas) return this.options.pictureCanvas;
    const video = this.video;
    const canvas = document.createElement('canvas');
    canvas.dataset.overfin = 'pictures';
    canvas.style.position = 'absolute';
    const follow = () => {
      Object.assign(canvas.style, { left: `${video.offsetLeft}px`, top: `${video.offsetTop}px`,
        width: `${video.offsetWidth}px`, height: `${video.offsetHeight}px` });
    };
    follow();
    video.after(canvas);
    this.resizing = new ResizeObserver(follow);
    this.resizing.observe(video);
    return canvas;
  }

  /** The segment (intro, credits…) at a time, if any. */
  segmentAt(ms = this.positionMs) {
    return (this.playback?.segments || []).find((s) => ms >= s.startMs && ms < s.endMs) || null;
  }

  /** Takes playback back after another device took over. */
  continueHere() {
    this.video.autoplay = true;
    return this.restart();
  }

  /** Stops for good: the server frees the stream at once. */
  async close() {
    if (this.closed) return;
    const pb = this.playback;
    const position = this.positionMs;
    this.teardown();
    this.closed = true;
    this.pictures?.hide();
    this.resizing?.disconnect();
    if (!this.options.pictureCanvas) this.video.parentElement?.querySelector('canvas[data-overfin="pictures"]')?.remove();
    this.video.removeAttribute('src');
    this.video.load();
    if (pb) await this.client.stopPlayback(pb.id, position, { keepalive: true }).catch(() => {});
  }

  // ---- Inside --------------------------------------------------------------

  restart() {
    const wasPlaying = !this.video.paused;
    const at = this.positionMs;
    this.held = at;
    const old = this.playback;
    this.teardown();
    if (old) this.client.stopPlayback(old.id, at).catch(() => {});
    this.video.autoplay = this.video.autoplay || wasPlaying;
    return this.start(at);
  }

  teardown() {
    clearInterval(this.timer);
    this.timer = null;
    for (const [event, fn] of Object.entries(this.handlers)) this.video.removeEventListener(event, fn);
    if (this.hls) { this.hls.destroy(); this.hls = null; }
  }

  report(extra = {}) {
    const pb = this.playback;
    if (!pb || this.closed) return;
    this.client.reportProgress(pb.id, { positionMs: this.positionMs, paused: this.video.paused, ...extra })
      .catch((err) => this.gone(pb, err));
  }

  // settled: the new stream has its place, so the video says it again.
  settled() {
    if (this.video.currentTime > 0) this.held = null;
  }

  async check() {
    const pb = this.playback;
    if (!pb) return;
    try { await this.client.playback(pb.id); } catch (err) { this.gone(pb, err); }
  }

  // fatal handles a stream hls.js gave up on: with the playback gone, as
  // for any report; still there, or the server out of reach, it starts over
  // at the same place, a few times a minute at most, then says so, rather
  // than stay frozen on a playback that holds the account's stream.
  async fatal(data) {
    const pb = this.playback;
    await this.check();
    if (pb !== this.playback || this.closed) return; // gone() took it from here
    const now = Date.now();
    this.fatals = (this.fatals || []).filter((at) => now - at < 60_000);
    this.fatals.push(now);
    if (this.fatals.length <= 3) {
      this.restart();
      return;
    }
    this.video.pause();
    this.options.onError?.(new Error(`the stream failed: ${data?.details || 'unknown'}`));
  }

  // gone handles a playback the server no longer has.
  gone(pb, err) {
    if (pb !== this.playback || this.closed) return;
    // Freed after a pause or a lost connection, or no longer known at all
    // (the server forgets why after an hour, and everything when it
    // restarts): carry on, unnoticed.
    if ((err.status === 410 && err.type === 'playback-expired') || err.status === 404) {
      this.video.autoplay = !this.video.paused;
      this.restart();
      return;
    }
    if (err.status !== 410) return;
    this.held = this.positionMs;
    this.teardown();
    this.playback = null;
    this.video.pause();
    if (err.type === 'playback-taken-over' && this.options.onTakenOver) {
      this.playback = pb; // continueHere() restarts from it
      this.options.onTakenOver(err.problem.takenOverBy || '');
    } else {
      this.options.onError?.(err);
    }
  }
}

// ---- Picture subtitles (PGS) ---------------------------------------------

// Subtitles that are pictures, as Blu-rays carry them: Overfin serves the
// track as the file has it (checked whole first), and the player draws it
// over the video, so the video is never converted for them. decodePGS
// reads a track into cues; each picture stays compressed until it's shown
// (a whole track as pixels would take hundreds of MB).

const PGS_PALETTE = 0x14, PGS_OBJECT = 0x15, PGS_PRESENTATION = 0x16, PGS_END = 0x80;
const PGS_MAX_SIDE = 4096;

/**
 * Reads a PGS track (the bytes Overfin serves) into cues, in order: each
 * { startMs, endMs, width, height, forced, pictures: [{ x, y, width,
 * height, crop?, palette, data }] }, width and height being the screen's.
 * The last cue ends at Infinity. Throws on a malformed track.
 */
export function decodePGS(bytes) {
  const b = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
  const u16 = (i) => (b[i] << 8) | b[i + 1];
  const bad = () => { throw new Error('not a well-formed PGS track'); };
  const cues = [];
  // What an epoch keeps from one display set to the next: palettes and
  // pictures by id. A picture is whole once its last fragment came.
  let palettes = new Map(), objects = new Map();
  let shown = null; // the display set being read: its presentation
  for (let pos = 0; pos < b.length;) {
    if (pos + 13 > b.length || b[pos] !== 0x50 || b[pos + 1] !== 0x47) bad();
    const ms = Math.round(((b[pos + 2] * 2 ** 24) + (b[pos + 3] << 16 | b[pos + 4] << 8 | b[pos + 5])) / 90);
    const type = b[pos + 10];
    const size = u16(pos + 11);
    const s = pos + 13;
    pos = s + size;
    if (pos > b.length) bad();
    switch (type) {
      case PGS_PRESENTATION: {
        if (size < 11) bad();
        // Width, height, rate, number, state (0x80: a new epoch), palette
        // update, palette, then each picture shown.
        const screenW = u16(s), screenH = u16(s + 2);
        if (!screenW || !screenH || screenW > PGS_MAX_SIDE || screenH > PGS_MAX_SIDE) bad();
        if (b[s + 7] & 0x80) { palettes = new Map(); objects = new Map(); }
        const show = { ms, width: u16(s), height: u16(s + 2), palette: b[s + 9], forced: false, refs: [] };
        let o = s + 11;
        for (let n = b[s + 10]; n > 0; n--) {
          if (o + 8 > pos) bad();
          const ref = { id: u16(o), x: u16(o + 4), y: u16(o + 6) };
          if (b[o + 3] & 0x40) show.forced = true;
          if (b[o + 3] & 0x80) {
            if (o + 16 > pos) bad();
            ref.crop = { x: u16(o + 8), y: u16(o + 10), width: u16(o + 12), height: u16(o + 14) };
            o += 8;
          }
          show.refs.push(ref);
          o += 8;
        }
        shown = show;
        break;
      }
      case PGS_PALETTE: {
        if (size < 2 || (size - 2) % 5) bad();
        // Entries: index, Y, Cr, Cb, alpha. Unset entries are clear.
        const id = b[s];
        const rgba = new Uint8ClampedArray((palettes.get(id) || new Uint8ClampedArray(1024)));
        for (let e = s + 2; e < pos; e += 5) {
          yuvToRGBA(b[e + 1], b[e + 3], b[e + 2], b[e + 4], rgba, b[e] * 4, shown ? shown.height > 576 : true);
        }
        palettes.set(id, rgba);
        break;
      }
      case PGS_OBJECT: {
        if (size < 4) bad();
        const id = u16(s), flags = b[s + 3];
        if (flags & 0x80) { // its first fragment: its size, then data
          if (size < 11) bad();
          const width = u16(s + 7), height = u16(s + 9);
          if (!width || !height || width > PGS_MAX_SIDE || height > PGS_MAX_SIDE) bad();
          objects.set(id, { width, height, parts: [b.subarray(s + 11, pos)], data: null });
        } else {
          const obj = objects.get(id);
          if (!obj || obj.data) bad();
          obj.parts.push(b.subarray(s + 4, pos));
        }
        if (flags & 0x40) { // its last
          const obj = objects.get(id);
          if (!obj) bad();
          obj.data = concat(obj.parts);
          obj.parts = null;
        }
        break;
      }
      case PGS_END: {
        if (!shown) break;
        const prev = cues[cues.length - 1];
        if (prev && prev.endMs === Infinity) prev.endMs = shown.ms;
        const palette = palettes.get(shown.palette);
        const pictures = [];
        for (const ref of shown.refs) {
          const obj = objects.get(ref.id);
          if (!obj?.data || !palette) continue;
          pictures.push({ x: ref.x, y: ref.y, width: obj.width, height: obj.height, crop: ref.crop, palette, data: obj.data });
        }
        if (pictures.length) {
          cues.push({ startMs: shown.ms, endMs: Infinity, width: shown.width, height: shown.height, forced: shown.forced, pictures });
        }
        shown = null;
        break;
      }
      case 0x17: break; // windows: where pictures may go; their own places say it
      default: bad();
    }
  }
  return cues;
}

/**
 * A picture's pixels, RGBA, from its run-length data: width × height × 4
 * bytes, ready for an ImageData. Throws when the data overflows its size.
 */
export function pgsPixels(picture) {
  const { width, height, data, palette } = picture;
  const out = new Uint8ClampedArray(width * height * 4);
  const bad = () => { throw new Error('PGS picture overflows its size'); };
  let x = 0, y = 0;
  const put = (n, color) => {
    if (x + n > width || y >= height) bad();
    const c = color * 4;
    if (palette[c + 3]) { // clear pixels stay as they are: zero
      for (let p = (y * width + x) * 4, end = p + n * 4; p < end; p += 4) {
        out[p] = palette[c]; out[p + 1] = palette[c + 1]; out[p + 2] = palette[c + 2]; out[p + 3] = palette[c + 3];
      }
    }
    x += n;
  };
  for (let i = 0; i < data.length;) {
    const v = data[i++];
    if (v) { put(1, v); continue; }
    if (i >= data.length) bad();
    const f = data[i++];
    if (!f) { x = 0; y++; continue; } // end of a line
    let n = f & 0x3f;
    if (f & 0x40) { if (i >= data.length) bad(); n = (n << 8) | data[i++]; }
    let color = 0;
    if (f & 0x80) { if (i >= data.length) bad(); color = data[i++]; }
    put(n, color);
  }
  return out;
}

/**
 * Draws a PGS track over a video, on a canvas laid over it (the same box;
 * the SDK sets its object-fit so the screen matches the picture's): the
 * cue of the moment, drawn when it changes, cleared between cues.
 */
export class PictureSubtitles {
  constructor(video, canvas) {
    this.video = video;
    this.canvas = canvas;
    canvas.style.objectFit = 'contain';
    canvas.style.pointerEvents = 'none';
    this.cues = [];
    this.current = null;
    this.frame = 0;
    this.loads = 0;
  }

  /** Loads a track (its URL); resolves once its cues are read. */
  async load(url) {
    this.hide();
    const run = ++this.loads;
    const res = await fetch(url);
    if (!res.ok) throw new Error(`picture subtitles: HTTP ${res.status}`);
    const cues = decodePGS(new Uint8Array(await res.arrayBuffer()));
    if (run !== this.loads) return;
    this.cues = cues;
    this.tick();
  }

  /** Stops drawing and clears the canvas; load() starts again. */
  hide() {
    this.loads++;
    cancelAnimationFrame(this.frame);
    this.frame = 0;
    this.cues = [];
    this.draw(null);
  }

  // tick draws the cue of the moment, every frame the page shows.
  tick() {
    this.draw(cueAt(this.cues, this.video.currentTime * 1000));
    this.frame = requestAnimationFrame(() => this.tick());
  }

  draw(cue) {
    if (cue === this.current) return;
    this.current = cue;
    const ctx = this.canvas.getContext('2d');
    if (!cue) { ctx.clearRect(0, 0, this.canvas.width, this.canvas.height); return; }
    if (this.canvas.width !== cue.width || this.canvas.height !== cue.height) {
      this.canvas.width = cue.width;
      this.canvas.height = cue.height;
    } else {
      ctx.clearRect(0, 0, cue.width, cue.height);
    }
    for (const p of cue.pictures) {
      let image;
      try { image = new ImageData(pgsPixels(p), p.width, p.height); } catch { continue; }
      const c = p.crop || { x: 0, y: 0, width: p.width, height: p.height };
      ctx.putImageData(image, p.x - c.x, p.y - c.y, c.x, c.y, c.width, c.height);
    }
  }
}

/** The cue showing at a time (ms), or null. */
export function cueAt(cues, ms) {
  let lo = 0, hi = cues.length - 1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (cues[mid].startMs > ms) hi = mid - 1;
    else if (cues[mid].endMs <= ms) lo = mid + 1;
    else return cues[mid];
  }
  return null;
}

// yuvToRGBA writes a palette entry: studio-range Y'CbCr to RGB, by BT.709
// for HD screens (taller than 576), else BT.601.
function yuvToRGBA(Y, Cb, Cr, A, out, at, hd) {
  const y = (Y - 16) * 255 / 219, cb = (Cb - 128) * 255 / 224, cr = (Cr - 128) * 255 / 224;
  const [kr, kgb, kgr, kb] = hd ? [1.5748, 0.1873, 0.4681, 1.8556] : [1.402, 0.3441, 0.7141, 1.772];
  out[at] = y + kr * cr;
  out[at + 1] = y - kgb * cb - kgr * cr;
  out[at + 2] = y + kb * cb;
  out[at + 3] = A;
}

function concat(parts) {
  if (parts.length === 1) return parts[0];
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let at = 0;
  for (const p of parts) { out.set(p, at); at += p.length; }
  return out;
}

// ---- Helpers ---------------------------------------------------------------

const enc = encodeURIComponent;

function toQuery(query) {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v === undefined || v === null || v === '') continue;
    for (const one of Array.isArray(v) ? v : [v]) q.append(k, String(one));
  }
  const s = q.toString();
  return s ? '?' + s : '';
}

function clean(obj) {
  return Object.fromEntries(Object.entries(obj).filter(([, v]) => v !== undefined && v !== null));
}
