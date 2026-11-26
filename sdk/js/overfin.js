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
    // Only to a page of this site: a step-up never leaves it.
    const to = new URL(url, location.origin);
    if (to.origin !== location.origin) throw new Error('this server offers no step-up');
    to.searchParams.set('return', returnTo);
    location.href = to.href;
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
  /** A page of them, for the whole list: { items, nextCursor? }, nextCursor saying whether there are more (since 0.10). */
  recommendedPage(scope = 'all', { limit, cursor } = {}) { return this.request('GET', `/titles/recommended${toQuery({ scope, limit, cursor })}`); }
  /** Titles like this one: those the server has first (since 0.6). */
  async similar(id, limit = 20) { return (await this.request('GET', `/titles/${enc(id)}/similar${toQuery({ limit })}`)).items; }
  /** The collections of films (TMDB's series of films) the profile has films of, without their films (since 0.7). */
  async collections() { return (await this.request('GET', '/collections')).items; }
  /** A collection with its films, in release order (since 0.7). */
  collection(id) { return this.request('GET', `/collections/${enc(id)}`); }
  /**
   * Searches titles (in a library or not) and people; the terms travel in
   * the body. A title's ID or a link to it (imdb:tt0133093, tt0133093,
   * tmdb:movie/603, tmdb:tv/1399, tvdb:81189, an imdb.com, themoviedb.org
   * or thetvdb.com page) finds that title instead, with byId (since 0.10).
   */
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
  async leaving() { return (await this.request('GET', '/titles/leaving')).items; }
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

  /** Says the profile isn't interested in a film or a series (its
   * recommendations leave it out), or takes that back (since 0.9). */
  setNotInterested(id, notInterested = true) { return this.request(notInterested ? 'PUT' : 'DELETE', `/titles/${enc(id)}/not-interested`); }
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
   * 'missing' (the default since 0.10: what neither this server nor a
   * paired one has yet, Discovery's), 'library' (what they have) or 'all'.
   * A page is filled as the list allows; nextCursor says whether there are
   * more.
   */
  discover(list, mediaKind, { availability, limit, cursor } = {}) {
    return this.request('GET', `/discover${toQuery({ list, mediaKind, availability, limit, cursor })}`);
  }

  /** The address of an image reference, in a size: 'small', 'medium' or 'large'. */
  imageUrl(ref, size = 'medium') { return ref ? `${ref.url}?size=${size}` : ''; }

  // ---- Playback ----------------------------------------------------------

  /**
   * Starts a playback: MSFP's play request (playRequest() builds it from
   * chooseDelivery()'s decision). Most clients want play() instead, which
   * does the rest. A body of the older API (capabilities, before 0.10) is
   * answered 401 not-authenticated, which sends a page still on an older
   * SDK through sign-in and so loads it again.
   */
  startPlayback(request) { return this.request('POST', '/playback', request); }

  /**
   * A copy's seek-bar previews before it plays: { ticket, etag }, the
   * sheets at streamUrls(base, { ticket }).sheet(n) (previewSheets() with
   * the copy's previewLayout), for ten minutes (since 0.10).
   */
  previews(titleId, versionId) { return this.request('POST', '/previews', { titleId, versionId }); }

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

// appHdr is what Overfin's Android app says the screen shows in HDR
// (OverfinApp.hdr(), JSON names), or null outside the app.
function appHdr() {
  try {
    const names = JSON.parse(globalThis.OverfinApp?.hdr?.() ?? 'null');
    return Array.isArray(names) ? names.filter((n) => n === 'hdr10' || n === 'hlg') : null;
  } catch {
    return null;
  }
}

// ---- Choosing a copy and how it plays -------------------------------------
//
// A title's page gives its copies as MSFP's copy objects: facts only (MSFP
// video, section 3). The player chooses among them for this device: the
// copy, its audio track, and the cheapest delivery the device plays
// (Overfin's plan, section 7.1): the file itself, a remux (the streams as
// they are, in segments), a transcode (the audio alone converted, or the
// picture too), or, when in doubt, MSFP's baseline (H.264, AAC stereo).
// The server makes exactly what is asked, or refuses it.

/** MSFP's media types, as browsers name them; any other: not played as it is. */
export const BROWSER_TYPES = {
  'video/mp4': 'video/mp4',
  'video/quicktime': 'video/quicktime',
  'video/webm': 'video/webm',
  'video/matroska': 'video/x-matroska',
  'video/mp2t': 'video/mp2t',
};

// The video codecs a browser may decode as they are (registry tokens), and
// the audio ones; any other is converted.
const VIDEO_CODECS = ['h264', 'hevc', 'av1', 'vp9', 'vp8'];
const AUDIO_CODECS = ['aac', 'mp3', 'ac3', 'eac3', 'opus', 'flac', 'vorbis', 'alac'];
// The codecs string of an audio codec whose copy may leave it out (MSFP
// video, section 3.5: the registry's fixed codes).
const FIXED_AUDIO = { ac3: 'ac-3', eac3: 'ec-3', flac: 'fLaC', opus: 'Opus', alac: 'alac', mp3: 'mp4a.40.34', vorbis: 'vorbis' };
// What segments carry as they are (MSFP's default segment formats, msfp-go
// wire.DefaultSegments): fragmented MP4, and MPEG-TS.
const MP4_CARRIES = ['h264', 'hevc', 'av1', 'vp9', 'aac', 'ac3', 'eac3', 'opus', 'flac', 'alac', 'mp3'];
const TS_CARRIES = ['h264', 'hevc', 'aac', 'ac3', 'eac3', 'mp3'];
// Dolby Vision's sample entries, from the base codec's (registry, section 1).
const DV_ENTRIES = { hvc1: 'dvh1', hev1: 'dvhe', av01: 'dav1', avc1: 'dva1', avc3: 'dvav' };

/**
 * The deliveries, cheapest first: each play starts at the first that fits
 * the copy and the device, and the fallback ladder steps down from there.
 */
export const LEVELS = ['file', 'remux', 'audio', 'video', 'baseline'];

/**
 * The reasons a player gives for the delivery it chose (the play request's
 * `reason`, shown in the control panel's "Now playing"): a closed list.
 */
export const REASONS = ['as-is', 'container', 'audio-track', 'audio-codec', 'video-codec', 'picture-range', 'bitrate',
  'fallback', 'quirk', 'in-doubt'];

/**
 * Devices that play otherwise than they say. Each entry: the device test,
 * what it overrides, the date it was added and how it was found. New
 * entries only after a failure seen on a real device.
 *
 * Overrides: noFile (media types never played as a file), hdrMapped (the
 * browser maps HDR onto any screen itself), hdrFromApp (HDR is what
 * Overfin's app says the screen shows), audioNever (audio codecs never
 * played as they are; a name ending in _ covers its family).
 */
export const QUIRKS = [
  {
    id: 'firefox-matroska',
    test: (env) => env.family.firefox,
    override: { noFile: ['video/matroska'] },
    date: '2026-11-22',
    found: 'Firefox says it plays Matroska, then reads the whole file before the first picture (the site\'s player, before 0.10)',
  },
  {
    id: 'browser-maps-hdr',
    test: (env) => !env.app && ((env.family.chromium && !env.family.mobile) || (env.family.safari && env.family.mac) || (env.family.firefox && env.family.mac)),
    override: { hdrMapped: true },
    date: '2026-11-22',
    found: 'desktop Chromium browsers and Apple\'s Safari (Firefox on macOS only) map HDR video onto any screen themselves (the site\'s player, before 0.10); WebKit on Linux doesn\'t (its colours washed out in a test, 2026-11-22)',
  },
  {
    id: 'app-hdr',
    test: (env) => !!env.app,
    override: { hdrFromApp: true },
    date: '2026-11-22',
    found: 'Android\'s WebView doesn\'t tell pages what the screen shows; Overfin\'s app does (OverfinApp.hdr(): hdr10, hlg; never Dolby Vision)',
  },
  {
    id: 'lossless-audio',
    test: () => true,
    override: { audioNever: ['truehd', 'mlp', 'dts', 'dtsuhd', 'pcm_'] },
    date: '2026-11-22',
    found: 'no browser plays TrueHD, DTS or PCM in a video, whatever it says (Overfin\'s plan, section 7.1)',
  },
  {
    id: 'webos-browser',
    test: (env) => env.family.webos,
    override: {},
    date: '2026-11-22',
    found: 'LG webOS\'s browser: to measure on a real TV; nothing overridden until a failure is seen',
  },
];

/**
 * What this device plays, read once per page: the browser's own answers
 * (MediaSource.isTypeSupported or ManagedMediaSource's, canPlayType,
 * mediaCapabilities.decodingInfo), the screen's range (the
 * dynamic-range media query, or Overfin's app), whether HLS plays (hls.js,
 * or natively), whether a file's audio tracks can be switched
 * (audioTracks), and the quirks that apply.
 * @param {{ Hls?: any }} [options] the hls.js constructor (globalThis.Hls by default)
 */
export function deviceEnv({ Hls } = {}) {
  const g = globalThis;
  let video = null;
  try { video = g.document?.createElement?.('video') || null; } catch { video = null; }
  const MSE = g.ManagedMediaSource || g.MediaSource;
  const caps = g.navigator?.mediaCapabilities;
  const hlsjs = Hls || g.Hls;
  const decoded = new Map();
  let hdrScreen = false;
  try { hdrScreen = !!g.matchMedia?.('(dynamic-range: high)')?.matches; } catch { hdrScreen = false; }
  const canPlay = (type) => {
    try { return video?.canPlayType?.(type) || ''; } catch { return ''; }
  };
  let hls = '';
  try {
    if (hlsjs?.isSupported?.()) hls = 'hls.js';
    else if (canPlay('application/vnd.apple.mpegurl')) hls = 'native';
  } catch { hls = ''; }
  const env = {
    family: familyOf(g.navigator?.userAgent || ''),
    app: appHdr(),
    hdrScreen,
    hls,
    audioTracks: !!video && 'audioTracks' in video,
    canPlay,
    mse: (type) => {
      try { return !!MSE?.isTypeSupported?.(type); } catch { return false; }
    },
    // decodingInfo's answer ({ supported, smooth, powerEfficient }), or
    // null when the browser can't say: each configuration asked once.
    decode: (config) => {
      const key = JSON.stringify(config);
      if (!decoded.has(key)) {
        let answer;
        try {
          answer = caps?.decodingInfo ? Promise.resolve(caps.decodingInfo(config)).then((r) => r || null, () => null) : Promise.resolve(null);
        } catch {
          answer = Promise.resolve(null);
        }
        decoded.set(key, answer);
      }
      return decoded.get(key);
    },
    quirks: [],
    rules: { noFile: [], audioNever: [], hdrMapped: false, hdrFromApp: false },
  };
  for (const q of QUIRKS) {
    let applies = false;
    try { applies = !!q.test(env); } catch { applies = false; }
    if (!applies) continue;
    env.quirks.push(q.id);
    const o = q.override || {};
    env.rules.noFile.push(...(o.noFile || []));
    env.rules.audioNever.push(...(o.audioNever || []));
    env.rules.hdrMapped ||= !!o.hdrMapped;
    env.rules.hdrFromApp ||= !!o.hdrFromApp;
  }
  return env;
}

// familyOf tells browsers apart where what they say isn't enough.
function familyOf(ua) {
  const firefox = /Firefox\//.test(ua);
  const chromish = /Chrom(e|ium)\//.test(ua);
  const mobile = /Mobile|Android/.test(ua);
  return {
    firefox,
    chromium: !firefox && chromish,
    safari: !firefox && !chromish && /Safari\//.test(ua),
    mac: /Mac OS X|Macintosh/.test(ua),
    android: /Android/.test(ua),
    webos: /Web0S|webOS/.test(ua),
    mobile,
  };
}

/**
 * Chooses the copy of a title, its audio track and its delivery for this
 * device (Overfin's plan, section 7.1). copies: the title page's copies;
 * env: deviceEnv(); wishes: the title page's wrapper (limits, encoders,
 * wishes.audio as `audio`), the app's `language`, and the player's own
 * choices (versionId, audioTrack, audioLanguage, maxBitrateKbps: the
 * quality chosen), `peers` (IDs of copies on paired servers), `minLevel`
 * (a level of LEVELS to start from at least), `memory` (the fallback
 * memory; fallbacks by default; null for none).
 *
 * Resolves with the decision, or null when there is no copy: { copy,
 * audioTrack, delivery ('file', 'remux', 'transcode' or '' for MSFP's
 * baseline), mediaType, transcode, reason, level, converts: { video,
 * audio }, asIs (nothing converted), quirks (those that applied) }.
 */
export async function chooseDelivery(copies, env, wishes = {}) {
  let list = (copies || []).filter((c) => c && typeof c.id === 'string');
  if (wishes.versionId) {
    const asked = list.filter((c) => c.id === wishes.versionId);
    if (asked.length) list = asked;
  }
  if (!list.length) return null;
  const memory = wishes.memory === undefined ? fallbacks : wishes.memory;
  const peers = new Set(wishes.peers || []);
  const plans = [];
  for (const [order, copy] of list.entries()) {
    const audio = chooseAudio(copy, env, wishes);
    const remembered = memory ? memory.level(copy, audio.id) : 0;
    const plan = await planCopy(copy, audio.id, env, wishes, Math.max(remembered, wishes.minLevel || 0));
    // Started lower than it would: the ladder stepped down before.
    if (plan.level > plan.natural) plan.reason = 'fallback';
    plans.push({ ...plan, matched: audio.matched, peer: peers.has(copy.id), order, picture: pictureScore(copy, env) });
  }
  // D11: own copies that play as they are, the best picture first; else a
  // paired server's that does; else own copies first, converted. Within
  // each, the one with the audio wished for first.
  const tier = (p) => (p.asIs ? (p.peer ? 1 : 0) : (p.peer ? 3 : 2));
  plans.sort((a, b) => tier(a) - tier(b) || Number(b.matched) - Number(a.matched)
    || (a.asIs ? b.picture - a.picture : 0) || a.order - b.order);
  const { matched, peer, order, picture, natural, ...decision } = plans[0];
  return { ...decision, quirks: [...env.quirks] };
}

// chooseAudio is the audio track a play of copy asks for: the player's
// choice, else the language wished (the player's, the title's or the
// profile's, then the app's), else the original, else the file's default.
// matched: a wished language was found.
function chooseAudio(copy, env, wishes) {
  const tracks = copy.audioTracks || [];
  if (wishes.audioTrack && tracks.some((t) => t.id === wishes.audioTrack)) return { id: wishes.audioTrack, matched: true };
  const menu = audioMenu(copy, env);
  for (const language of [wishes.audioLanguage, wishes.audio, wishes.language]) {
    const t = pickLanguage(menu.filter((m) => roleClass(m.role) === 'main'), language);
    if (t) return { id: t.id, matched: true };
  }
  const original = menu.find((t) => t.original && roleClass(t.role) === 'main');
  if (original) return { id: original.id, matched: false };
  const d = defaultAudio(copy);
  return { id: d?.id || '', matched: false };
}

// defaultAudio is the audio track the file plays by itself: the
// default-marked one, else the first (MSFP's DefaultAudio).
function defaultAudio(copy) {
  const tracks = copy.audioTracks || [];
  return tracks.find((t) => t.default) || tracks[0] || null;
}

// pickLanguage is the track in a language (a tag): the same variant, else
// the same language from anywhere.
function pickLanguage(tracks, language) {
  if (!language) return null;
  const want = String(language).toLowerCase();
  const base = want.split('-')[0];
  return tracks.find((t) => (t.language || '').toLowerCase() === want)
    || tracks.find((t) => t.language && t.language.toLowerCase().split('-')[0] === base) || null;
}

// roleClass groups audio and subtitle roles: the film's own sound (or
// text), a commentary, a description, anything else.
function roleClass(role) {
  if (!role || ['main', 'dub', 'alternate', 'supplementary', 'subtitle'].includes(role)) return 'main';
  return role;
}

// planCopy is the cheapest delivery of copy, its audio track audioId, on
// the device, from level from (of LEVELS) on. natural: the level it would
// start at with nothing remembered.
async function planCopy(copy, audioId, env, wishes, from) {
  const track = (copy.audioTracks || []).find((t) => t.id === audioId) || null;
  const limits = wishes.limits || {};
  const encoders = wishes.encoders || null;
  const cap = Math.min(...[limits.maxBitrateKbps, wishes.maxBitrateKbps].filter((k) => k > 0), Infinity);
  const kbps = copy.durationMs > 0 ? (copy.size * 8) / copy.durationMs : 0;
  const overCap = Number.isFinite(cap) && kbps > cap;
  // An HDR picture the device decodes but doesn't show, which the server
  // can't make SDR (no tone mapping): played as it is all the same.
  const washed = (v) => (!v.ok && v.reason === 'picture-range' && v.decodes && !encoders?.toneMapping
    ? { ...v, ok: true, reason: '' } : v);
  const seg = washed(await videoPlays(copy, env, 'segments'));
  const fileVideo = washed(await videoPlays(copy, env, 'file'));
  const a = audioPlays(track, env);
  const out = (level, delivery, extra, reason) => ({ copy, audioTrack: audioId, delivery, level, reason, ...extra });
  const candidates = [];

  // The file itself.
  const isDefault = !track || track.id === defaultAudio(copy)?.id;
  let fileReason = '';
  if (limits.originalFile === false) fileReason = 'container';
  else if (copy.parts > 1 || !copy.video) fileReason = 'in-doubt';
  else if (overCap) fileReason = 'bitrate';
  else if (!fileVideo.ok) fileReason = fileVideo.reason;
  else if (!a.ok) fileReason = a.reason;
  else if (env.rules.noFile.includes(copy.mediaType)) fileReason = 'quirk';
  else if (!containerPlays(copy, fileVideo, a, env)) fileReason = 'container';
  else if (!isDefault && !env.audioTracks) fileReason = 'audio-track';
  if (!fileReason) candidates.push(out(0, 'file', { converts: { video: false, audio: false }, asIs: true }, 'as-is'));

  // A remux: the streams as they are, in segments.
  const mediaType = !overCap && seg.ok && a.ok ? remuxType(copy, seg, a, track, env) : '';
  const remuxReason = ['audio-track', 'quirk'].includes(fileReason) ? fileReason : 'container';
  if (mediaType) candidates.push(out(1, 'remux', { mediaType, converts: { video: false, audio: false }, asIs: true }, remuxReason));

  // The audio alone converted (AAC stereo), the picture as it is.
  const copiedVideo = seg.ok && !overCap && (videoSegments(copy, seg, env) !== '');
  if (copiedVideo && copy.video) {
    candidates.push(out(2, 'transcode', { transcode: { videoCodec: 'copy', audioCodec: 'aac' }, converts: { video: false, audio: !!track }, asIs: false },
      a.ok ? remuxReason : a.reason));
  }

  // The picture converted.
  if (copy.video && encoders) {
    const t = convertedVideo(copy, env, encoders, cap);
    if (t) {
      const audioCopy = a.ok && track && ['aac', 'mp3'].includes(track.codec) && (t.videoCodec === 'h264' || MP4_CARRIES.includes(track.codec));
      t.audioCodec = audioCopy ? 'copy' : 'aac';
      const why = overCap ? 'bitrate' : seg.reason && seg.reason !== 'in-doubt' ? seg.reason : 'video-codec';
      candidates.push(out(3, 'transcode', { transcode: t, converts: { video: true, audio: !audioCopy && !!track }, asIs: false }, why));
    }
  }

  // MSFP's baseline: the server converts what isn't within it.
  candidates.push(out(4, '', { converts: { video: !!copy.video, audio: !!track }, asIs: false }, 'in-doubt'));

  const natural = candidates[0].level;
  const chosen = candidates.find((c) => c.level >= from) || candidates[candidates.length - 1];
  return { ...chosen, natural };
}

// rangeOf is a picture's dynamic range: sdr, hdr10 (PQ), hlg, or dv
// (Dolby Vision without a base layer of another range); unknown when it
// says nothing (no colour: taken as SDR, as MSFP says nothing of it).
function rangeOf(v) {
  if (v.dv && v.dv.compatibility === 0) return 'dv';
  if (v.dv) return { 1: 'hdr10', 6: 'hdr10', 4: 'hlg', 2: 'sdr' }[v.dv.compatibility] || 'dv';
  const tr = v.colour?.transfer;
  return tr === 16 ? 'hdr10' : tr === 18 ? 'hlg' : 'sdr';
}

// rangeShown says whether the device shows a range (or maps it onto its
// screen itself).
function rangeShown(range, env) {
  if (range === 'sdr') return true;
  if (env.rules.hdrFromApp) return range !== 'dv' && (env.app || []).includes(range);
  return env.hdrScreen || env.rules.hdrMapped;
}

// outOfBand is a codecs string as segments carry it: parameter sets out of
// band (hev1 → hvc1, avc3 → avc1), as the server's master playlist says.
function outOfBand(codecs) {
  const [four, ...rest] = codecs.split('.');
  const to = { hev1: 'hvc1', avc3: 'avc1' }[four];
  return to ? [to, ...rest].join('.') : codecs;
}

/**
 * The Dolby Vision codecs string of a picture (MSFP video, section 3.5):
 * its base codecs string's 4CC turned into the Dolby Vision entry, with
 * the profile and level of `dv` ("dvh1.08.06"); '' without dv.
 */
export function dolbyVisionCodecs(codecs, dv) {
  if (!dv || !codecs) return '';
  const entry = DV_ENTRIES[codecs.split('.')[0]];
  if (!entry) return '';
  const two = (n) => String(n).padStart(2, '0');
  return `${entry}.${two(dv.profile)}.${two(dv.level)}`;
}

// videoPlays says whether the device decodes and shows a copy's picture as
// it is, in its file (form 'file') or in segments: { ok, reason, codecs,
// range, dv }.
async function videoPlays(copy, env, form) {
  const v = copy.video;
  if (!v) return { ok: false, reason: 'in-doubt' };
  if (!VIDEO_CODECS.includes(v.codec)) return { ok: false, reason: 'video-codec' };
  if (!v.codecs) return { ok: false, reason: 'in-doubt' };
  const [num, den] = String(v.fps || '').split('/').map(Number);
  const fps = num > 0 ? num / (den > 0 ? den : 1) : 0;
  if (copy.threeD || v.interlaced || (v.bitDepth || 8) > 10 || v.width > 4096 || v.height > 2304 || fps > 61
    || (v.codec === 'h264' && v.refFrames > 16)) {
    return { ok: false, reason: 'video-codec' };
  }
  const codecs = form === 'file' ? v.codecs : outOfBand(v.codecs);
  let range = rangeOf(v);
  // Dolby Vision: played as such only where the browser takes its string;
  // elsewhere its base layer, when it has one of another range.
  const dvString = dolbyVisionCodecs(codecs, v.dv);
  const dv = !!dvString && env.mse(`video/mp4; codecs="${dvString}"`) && (env.hdrScreen || (env.rules.hdrMapped && !env.rules.hdrFromApp));
  if (v.dv && !dv) {
    if (range === 'dv') return { ok: false, reason: 'picture-range' };
    // A file keeps its Dolby Vision entry: only segments carry the base.
    if (form === 'file') return { ok: false, reason: 'container' };
  }
  const tested = dv ? dvString : codecs;
  const type = `video/mp4; codecs="${tested}"`;
  if (!env.mse(type) && env.canPlay(type) !== 'probably') return { ok: false, reason: 'video-codec' };
  if (dv) range = 'dv';
  if (range !== 'sdr') {
    if (!rangeShown(range, env)) return { ok: false, reason: 'picture-range', codecs: tested, range, decodes: true };
    const kbps = copy.durationMs > 0 ? Math.round((copy.size * 8) / copy.durationMs) : 0;
    const config = {
      type: form === 'file' ? 'file' : 'media-source',
      video: { contentType: type, width: v.width, height: v.height, bitrate: (kbps || 20000) * 1000, framerate: fps || 24,
        transferFunction: range === 'hlg' ? 'hlg' : 'pq', colorGamut: 'rec2020' },
    };
    if (range === 'hdr10') config.video.hdrMetadataType = (v.hdr || []).includes('hdr10plus') ? 'smpteSt2094-40' : 'smpteSt2086';
    let info = await env.decode(config);
    // An HDR10+ picture is HDR10 too: a device that refuses its dynamic
    // metadata shows it as HDR10 (Chromium refuses smpteSt2094-40).
    if (info && info.supported === false && config.video.hdrMetadataType === 'smpteSt2094-40') {
      info = await env.decode({ ...config, video: { ...config.video, hdrMetadataType: 'smpteSt2086' } });
    }
    if (info && info.supported === false) return { ok: false, reason: 'picture-range', codecs: tested, range, decodes: true };
  }
  return { ok: true, reason: '', codecs: tested, range, dv };
}

// audioPlays says whether the device decodes an audio track as it is:
// { ok, reason, codecs }. No track: nothing to decode.
function audioPlays(track, env) {
  if (!track) return { ok: true, reason: '', codecs: '' };
  const c = track.codec || '';
  if (env.rules.audioNever.some((n) => (n.endsWith('_') ? c.startsWith(n) : c === n))) return { ok: false, reason: 'audio-codec' };
  if (!AUDIO_CODECS.includes(c)) return { ok: false, reason: 'audio-codec' };
  const codecs = track.codecs || FIXED_AUDIO[c];
  if (!codecs) return { ok: false, reason: 'in-doubt' };
  const type = `audio/mp4; codecs="${codecs}"`;
  if (env.mse(type) || env.canPlay(type) === 'probably') return { ok: true, reason: '', codecs };
  // Vorbis and Opus also in WebM.
  if ((c === 'vorbis' || c === 'opus') && (env.mse(`audio/webm; codecs="${c}"`) || env.canPlay(`audio/webm; codecs="${c}"`) === 'probably')) {
    return { ok: true, reason: '', codecs: c, webmOnly: true };
  }
  return { ok: false, reason: 'audio-codec' };
}

// containerPlays says whether the browser plays the copy's file with its
// streams: its media type with their codecs strings.
function containerPlays(copy, video, audio, env) {
  const type = BROWSER_TYPES[copy.mediaType];
  if (!type) return false;
  // WebM names Opus and Vorbis in lower case (Firefox takes no other).
  const a = type === 'video/webm' && /^(opus|vorbis)$/i.test(audio.codecs) ? audio.codecs.toLowerCase() : audio.codecs;
  const list = [video.codecs, a].filter(Boolean).join(',');
  if (env.canPlay(`${type}; codecs="${list}"`)) return true;
  // Matroska's own name, for browsers that know only it.
  return type === 'video/x-matroska' && !!env.canPlay(`video/matroska; codecs="${list}"`);
}

// remuxType is the segments a remux of the copy is asked in: fragmented MP4
// where the device plays its streams so, else MPEG-TS where HLS plays
// natively; '' when neither.
function remuxType(copy, video, audio, track, env) {
  if (!env.hls || audio.webmOnly) return '';
  const codecs = [video.codecs, audio.codecs].filter(Boolean).join(',');
  const streams = [copy.video?.codec, track?.codec].filter(Boolean);
  if (streams.every((c) => MP4_CARRIES.includes(c)) && (env.mse(`video/mp4; codecs="${codecs}"`)
    || (env.hls === 'native' && env.canPlay(`video/mp4; codecs="${codecs}"`) === 'probably'))) return 'video/mp4';
  if (env.hls === 'native' && streams.every((c) => TS_CARRIES.includes(c))) return 'video/mp2t';
  return '';
}

// videoSegments is the segment format a copied picture travels in ('' when
// none the device plays): TS for H.264 (hls.js repackages it), else MP4.
function videoSegments(copy, video, env) {
  if (!env.hls) return '';
  if (copy.video.codec === 'h264') return 'ts';
  return MP4_CARRIES.includes(copy.video.codec) && (env.mse(`video/mp4; codecs="${video.codecs}"`) || env.hls === 'native') ? 'mp4' : '';
}

// convertedVideo is a transcode's targets when the picture is converted:
// HDR kept (range keep, HEVC or AV1, when the server makes them and the
// device shows them), else H.264, made SDR from an HDR picture when the
// server tone maps; null when the server can't make it.
function convertedVideo(copy, env, encoders, cap) {
  const range = rangeOf(copy.video);
  const t = {};
  if (Number.isFinite(cap)) t.maxBitrateKbps = Math.max(1000, Math.floor(cap / 1000) * 1000);
  if (range !== 'sdr') {
    // Dolby Vision without a base layer is made SDR (its colours need its
    // own metadata, which a re-encoding drops).
    const keep = range !== 'dv' && (encoders.ranges || []).includes('keep') && rangeShown(range, env);
    const tryCodec = (codec, codecs) => (encoders.video || []).includes(codec) && env.mse(`video/mp4; codecs="${codecs}"`);
    if (keep && tryCodec('hevc', 'hvc1.2.4.L150.90')) return { videoCodec: 'hevc', range: 'keep', ...t };
    if (keep && tryCodec('av1', 'av01.0.08M.10')) return { videoCodec: 'av1', range: 'keep', ...t };
    if (!encoders.toneMapping) return null;
    return { videoCodec: 'h264', range: 'sdr', ...t };
  }
  return { videoCodec: 'h264', ...t };
}

// pictureScore orders copies by picture: lines, then the range shown, then
// the rate.
function pictureScore(copy, env) {
  const v = copy.video;
  if (!v) return 0;
  const range = rangeOf(v);
  const shown = rangeShown(range, env);
  const rangeRank = !shown ? 0 : { sdr: 0, hlg: 1, hdr10: 2, dv: 4 }[range] + ((v.hdr || []).includes('hdr10plus') ? 1 : 0) + (v.dv && shown ? 1 : 0);
  const kbps = copy.durationMs > 0 ? (copy.size * 8) / copy.durationMs : 0;
  return v.height * 100 + rangeRank * 10 + Math.min(kbps / 100000, 9);
}

// ---- Tracks offered -----------------------------------------------------

/**
 * A copy's audio tracks as the player offers them: one per language
 * variant and role; of several, the one the device plays as it is (with
 * env), then the most channels, then the file's default; in the file's
 * order.
 */
export function audioMenu(copy, env) {
  const out = [];
  const plays = (t) => (env ? audioPlays(t, env).ok : false);
  for (const t of copy.audioTracks || []) {
    const i = out.findIndex((o) => t.language && o.language === t.language && roleClass(o.role) === roleClass(t.role));
    if (i < 0) { out.push(t); continue; }
    const o = out[i];
    const better = Number(plays(t)) - Number(plays(o)) || (t.channels || 0) - (o.channels || 0) || Number(!!t.default) - Number(!!o.default);
    if (better > 0) out[i] = t;
  }
  return out;
}

// The subtitle formats a player shows: text (served as WebVTT) and PGS
// pictures; DVD and DVB pictures are never served apart.
const TEXT_SUBTITLES = ['subrip', 'ass', 'ssa', 'webvtt', 'mov_text', 'ttml', 'hdmv_text_subtitle', 'microdvd', 'sami',
  'subviewer', 'mpl2', 'realtext', 'jacosub', 'pjs', 'vplayer', 'stl'];
const PICTURE_SUBTITLES = ['hdmv_pgs_subtitle'];

/** Whether a subtitle track is pictures (PGS) rather than text. */
export function pictureSubtitle(track) { return PICTURE_SUBTITLES.includes(track.format); }

/**
 * A copy's subtitles as the player offers them: { tracks, forced }, one
 * per language variant each (text before pictures, then not for the hard
 * of hearing first), forced ones apart, in the file's order. served: the
 * playback's served subtitles ({ id, form }), to keep only those.
 */
export function subtitleMenu(copy, served) {
  const ok = (t) => (served ? served.some((s) => s.id === t.id)
    : TEXT_SUBTITLES.includes(t.format) || PICTURE_SUBTITLES.includes(t.format));
  const score = (t) => (pictureSubtitle(t) ? 0 : 2) + (t.hearingImpaired ? 0 : 1);
  const best = (list) => {
    const out = [];
    for (const t of list) {
      const i = out.findIndex((o) => t.language && o.language === t.language && roleClass(o.role) === roleClass(t.role));
      if (i < 0) out.push(t);
      else if (score(t) > score(out[i])) out[i] = t;
    }
    return out;
  };
  const all = (copy.subtitleTracks || []).filter(ok);
  return { tracks: best(all.filter((t) => !t.forced)), forced: best(all.filter((t) => t.forced)) };
}

/** The forced subtitles shown with an audio track: those in its language. */
export function forcedFor(copy, audioTrack, served) {
  const a = (copy.audioTracks || []).find((t) => t.id === audioTrack);
  if (!a?.language) return null;
  const base = a.language.toLowerCase().split('-')[0];
  return subtitleMenu(copy, served).forced.find((t) => t.language && t.language.toLowerCase().split('-')[0] === base) || null;
}

/** The subtitles a language wish picks ('' or none: null). */
export function subtitlesFor(copy, language, served) {
  return pickLanguage(subtitleMenu(copy, served).tracks, language);
}

// ---- The fallback ladder's memory ------------------------------------------

const FALLBACKS = 'overfin-fallbacks';
const MAX_FALLBACKS = 300;

/**
 * What this device remembers of plays that failed (D12): by the copy's
 * facts and its audio track (never its ID), the level of LEVELS to start
 * from. Kept in localStorage; a browser that keeps nothing remembers
 * nothing.
 */
export const fallbacks = {
  /** The key of a copy's facts and an audio track. */
  key(copy, audioTrack) {
    const v = copy.video || {};
    const a = (copy.audioTracks || []).find((t) => t.id === audioTrack) || {};
    const facts = [copy.mediaType, v.codec, v.codecs, v.bitDepth, v.colour?.transfer, v.dv?.profile, v.dv?.compatibility,
      (v.hdr || []).join('+'), v.width, v.height, v.fps, a.codec, a.codecs, a.channels, a.objects,
      a.id === defaultAudio(copy)?.id];
    return hash(JSON.stringify(facts));
  },
  /** The level to start from (0: none remembered). */
  level(copy, audioTrack) {
    const e = readFallbacks()[this.key(copy, audioTrack)];
    return e && Number.isInteger(e.level) ? Math.min(Math.max(e.level, 0), LEVELS.length - 1) : 0;
  },
  /** Remembers that plays of this copy start from level at least. */
  remember(copy, audioTrack, level) {
    const all = readFallbacks();
    all[this.key(copy, audioTrack)] = { level, at: Date.now() };
    const keys = Object.keys(all);
    if (keys.length > MAX_FALLBACKS) {
      keys.sort((x, y) => all[x].at - all[y].at).slice(0, keys.length - MAX_FALLBACKS).forEach((k) => delete all[k]);
    }
    writeFallbacks(all);
  },
  /** How many copies' fallbacks are remembered. */
  count() { return Object.keys(readFallbacks()).length; },
  /** Forgets them all (the player's settings). */
  reset() { writeFallbacks(null); },
};

function readFallbacks() {
  try {
    const all = JSON.parse(globalThis.localStorage?.getItem(FALLBACKS) || '{}');
    return all && typeof all === 'object' && !Array.isArray(all) ? all : {};
  } catch {
    return {};
  }
}

function writeFallbacks(all) {
  try {
    if (all) globalThis.localStorage?.setItem(FALLBACKS, JSON.stringify(all));
    else globalThis.localStorage?.removeItem(FALLBACKS);
  } catch {
    // nothing kept: the next play tries again
  }
}

// hash is FNV-1a, 32 bits, in base 36.
function hash(s) {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h.toString(36);
}

// ---- The play request, and the addresses -----------------------------------

/**
 * The play request of a decision (POST /playback): MSFP's, its title
 * Overfin's titleId, with the reason for "Now playing". startPositionMs,
 * maxBitrateKbps (the quality chosen) and device as given.
 */
export function playRequest(decision, { titleId, startPositionMs = 0, maxBitrateKbps, device } = {}) {
  const r = { titleId, versionId: decision.copy.id, startPositionMs: Math.max(0, Math.round(startPositionMs)) };
  if (decision.audioTrack) r.audioTrack = decision.audioTrack;
  if (decision.delivery) r.delivery = decision.delivery;
  if (decision.mediaType) r.mediaType = decision.mediaType;
  if (decision.transcode) r.transcode = { ...decision.transcode };
  if (maxBitrateKbps > 0) r.maxBitrateKbps = Math.round(maxBitrateKbps);
  if (REASONS.includes(decision.reason)) r.reason = decision.reason;
  if (device) r.device = device;
  return r;
}

/**
 * The addresses of a playback, built from its ticket (MSFP video, section
 * 5.3): the stream (master.m3u8 for HLS, file for the file), a text
 * track's stretches, a picture track's, the previews' sheets.
 */
export function streamUrls(base, answer) {
  const root = `${base}/stream/${enc(answer.ticket)}`;
  return {
    stream: answer.protocol === 'file' ? `${root}/file` : `${root}/master.m3u8`,
    subtitles: (id) => `${root}/subtitles/${enc(id)}`,
    pictures: (id) => `${root}/pictures/${enc(id)}`,
    sheet: (n) => `${root}/trickplay/${n}`,
  };
}

/**
 * Seek-bar previews from a copy's previewLayout and a ticket's sheet
 * address (streamUrls().sheet): the layout with every sheet's address,
 * columns × rows tiles each, tile n at n × intervalMs.
 */
export function previewSheets(layout, sheet) {
  if (!layout || !(layout.columns > 0) || !(layout.rows > 0) || !(layout.tileCount > 0)) return undefined;
  const count = Math.ceil(layout.tileCount / (layout.columns * layout.rows));
  return { ...layout, sheets: Array.from({ length: count }, (_, n) => sheet(n)) };
}

// ---- Labels (fr, en) -----------------------------------------------------

// The words labels are made of, by language; English for any other.
const WORDS = {
  en: {
    unknownLanguage: 'Unknown language', mono: 'mono', stereo: 'stereo', channels: '{n} ch', original: 'original',
    forced: 'forced', sdh: 'SDH', version: 'Version', sd: 'SD',
    roles: { commentary: 'commentary', description: 'audio description', dub: 'dubbed', easyreader: 'easy reader',
      'enhanced-audio-intelligibility': 'clear dialogue', karaoke: 'karaoke' },
    subtitleRoles: { commentary: 'commentary', description: 'described', easyreader: 'easy reader' },
    editions: { theatrical: 'Theatrical', directors: 'Director\'s cut', extended: 'Extended', unrated: 'Unrated',
      remastered: 'Remastered', special: 'Special edition', imax: 'IMAX' },
  },
  fr: {
    unknownLanguage: 'Langue inconnue', mono: 'mono', stereo: 'stéréo', channels: '{n} canaux', original: 'VO',
    forced: 'forcés', sdh: 'SME', version: 'Version', sd: 'SD',
    roles: { commentary: 'commentaire', description: 'audiodescription', dub: 'doublage', easyreader: 'lecture facile',
      'enhanced-audio-intelligibility': 'dialogues clairs', karaoke: 'karaoké' },
    subtitleRoles: { commentary: 'commentaire', description: 'descriptifs', easyreader: 'lecture facile' },
    editions: { theatrical: 'Version cinéma', directors: 'Version du réalisateur', extended: 'Version longue',
      unrated: 'Non censurée', remastered: 'Remasterisée', special: 'Édition spéciale', imax: 'IMAX' },
  },
};

function words(lang) {
  return WORDS[String(lang || 'en').toLowerCase().split('-')[0]] || WORDS.en;
}

/** A language's name in itself ("Français (Canada)", "日本語"), from its tag. */
export function languageName(tag, lang) {
  const w = words(lang);
  if (!tag || tag === 'und' || tag === 'zxx' || tag === 'mul') return w.unknownLanguage;
  try {
    const name = new Intl.DisplayNames([tag], { type: 'language' }).of(tag);
    if (!name || name === tag) return w.unknownLanguage;
    return name.charAt(0).toLocaleUpperCase(tag) + name.slice(1);
  } catch {
    return w.unknownLanguage;
  }
}

/** A codec's usual name: H.264, HEVC, AV1, E-AC-3… */
export function codecName(token) {
  const names = { h264: 'H.264', hevc: 'HEVC', av1: 'AV1', vp9: 'VP9', vp8: 'VP8', mpeg2video: 'MPEG-2', mpeg4: 'MPEG-4',
    vc1: 'VC-1', aac: 'AAC', ac3: 'AC-3', eac3: 'E-AC-3', truehd: 'TrueHD', dts: 'DTS', flac: 'FLAC', opus: 'Opus',
    mp3: 'MP3', vorbis: 'Vorbis', alac: 'ALAC' };
  return names[token] || (token || '').toUpperCase();
}

/** The channels as people say them: mono, stereo, 5.1, 7.1. */
export function channelsName(n, lang) {
  const w = words(lang);
  if (!(n > 0)) return '';
  return { 1: w.mono, 2: w.stereo, 3: '2.1', 6: '5.1', 7: '6.1', 8: '7.1' }[n] || w.channels.replace('{n}', n);
}

/** A picture's lines as people say them: 4K, 1080p, 720p, SD. */
function linesName(v, w) {
  if (v.width >= 3200 || v.height >= 1800) return '4K';
  if (v.width >= 2200 || v.height >= 1300) return '1440p';
  if (v.width >= 1700 || v.height >= 950) return '1080p';
  if (v.width >= 1100 || v.height >= 650) return '720p';
  return w.sd;
}

/** A picture's range: HDR10, HDR10+, Dolby Vision, HLG; '' for SDR. */
export function rangeName(v) {
  if (!v) return '';
  if (v.dv) return 'Dolby Vision';
  const tr = v.colour?.transfer;
  if (tr === 16) return (v.hdr || []).includes('hdr10plus') ? 'HDR10+' : 'HDR10';
  if (tr === 18) return 'HLG';
  return '';
}

function baseLabel(copy, w) {
  const v = copy.video;
  return [v ? linesName(v, w) : '', rangeName(v), w.editions[copy.edition] || '', copy.threeD ? '3D' : ''].filter(Boolean);
}

/**
 * A copy's label, from its facts: lines, range, edition (unknown ones
 * hidden), 3D, and its codec only where another of copies differs from it
 * in nothing else. lang: fr or en.
 */
export function copyLabel(copy, lang, copies = []) {
  const w = words(lang);
  const parts = baseLabel(copy, w);
  const same = (copies || []).filter((o) => o.id !== copy.id && baseLabel(o, w).join() === parts.join());
  if (copy.video && same.some((o) => o.video && o.video.codec !== copy.video.codec)) parts.push(codecName(copy.video.codec));
  return parts.join(' · ') || w.version;
}

/** An audio track's label: language, channels, Atmos or DTS:X, role, original. */
export function audioLabel(track, lang) {
  const w = words(lang);
  const objects = { 'dolby-atmos': 'Atmos', 'dts-x': 'DTS:X' }[track.objects] || '';
  const role = roleClass(track.role) === 'main' && track.role !== 'dub' ? '' : w.roles[track.role] || '';
  return [languageName(track.language, lang), channelsName(track.channels, lang), objects, role, track.original ? w.original : '']
    .filter(Boolean).join(' · ');
}

/** A subtitle track's label: language, forced, SDH, role. */
export function subtitleLabel(track, lang) {
  const w = words(lang);
  return [languageName(track.language, lang), track.forced ? w.forced : '', track.hearingImpaired ? w.sdh : '',
    w.subtitleRoles[track.role] || ''].filter(Boolean).join(' · ');
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
 * @property {object} [env] What the device plays (deviceEnv()); detected by default.
 * @property {(playback: object) => void} [onStart] Called on every (re)start,
 *   with the playback: tracks, segments, method.
 * @property {(device: string) => void} [onTakenOver] Another device of the
 *   account took over; player.continueHere() takes it back.
 * @property {(error: Error) => void} [onError] The playback can't go on.
 * @property {(on: boolean) => void} [onReconnecting] The server can't be reached:
 *   the player waits for it, the place kept (true), then starts over there (false).
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
    this.choice = { audioTrack: options.audioTrack, versionId: options.versionId };
    // The device's answers (deviceEnv), once; the title's page (its
    // copies), read at the first start; the decision of the playback, the
    // level a refused or failed play steps down to, and the watch of a
    // play's first seconds (the fallback ladder).
    this.env = options.env || null;
    this.page = null;
    this.decision = null;
    this.minLevel = 0;
    this.trial = null;
    this.lastFallback = '';
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
      // Once it has played, whether it plays on after a restart is whether
      // it was playing, not the video's autoplay (always on in the site).
      playing: () => { this.everPlayed = true; },
      play: () => {
        if (this.pausedAt && Date.now() - this.pausedAt > LONG_PAUSE) this.check();
        this.pausedAt = 0;
      },
      timeupdate: () => { this.settled(); this.fillSubtitles(); this.watchTrial(); },
      waiting: () => this.jumpHole(),
      seeking: () => { this.fillSubtitles(); this.jumpHole(); },
      seeked: () => { this.settled(); this.report(); },
      // A direct stream cut short: the browser says error, or ends the
      // video where its data stopped. Not the end of the title.
      error: () => {
        if (this.hls) return;
        const code = this.video.error?.code;
        this.mediaFailed(code === 3 || code === 4, { details: 'media error' });
      },
      ended: () => {
        if (this.cutShort()) { this.fatal({ details: 'cut short' }); return; }
        this.report(); this.close(); this.options.onEnded?.();
      },
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
    // The title's page: its copies, and where the profile left off.
    if (!this.page) {
      let page;
      try {
        page = await this.client.title(this.titleId);
      } catch (err) {
        if (startMs === undefined) throw err;
        page = {};
      }
      if (overtaken()) return null;
      this.page = page;
    }
    const at = startMs ?? (this.page.userState?.positionMs || 0);
    if (this.page.copies?.length) return this.startCopy(at, overtaken);
    // No copy this profile may play (none read yet, or none it may see).
    this.page = null;
    const err = new OverfinError(409, { type: 'not-playable', title: 'This item can\'t be played', status: 409 });
    this.options.onError?.(err);
    throw err;
  }

  // startCopy starts a play of one of the title's copies, chosen here
  // (chooseDelivery): MSFP's play request, the answer's addresses built
  // from its ticket. A delivery refused steps down a level and asks
  // again, down to the baseline; a copy gone reads the title again, once.
  async startCopy(at, overtaken) {
    const page = this.page;
    this.env ||= deviceEnv({ Hls: this.options.Hls });
    const memory = this.options.fallbacks === undefined ? fallbacks : this.options.fallbacks;
    const decision = await chooseDelivery(page.copies, this.env, {
      versionId: this.choice.versionId, audioTrack: this.choice.audioTrack, audioLanguage: this.choice.audioLanguage,
      audio: page.wishes?.audio, language: this.options.audioLanguage, limits: page.limits, encoders: page.encoders,
      maxBitrateKbps: this.choice.maxBitrateKbps, minLevel: this.minLevel, peers: page.peers || this.options.peers, memory,
    });
    if (overtaken()) return null;
    let answer;
    try {
      answer = await this.client.startPlayback(playRequest(decision, {
        titleId: page.id || this.titleId, startPositionMs: at, maxBitrateKbps: this.choice.maxBitrateKbps,
      }));
    } catch (err) {
      if (overtaken()) return null;
      if (err?.status === 409 && err.type === 'delivery-refused' && decision.level < LEVELS.length - 1) {
        this.minLevel = decision.level + 1;
        return this.start(at);
      }
      if (err?.status === 404 && !this.reread) {
        this.reread = true;
        this.page = null;
        return this.start(at);
      }
      this.options.onError?.(err);
      throw err;
    }
    if (overtaken()) {
      this.client.stopPlayback(answer.id, Math.round(at)).catch(() => {});
      return null;
    }
    this.reread = false;
    this.decision = decision;
    const pb = this.view(answer, decision);
    this.playback = pb;
    this.attach(pb);
    for (const [event, fn] of Object.entries(this.handlers)) this.video.addEventListener(event, fn);
    this.timer = setInterval(() => { if (!this.video.paused) this.report(); }, PROGRESS_EVERY);
    this.trial = { at: Date.now(), fromMs: pb.positionMs || 0, done: decision.level >= LEVELS.length - 1 };
    // A file whose chosen audio isn't the one it plays by itself: that
    // track turned on in the video (audioTracks), else a step down.
    if (decision.delivery === 'file' && decision.audioTrack && decision.audioTrack !== defaultAudio(decision.copy)?.id) {
      this.video.addEventListener('loadedmetadata', () => {
        if (this.playback === pb && !this.switchFileAudio(pb, decision.audioTrack)) this.stepDown('audio-track');
      }, { once: true });
    }
    // A play that never gets going (no time played while it should
    // play): a step down too. Conversions get longer to start.
    const run = this.starts;
    this.stall = setTimeout(() => {
      const tr = this.trial;
      if (run !== this.starts || !tr || tr.done || !this.shouldPlay()) return;
      if (this.video.currentTime * 1000 - tr.fromMs < 500) this.stepDown('stalled');
    }, decision.level <= 1 ? TRIAL_STALL : TRIAL_STALL * 3);
    this.options.onStart?.(pb);
    return pb;
  }

  // view is a play's answer as the player shows it: the stream's address,
  // the copy's tracks with their labels and addresses, its skip markers
  // and previews, the copies to choose among, and how it plays (the
  // delivery, why, what's converted, the quirks that applied).
  view(answer, d) {
    const page = this.page;
    const lang = this.options.language || this.options.audioLanguage || 'en';
    const urls = streamUrls(this.client.base, answer);
    const copy = d.copy;
    const served = answer.subtitles || [];
    const playing = answer.audioTrack || d.audioTrack;
    const audio = audioMenu(copy, this.env);
    const chosen = (copy.audioTracks || []).find((t) => t.id === playing);
    if (chosen && !audio.includes(chosen)) audio.splice(audio.findIndex((t) => t.language === chosen.language && roleClass(t.role) === roleClass(chosen.role)), 1, chosen);
    const menu = subtitleMenu(copy, served);
    const subtitle = (t) => {
      const pictures = pictureSubtitle(t);
      return { ...t, label: subtitleLabel(t, lang), pictures, url: pictures ? urls.pictures(t.id) : urls.subtitles(t.id) };
    };
    const span = copy.endMs ? copy.endMs - (copy.startMs || 0) : copy.durationMs;
    return {
      ...answer,
      stream: { url: urls.stream, protocol: answer.protocol },
      durationMs: span || undefined,
      segments: copy.segments || [],
      audioTrack: playing,
      audioTracks: audio.map((t) => ({ ...t, label: audioLabel(t, lang) })),
      subtitleTracks: menu.tracks.map(subtitle),
      forcedTracks: menu.forced.map(subtitle),
      subtitleTrack: subtitlesFor(copy, page.wishes?.subtitles, served)?.id,
      forcedTrack: forcedFor(copy, playing, served)?.id,
      subtitleStretchSeconds: SUBTITLE_STRETCH,
      trickplay: previewSheets(copy.previewLayout, urls.sheet),
      versionId: copy.id,
      versions: page.copies.map((c) => ({ id: c.id, name: copyLabel(c, lang, page.copies) })),
      copy,
      delivery: { kind: d.delivery || 'baseline', mediaType: d.mediaType || '', transcode: d.transcode, reason: d.reason,
        level: d.level, converts: d.converts, quirks: d.quirks, fallback: d.reason === 'fallback' ? this.lastFallback : '' },
    };
  }

  // watchTrial is the fallback ladder's watch of a play's first seconds:
  // once it has played a few, a copy with a picture that shows none, or
  // with audio that's heard nowhere (the browser decoded none of it: the
  // silent case), steps down.
  watchTrial() {
    const tr = this.trial;
    const pb = this.playback;
    if (!tr || tr.done || !pb?.copy || this.video.currentTime * 1000 - tr.fromMs < TRIAL_PLAYED) return;
    tr.done = true;
    if (pb.copy.video && this.video.videoWidth === 0) this.stepDown('no-picture');
    else if ((pb.copy.audioTracks || []).length && silent(this.video)) this.stepDown('no-audio');
  }

  // mediaFailed handles a stream that failed: one the device couldn't
  // decode (decoding: a decode or format error, never the network) in a
  // play's first seconds, with the playback still the server's (not taken
  // over, ended or out of reach), steps down; anything else is fatal()'s.
  async mediaFailed(decoding, data) {
    const pb = this.playback;
    const tr = this.trial;
    if (decoding && pb && tr && !tr.done && Date.now() - tr.at <= TRIAL_TIME) {
      let alive = true;
      try { await this.client.playback(pb.id); } catch { alive = false; }
      if (alive && pb === this.playback && this.stepDown('media-error')) return;
    }
    this.fatal(data);
  }

  // stepDown starts the play again one level lower (file, remux, the
  // audio converted, the picture too, the baseline), remembered for this
  // device and these facts (D12).
  stepDown(why) {
    const d = this.decision;
    if (!d || !this.playback || d.level >= LEVELS.length - 1) return false;
    this.trial = null;
    this.minLevel = Math.max(this.minLevel, d.level + 1);
    const memory = this.options.fallbacks === undefined ? fallbacks : this.options.fallbacks;
    memory?.remember(d.copy, d.audioTrack, d.level + 1);
    this.lastFallback = why;
    this.restart(!this.video.paused || this.video.autoplay);
    return true;
  }

  /** The decision the playback plays by (chooseDelivery's), or null before it plays. */
  get delivery() { return this.decision; }

  /**
   * Plays another copy of the title, at the same place, with the audio in
   * the same language when it has it.
   */
  setVersion(id) {
    const now = this.playback?.audioTracks?.find((t) => t.id === this.playback.audioTrack);
    this.choice.versionId = id;
    this.choice.audioTrack = undefined;
    this.choice.audioLanguage = now?.language || this.choice.audioLanguage;
    this.minLevel = 0;
    return this.restart();
  }

  // attach points the video element at the stream, with its subtitles.
  attach(pb) {
    const video = this.video;
    for (const t of video.querySelectorAll('track[data-overfin]')) t.remove();
    // A paired server's title streams from its own address: the video
    // fetches it in CORS mode (no cookies), as subtitles from there load
    // only that way.
    const here = globalThis.location?.href;
    const remote = here ? new URL(pb.stream.url, here).origin !== new URL(here).origin : false;
    if (remote) video.crossOrigin = 'anonymous';
    else video.removeAttribute('crossorigin');
    // Text tracks come in stretches (fillSubtitles): each is a track of
    // the video without a file of its own, its cues added as they come.
    this.texts = new Map();
    for (const t of [...(pb.subtitleTracks || []), ...(pb.forcedTracks || [])]) {
      if (!t.url || t.pictures || this.texts.has(t.id)) continue;
      const el = document.createElement('track');
      Object.assign(el, { kind: 'subtitles', srclang: t.language || 'und', label: t.label });
      el.dataset.overfin = t.id;
      video.append(el);
      this.texts.set(t.id, { el, url: t.url, parts: new Map(), seen: new Set() });
    }
    const startAt = (pb.positionMs || 0) / 1000;
    const Hls = this.options.Hls || globalThis.Hls;
    if (pb.stream.protocol === 'hls' && Hls && Hls.isSupported()) {
      this.hls = new Hls({ startPosition: startAt });
      this.hls.on(Hls.Events.ERROR, (_, data) => { if (data.fatal) this.mediaFailed(data.type === 'mediaError', data); });
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
    // The file playing has that track, and the browser switches tracks:
    // no new play.
    const now = this.playback;
    if (now?.delivery?.kind === 'file' && this.switchFileAudio(now, id)) {
      now.audioTrack = id;
      now.forcedTrack = forcedFor(now.copy, id, now.subtitles || [])?.id;
      this.showSubtitles();
      this.client.chooseTracks(now.id, { audioTrack: id }).catch(() => {});
      this.options.onStart?.(now);
      return now;
    }
    this.minLevel = 0;
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
      if (this.pictures.url !== track.url) this.pictures.show(track.url);
    } else if (this.pictures) {
      this.pictures.hide();
    }
    this.fillSubtitles();
  }

  // fillSubtitles fetches the stretches of the subtitles shown around where
  // the video is: the one playing and the next, each once. Subtitles are a
  // bonus: one that fails is tried again later, and playback goes on.
  fillSubtitles() {
    const pb = this.playback;
    const len = pb?.subtitleStretchSeconds;
    const id = this.subtitleTrack || this.forcedTrack;
    if (!pb || !len || !id) return;
    const at = Math.floor(this.positionMs / 1000 / len);
    const text = this.texts?.get(id);
    for (const part of [at, at + 1]) {
      if (text) this.textPart(pb, text, part);
      else if (this.pictures?.url) this.pictures.part(part);
    }
  }

  // textPart adds one stretch of a text track's cues to its track, those it
  // doesn't have yet: stretches overlap a little.
  textPart(pb, text, part) {
    if (!wanted(text.parts, part)) return;
    text.parts.set(part, LOADING);
    fetch(`${text.url}/${part}`, { credentials: 'same-origin' })
      .then((res) => (res.ok ? res.text() : Promise.reject(new Error(`subtitles: HTTP ${res.status}`))))
      .then((vtt) => {
        if (this.playback !== pb) return;
        for (const c of parseVTT(vtt)) {
          const key = `${c.start}|${c.end}|${c.text}`;
          if (text.seen.has(key)) continue;
          text.seen.add(key);
          text.el.track.addCue(new VTTCue(c.start, c.end, c.text));
        }
        text.parts.set(part, LOADED);
        text.el.dispatchEvent(new Event('load')); // what a track's file does once it's here
      })
      .catch(() => text.parts.set(part, Date.now() + SUBTITLE_RETRY));
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
    return this.restart(true);
  }

  /**
   * Stops for good: the server frees the stream at once. ended: the title
   * counts as watched to its end (left from its credits for the next one).
   */
  async close({ ended = false } = {}) {
    if (this.closed) return;
    const pb = this.playback;
    // Ended: stopped at its end, in the one request (a report first would
    // hold the stop back a round trip, the next title already starting).
    const position = ended && pb?.durationMs ? pb.durationMs : this.positionMs;
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

  // restart starts over at the same place; playing: whether to play on (by
  // default, whether it plays now). Paused, it stays paused: an audio
  // track or a quality picked, or the server back, doesn't start the film.
  // switchFileAudio turns on a file's audio track in the video itself
  // (audioTracks, in the file's order): false where the browser can't.
  switchFileAudio(pb, id) {
    const list = this.video.audioTracks;
    if (!list || !list.length) return false;
    const inFile = (pb.copy.audioTracks || []).filter((t) => !t.external)
      .sort((x, y) => Number(x.id.slice(1)) - Number(y.id.slice(1)));
    const i = inFile.findIndex((t) => t.id === id);
    if (i < 0 || i >= list.length || inFile.length !== list.length) return false;
    for (let n = 0; n < list.length; n++) list[n].enabled = n === i;
    return true;
  }

  restart(playing = !this.video.paused) {
    const at = this.positionMs;
    this.held = at;
    const old = this.playback;
    this.teardown();
    if (old) this.client.stopPlayback(old.id, at).catch(() => {});
    this.video.autoplay = this.everPlayed ? playing : this.video.autoplay || playing;
    return this.start(at);
  }

  // jumpHole: a stream that copies the picture starts each part at a
  // keyframe, which may come seconds after where the part says it starts
  // (Jellyfin cuts such streams of files other than Matroska in equal
  // parts, whatever their keyframes): started or sought there, the video
  // waits (or seeks) in a hole before the first picture. hls.js jumps
  // holes of up to 2 s; the player jumps to the picture when it's near
  // (HOLE_JUMP).
  jumpHole() {
    clearTimeout(this.hole);
    if (this.playback?.delivery?.kind === 'file') return;
    this.hole = setTimeout(() => {
      const v = this.video, t = v.currentTime;
      if (this.closed || !this.shouldPlay() || !v.buffered) return;
      for (let i = 0; i < v.buffered.length; i++) {
        const start = v.buffered.start(i);
        if (start <= t && t < v.buffered.end(i) - 0.1) return;
        if (start > t && start - t <= HOLE_JUMP) { v.currentTime = start + 0.05; return; }
      }
    }, HOLE_WAIT);
  }

  // shouldPlay: playing, or about to (autoplay waiting for its first
  // pictures: the video says paused until then).
  shouldPlay() {
    const v = this.video;
    return !v.paused || (v.autoplay && v.readyState < 3);
  }

  teardown() {
    clearInterval(this.timer);
    this.timer = null;
    clearTimeout(this.stall);
    clearTimeout(this.hole);
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

  // cutShort says whether the video ended before its title did: a stream
  // cut off (the server out of reach) ends where its data stopped.
  cutShort() {
    const end = (this.playback?.durationMs || 0) / 1000 || this.video.duration;
    return Number.isFinite(end) && end > 0 && this.video.currentTime < end - 5;
  }

  // fatal handles a stream that failed (hls.js gave up, a direct stream cut
  // short): with the playback gone, as for any report; the server out of
  // reach, it waits for it, then starts over at the same place; otherwise it
  // starts over at once, a few times a minute at most, then says so, rather
  // than stay frozen on a playback that holds the account's stream.
  async fatal(data) {
    const pb = this.playback;
    if (!pb || this.closed || this.waiting) return;
    try {
      await this.client.playback(pb.id);
    } catch (err) {
      if (unreachable(err)) {
        this.waitForServer();
        return;
      }
      this.gone(pb, err);
      // Taken over, ended, expired or forgotten: gone() took it from
      // there (a taken-over playback is kept for Continue here, and must
      // not start again by itself).
      if (err?.status === 410 || err?.status === 404) return;
    }
    if (pb !== this.playback || this.closed) return;
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

  // waitForServer holds the place while the server can't be reached (the
  // connection lost, its gateway saying it's out of reach or being
  // updated), asks again every few seconds, and starts over at the same
  // place once it answers. onReconnecting says when it starts and stops.
  async waitForServer() {
    const pb = this.playback;
    if (!pb || this.waiting || this.closed) return;
    this.waiting = true;
    this.held = this.positionMs;
    const wasPlaying = !this.video.paused || (!this.everPlayed && this.video.autoplay);
    this.video.pause();
    this.options.onReconnecting?.(true);
    while (!this.closed && pb === this.playback) {
      await new Promise((r) => setTimeout(r, RECONNECT_EVERY));
      try {
        await this.client.playback(pb.id);
        break;
      } catch (err) {
        if (!unreachable(err)) break; // answered: restart() sorts out a playback it forgot
      }
    }
    this.waiting = false;
    this.options.onReconnecting?.(false);
    if (this.closed || pb !== this.playback) return;
    this.restart(wasPlaying);
  }

  // gone handles a playback the server no longer has.
  gone(pb, err) {
    if (pb !== this.playback || this.closed) return;
    // Freed after a pause or a lost connection, or no longer known at all
    // (the server forgets why after an hour, and everything when it
    // restarts): carry on, unnoticed.
    if ((err.status === 410 && err.type === 'playback-expired') || err.status === 404) {
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
 * The last cue ends at Infinity. With clears, the moments the screen is
 * cleared are cues too, with no picture (for stretches of a track, merged
 * later). Throws on a malformed track.
 */
export function decodePGS(bytes, { clears = false } = {}) {
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
        if (pictures.length || clears) {
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
    this.url = '';
    this.parts = new Map(); // stretch → LOADING, LOADED or when to try again
  }

  /**
   * Shows a track from its URL, its stretches after it ("/0", "/1"…),
   * fetched with part(); nothing on screen until they come.
   */
  show(url) {
    this.hide();
    this.url = url;
    this.tick();
  }

  /**
   * Fetches one stretch of the track shown, once, and adds its cues; one
   * that fails is tried again later.
   */
  part(n) {
    if (!this.url || !wanted(this.parts, n)) return;
    this.parts.set(n, LOADING);
    const run = this.loads;
    fetch(`${this.url}/${n}`)
      .then((res) => (res.ok ? res.arrayBuffer() : Promise.reject(new Error(`picture subtitles: HTTP ${res.status}`))))
      .then((bytes) => {
        if (run !== this.loads) return;
        this.add(decodePGS(new Uint8Array(bytes), { clears: true }));
        this.parts.set(n, LOADED);
      })
      .catch(() => { if (run === this.loads) this.parts.set(n, Date.now() + SUBTITLE_RETRY); });
  }

  // add merges a stretch's cues with those already here: one per start,
  // each lasting until the next starts (a clear, between lines, is a cue
  // with no picture), so a line whose clear came in the next stretch
  // leaves the screen on time.
  add(cues) {
    const byStart = new Map(this.cues.map((c) => [c.startMs, c]));
    for (const c of cues) if (!byStart.has(c.startMs)) byStart.set(c.startMs, c);
    const all = [...byStart.values()].sort((a, b) => a.startMs - b.startMs);
    for (let i = 0; i < all.length; i++) all[i].endMs = i + 1 < all.length ? all[i + 1].startMs : Infinity;
    this.cues = all;
  }

  /** Stops drawing and clears the canvas; show() starts again. */
  hide() {
    this.loads++;
    cancelAnimationFrame(this.frame);
    this.frame = 0;
    this.url = '';
    this.parts = new Map();
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

// How long a stretch of subtitles covers, in seconds (MSFP media section
// 3.4).
const SUBTITLE_STRETCH = 120;

// The fallback ladder watches a play's first seconds: a failure within
// TRIAL_TIME of its start, or no picture or no audio once TRIAL_PLAYED of
// it played, steps down.
const TRIAL_TIME = 30_000;
const TRIAL_PLAYED = 3_000;
// A file or a remux that played nothing this long after it started, while
// it should play, failed (three times as long for a conversion).
const TRIAL_STALL = 15_000;
// A stream waiting HOLE_WAIT in a hole before pictures at most HOLE_JUMP
// seconds ahead jumps to them (jumpHole).
const HOLE_WAIT = 1_500;
const HOLE_JUMP = 15;

// silent says whether the browser decoded no audio at all of what played:
// Firefox's mozHasAudio, Chromium's and Safari's decoded byte count; false
// where it can't tell.
function silent(video) {
  if (typeof video.mozHasAudio === 'boolean') return !video.mozHasAudio;
  if (typeof video.webkitAudioDecodedByteCount === 'number') return video.webkitAudioDecodedByteCount === 0;
  return false;
}

// How often a player waiting for the server asks again.
const RECONNECT_EVERY = 3_000;

// unreachable says whether an error means the server can't be reached for
// now (no answer at all, or its gateway saying it's out of reach or being
// updated), rather than a refusal.
function unreachable(err) {
  return !err?.status || err.status === 502 || err.status === 503 || err.status === 504;
}

// A stretch of subtitles is being fetched, or is here; else the map holds
// when to try a failed one again.
const LOADING = 'loading', LOADED = 'loaded';
const SUBTITLE_RETRY = 10_000;

// wanted says whether a stretch should be fetched now.
function wanted(parts, n) {
  const v = parts.get(n);
  return v === undefined || (typeof v === 'number' && Date.now() >= v);
}

/**
 * Reads WebVTT, as Overfin rebuilds it (timings and plain text), into
 * cues: { start, end, text }, in seconds.
 */
export function parseVTT(vtt) {
  const cues = [];
  for (const block of vtt.replace(/\r/g, '').split(/\n\n+/)) {
    const lines = block.split('\n');
    const i = lines.findIndex((l) => l.includes('-->'));
    const m = i < 0 ? null : /^((?:\d+:)?\d{2}:\d{2}\.\d{3})\s+-->\s+((?:\d+:)?\d{2}:\d{2}\.\d{3})/.exec(lines[i]);
    const text = m ? lines.slice(i + 1).join('\n') : '';
    if (text) cues.push({ start: vttSeconds(m[1]), end: vttSeconds(m[2]), text });
  }
  return cues;
}

function vttSeconds(t) {
  return t.split(':').reduce((s, part) => s * 60 + Number(part), 0);
}

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
