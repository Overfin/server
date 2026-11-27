// Types for overfin.js. The API is specified in api/openapi.yaml; these
// cover what the SDK returns. Fields marked optional may be absent.

export interface Problem {
  type: string;
  title: string;
  status: number;
  detail?: string;
  retryAfter?: number;
  takenOverBy?: string;
  stepUp?: string;
  stepUpUrl?: string;
  /** For account-setup-required: whether an existing account's history can be brought. */
  claim?: 'copy';
  /** For account-setup-required: the languages the new account may choose among… */
  languages?: { tag: string; name: string }[];
  /** …and the one it gets unless it chooses. */
  language?: string;
}

/** A wished title, and what became of it: waiting its turn, requested (handed to Seerr, not started yet), not out yet (releaseDate, when known), not found yet (tried again at nextTry), or downloading. */
export interface Wish {
  title: TitleSummary;
  state: 'waiting' | 'requested' | 'unreleased' | 'notFound' | 'downloading';
  releaseDate?: string;
  nextTry?: string;
}

/** The profile's wishlist. on is false when its owner switched its wishing off (its wishes wait); arrived, what came since it was last told (on its watchlist now). */
export interface Wishlist {
  on: boolean;
  items: Wish[];
  arrived: TitleSummary[];
}

export class OverfinError extends Error {
  constructor(status: number, problem: Problem);
  readonly status: number;
  /** Stable error name, such as 'pin-incorrect' or 'step-up-required'. */
  readonly type: string;
  readonly problem: Problem;
  readonly retryAfter?: number;
}

export interface Profile {
  id: string;
  name: string;
  kind: 'standard' | 'kids' | string;
  owner: boolean;
  hasPin: boolean;
  /** A kids profile's age: it sees titles for that age at most, films and series alike (since 0.6). */
  maxAge?: number;
  /** A kids profile without an age yet: it sees nothing until its owner picks one. */
  ageNeeded?: boolean;
  /** The only categories it sees, when it's limited to some (since 0.6). */
  categories?: string[];
  /** "Contenu pour enfants": its recommendations include children's content (since 0.6). */
  kidsTitles?: boolean;
  /** The audio it prefers: "original" or a language tag; absent, the site's language. */
  audioLanguage?: string;
  /** The subtitles it prefers, a language tag; absent, none. */
  subtitleLanguage?: string;
  /** The only libraries the profile gets (their IDs); absent, every library. */
  libraries?: string[];
  /** Tags hidden from the profile. */
  blockedTags?: string[];
  /** The profile's look; absent, it follows the device. */
  theme?: 'light' | 'dark';
}

/** The brand a site shows; files are addresses on the server. */
export interface Branding {
  custom: boolean;
  name: string;
  logo?: string;
  banner: { dark?: string; light?: string };
}

export interface Me {
  accountId: string;
  /** Absent until a profile is chosen (when the owner profile has a PIN). */
  activeProfile?: Profile;
  /** The account's language, a regional tag such as "fr-CA". */
  language: string;
  /** Those it may choose among, each named in itself. */
  languages: { tag: string; name: string }[];
  /** The account template's limits; 0 means none. With reservedStreams,
   * each profile owns one of the streams (never lent) and sharedStreams
   * are the rest, any profile's; without, sharedStreams is maxStreams. */
  limits: { maxProfiles: number; maxStreams: number; reservedStreams: boolean; sharedStreams: number };
  /** How the operator set the home page: missingTitles shows titles no
   * library has among what's trending and in the slideshow. */
  home: { missingTitles: boolean };
  /** How the operator set title pages: availability lists where a film or an episode can be played (since 0.9). */
  titlePages?: { availability: boolean };
  /** Whether the operator asks accounts what they like, and whether this
   * one answered (or chose to say nothing). */
  tastes: { asked: boolean; answered: boolean };
  /** What the server offers that depends on its setup: discovery (TMDB or
   * Seerr) and wishlists (since 0.8, when requests went). Absent before 0.5. */
  offers?: { discovery: boolean; wishes?: boolean };
  /** The control panel's path on the site ("/admin/"), only for an admin
   * who can open it there; absent otherwise. Since 0.6. */
  panel?: string;
}

/** What an account says it likes, for what the server keeps and gets. */
export interface Tastes {
  /** Absent when it didn't say. */
  prefer?: 'series' | 'movies' | 'both';
  /** TMDB genre IDs. */
  genres: number[];
  updatedAt?: string;
}

export interface TastesPage extends Tastes {
  answered: boolean;
  /** The genres to choose among, named in the server's language. */
  choices: { id: number; name: string }[];
  /** The account has a kids profile: it counts as wanting children's content. */
  kidsProfile: boolean;
}

export interface Session {
  id: string;
  current: boolean;
  device?: string;
  profileName?: string;
  createdAt: string;
  lastActiveAt?: string;
}

export interface ImageRef {
  url: string;
  aspectRatio?: number;
  blurHash?: string;
}

export interface Images {
  primary?: ImageRef;
  backdrops?: ImageRef[];
  logo?: ImageRef;
  thumb?: ImageRef;
  /** A film's or a series' poster without text, for the background of a screen taller than wide (since 0.7). */
  textless?: ImageRef;
}

export interface UserState {
  positionMs?: number;
  played: boolean;
  playCount?: number;
  favorite: boolean;
  lastPlayedAt?: string;
  unplayedCount?: number;
}

export interface Genre {
  name: string;
  /** How many of the library's titles the profile may see have it. */
  itemCount?: number;
}

export interface Library {
  id: string;
  name: string;
  kind: string;
  image?: ImageRef;
}

/** A section of the site the operator made from rules (since 0.6). */
export interface Category {
  id: string;
  name: string;
  /** Also a row on the home page. */
  home: boolean;
}

/** What a library's titles are filtered and sorted by. */
export interface TitleQuery {
  library?: string;
  /** A category's titles (since 0.6). */
  category?: string;
  /** Required by titles() and pages(): the server refuses a listing without it. */
  kind?: 'movie' | 'series' | ('movie' | 'series')[];
  /** One genre, or several (any of them). */
  genre?: string | string[];
  yearFrom?: number;
  yearTo?: number;
  played?: boolean;
  favorite?: boolean;
  inProgress?: boolean;
  sort?: 'name' | 'addedAt' | 'premiereDate' | 'communityRating' | 'rank' | 'lastPlayed' | 'random';
  order?: 'asc' | 'desc';
  seed?: string;
  limit?: number;
  cursor?: string;
}

export interface Track {
  id: string;
  /** The language's name in itself: "Français (Canada)", "日本語"; "?" when unknown. */
  label: string;
  /** Its tag, with its region when the file says it ("fr-CA"). */
  language?: string;
  channels?: number;
  /** Audio besides the film's own sound. */
  role?: 'commentary' | 'description' | string;
  default?: boolean;
  forced?: boolean;
  hearingImpaired?: boolean;
  /** A copy's track (a play of a copy): its facts, as MSFP gives them. */
  codec?: string;
  objects?: string;
  original?: boolean;
  format?: string;
  /** Subtitles that are pictures (PGS), drawn over the video (PictureSubtitles). */
  pictures?: boolean;
  /** Subtitles, in stretches at `url` + "/n" (see subtitleStretchSeconds): WebVTT for text, PGS for pictures. */
  url?: string;
}

/** A copy of a film or an episode: MSFP's copy object (MSFP video, section 3.3). */
export interface Copy {
  id: string;
  etag: string;
  edition?: string;
  durationMs: number;
  startMs?: number;
  endMs?: number;
  mediaType: string;
  size: number;
  parts?: number;
  video?: CopyVideo;
  audioTracks: CopyAudio[];
  subtitleTracks?: CopySubtitle[];
  chapters?: { startMs: number; name?: string }[];
  segments?: Segment[];
  previewLayout?: PreviewLayout;
  threeD?: string;
}

export interface CopyVideo {
  codec: string;
  codecs?: string;
  width: number;
  height: number;
  sampleAspect?: string;
  rotation?: number;
  bitDepth?: number;
  colour?: { primaries?: number; transfer?: number; matrix?: number; fullRange?: boolean };
  hdr?: string[];
  dv?: { profile: number; level: number; compatibility: number; el?: boolean };
  fps?: string;
  refFrames?: number;
  interlaced?: boolean;
}

export interface CopyAudio {
  id: string;
  codec: string;
  codecs?: string;
  language?: string;
  channels?: number;
  role?: string;
  default?: boolean;
  forced?: boolean;
  objects?: string;
  sampleRate?: number;
  bitDepth?: number;
  original?: boolean;
  external?: boolean;
}

export interface CopySubtitle {
  id: string;
  language?: string;
  role?: string;
  default?: boolean;
  forced?: boolean;
  hearingImpaired?: boolean;
  format?: string;
  external?: boolean;
}

export interface PreviewLayout {
  tileWidth: number;
  tileHeight: number;
  columns: number;
  rows: number;
  intervalMs: number;
  tileCount: number;
}

/** What the server converts to: registry tokens, the ranges a converted picture comes in, whether it tone maps. */
export interface Encoders {
  video: string[];
  audio: string[];
  ranges: ('sdr' | 'keep')[];
  toneMapping: boolean;
}

export interface Segment {
  kind: 'intro' | 'outro' | 'recap' | 'preview' | 'commercial' | string;
  startMs: number;
  endMs: number;
}

/** A title, in a library or not: a film, a series, a season, an episode. */
/** What a search found. */
export interface SearchResults {
  titles: TitleSummary[];
  people: { id: string; name: string; department?: string; image?: ImageRef }[];
  peopleFirst?: boolean;
  /**
   * Present when the terms were a title's ID or a link to one (since
   * 0.10). id is the ID as read (imdb:tt0133093, tmdb:movie/603…; for
   * terms that look like an ID but aren't, its source alone, imdb:);
   * invalid: none could be read, nothing was searched; seriesInstead: the
   * ID names an episode the server can't show alone, its series is given.
   */
  byId?: { id: string; invalid?: boolean; seriesInstead?: boolean };
}

export interface TitleSummary {
  id: string;
  kind: 'movie' | 'series' | 'season' | 'episode' | 'other' | (string & {});
  name: string;
  year?: number;
  /** A season's or an episode's, YYYY-MM-DD. */
  airDate?: string;
  durationMs?: number;
  /** Its rating in the server's ratings country (its series', for a season or an episode). */
  certification?: string;
  /** Its age, as the server works it out from its ratings in any country
   * (its series', for a season or an episode); absent when unknown. Since 0.6. */
  age?: number;
  /** Users' scores (a film's or a series'), by source: TMDB's for now. */
  ratings?: { source: string; value: number; votes: number }[];
  availability: 'available' | 'partiallyAvailable' | 'missing';
  /** A film or an episode a library has: play it by its ID. */
  playable: boolean;
  series?: { id: string; name: string };
  /** An episode's (its name may be empty). */
  season?: { id: string; name: string };
  seasonNumber?: number;
  episodeNumber?: number;
  /** A series' or a season's episodes, specials aside. */
  episodeCount?: { inLibrary: number; aired: number };
  images?: Images;
  userState?: UserState;
  /** In a collection's films and the upcoming discovery list: one no library has that isn't out yet. It has no page and can't be wished for until it's out (since 0.7; in discovery since 0.10). */
  upcoming?: boolean;
  /** When it leaves the server (ISO date), held for a last watch: only for a profile that keeps it for later or is partway through it (since 0.10). */
  leavesAt?: string;
}

/** A picture: its height (480, 720, 1080, 1440, 2160 or 4320) and its HDR formats, none for standard range. */
export interface Picture {
  height: number;
  hdr?: ('hdr10' | 'hlg' | 'dv')[];
}

/** One place a film or an episode plays from. */
export interface TitleSource {
  /** Absent when the page doesn't list where it's available. */
  name?: string;
  /** This server. */
  local?: boolean;
  video?: Picture;
  audio?: string[];
  subtitles?: string[];
}

/** A title's page: a series with its seasons, a season with its episodes. */
export interface Title extends TitleSummary {
  /** Who directed and wrote an episode, the job as character ("Director", "Writer"); its guest stars are in cast (since 0.9). */
  crew?: { id?: string; name: string; character?: string; image?: ImageRef }[];
  /**
   * A film's or an episode's copies the profile may see, MSFP's copy
   * objects as they are, the server's own first: facts only, labels built
   * by copyLabel(), audioLabel(), subtitleLabel() (since 0.10; before: a
   * picture's name and languages).
   */
  copies?: Copy[];
  /** IDs of the copies paired servers hold (the player prefers this server's own). */
  peers?: string[];
  /** What a play of copies goes by: the operator's bitrate cap, whether players may get the file itself (since 0.10). */
  limits?: { maxBitrateKbps?: number; originalFile: boolean };
  /** The audio and subtitle languages a play starts with: the title's choice, else the profile's (since 0.10). */
  wishes?: { audio?: string; subtitles?: string };
  /** What the server converts to now (since 0.10); absent when it can't say. */
  encoders?: Encoders;
  /** Where a film or an episode plays from for the profile, in the order Play picks them (the first is Play's): this server (local, by its brand's name) and the paired servers holding it, each once, by the names the server's menu gives them; each with its best copy's picture and its languages, named in themselves. With showAvailability off, only the first, without its name (since 0.9). */
  sources?: TitleSource[];
  /** Whether the page lists sources: the control panel's setting, on by default (since 0.9). */
  showAvailability?: boolean;
  /** The profile said it isn't interested in this film or series (since 0.9). */
  notInterested?: boolean;
  /** A film's or a series' place on the server by its value, among the titles of its kind the profile may see, 1 the most valued (since 0.9). */
  rank?: number;
  /** The collection a film belongs to, when the profile sees it (since 0.7). */
  collection?: { id: string; name: string };
  overview?: string;
  tagline?: string;
  genres?: string[];
  cast?: { id?: string; name: string; character?: string; image?: ImageRef }[];
  seasons?: TitleSummary[];
  /** A season's episodes, each with what it's about. */
  /** A file holding several episodes is one row: episodeEnd its last, parts each episode's title and overview (since 0.10). */
  episodes?: (TitleSummary & { overview?: string; episodeEnd?: number; parts?: { name: string; overview?: string }[] })[];
}

/** One of TMDB's collections: a series of films, such as a saga (since 0.7). */
export interface Collection {
  id: string;
  name: string;
  /** On its own page only. */
  overview?: string;
  images?: Images;
  /** How many of its films the profile has in a library, of films in all. */
  inLibrary: number;
  films: number;
  /** Its films in release order, on its own page only. */
  items?: TitleSummary[];
}

/** A person, from TMDB, and the titles they're in. */
export interface Person {
  id: string;
  name: string;
  biography?: string;
  birthday?: string;
  deathday?: string;
  birthplace?: string;
  department?: string;
  image?: ImageRef;
  credits: { title: TitleSummary; roles?: string[] }[];
}

export interface TitlePage {
  items: TitleSummary[];
  nextCursor?: string;
  totalCount?: number;
  seed?: string;
}

export interface Playback {
  id: string;
  /** Its ticket, the addresses built from it (streamUrls). */
  ticket: string;
  protocol: 'hls' | 'file';
  /** The copy playing, and how it plays. */
  copy: Copy;
  delivery: PlaybackDelivery;
  /** What plays; absent when the server has no title for the item yet. */
  title?: TitleSummary;
  method: 'direct' | 'remux' | 'transcode' | string;
  stream: { url: string; protocol: 'file' | 'hls'; mimeType?: string };
  positionMs: number;
  durationMs?: number;
  versions?: { id: string; name: string }[];
  versionId?: string;
  audioTracks?: Track[];
  audioTrack?: string;
  subtitleTracks?: Track[];
  subtitleTrack?: string;
  /** Subtitles for the lines in another language only. */
  forcedTracks?: Track[];
  /** Those in the audio's language: shown whenever no other subtitles are. */
  forcedTrack?: string;
  /** How long each stretch of a subtitle track is, in seconds: stretch n covers [n × it, (n+1) × it). */
  subtitleStretchSeconds?: number;
  maxBitrateKbps?: number;
  segments?: Segment[];
  /** Seek-bar previews, when the server has some: sheets of `columns` x `rows` tiles, tile n at n x `intervalMs`. */
  trickplay?: Trickplay;
  /** For an episode, the one after it (it may not be playable); absent at the end. */
  next?: TitleSummary;
}

/** How a play of a copy plays: chooseDelivery()'s decision, as the information panel shows it. */
export interface PlaybackDelivery {
  /** file, remux, transcode, or baseline (MSFP's: the server converts what isn't within it). */
  kind: 'file' | 'remux' | 'transcode' | 'baseline';
  mediaType: string;
  transcode?: TranscodeTargets;
  reason: Reason;
  /** Its level of LEVELS. */
  level: number;
  converts: { video: boolean; audio: boolean };
  /** The QUIRKS that applied on this device. */
  quirks: string[];
  /** Why the ladder stepped down to it (no-picture, no-audio, media-error), when it did. */
  fallback: string;
}

export interface TranscodeTargets {
  videoCodec?: string;
  audioCodec?: string;
  maxHeight?: number;
  maxBitrateKbps?: number;
  audioChannels?: number;
  range?: 'sdr' | 'keep';
}

/** Why a player chose its delivery: a closed list. */
export type Reason = 'as-is' | 'container' | 'audio-track' | 'audio-codec' | 'video-codec' | 'picture-range' | 'bitrate'
  | 'fallback' | 'quirk' | 'in-doubt';

export interface Trickplay {
  tileWidth: number;
  tileHeight: number;
  columns: number;
  rows: number;
  intervalMs: number;
  tileCount: number;
  sheets: string[];
}

export interface PlayerOptions {
  /** Where to start; by default where the profile left off. */
  startMs?: number;
  /** The copy to play; by default the one chooseDelivery() picks. */
  versionId?: string;
  /** The language labels are in (fr, en); by default audioLanguage's, else English. */
  language?: string;
  /** What the device plays; deviceEnv() by default. */
  env?: DeviceEnv;
  /** The fallback ladder's memory (fallbacks by default); null: none. */
  fallbacks?: typeof fallbacks | null;
  /** IDs of copies on paired servers; by default the title page's. */
  peers?: string[];
  audioTrack?: string;
  subtitleTrack?: string;
  /** The language to start in (the app's), when the video has it and no audioTrack is given. */
  audioLanguage?: string;
  /** Every (re)start, with the playback: tracks, segments, method. */
  onStart?: (playback: Playback) => void;
  /** Another device took over; player.continueHere() takes it back. */
  onTakenOver?: (device: string) => void;
  /** The playback can't go on. */
  onError?: (error: Error) => void;
  /** The server can't be reached: the player waits for it, its place kept (true), then starts over there (false). */
  onReconnecting?: (on: boolean) => void;
  /** Played to the end. */
  onEnded?: () => void;
  /** The hls.js constructor; globalThis.Hls by default. */
  Hls?: unknown;
  /** Where subtitles that are pictures are drawn; by default a canvas laid over the video. */
  pictureCanvas?: HTMLCanvasElement;
}

export class Player {
  readonly video: HTMLVideoElement;
  readonly titleId: string;
  readonly playback: Playback | null;
  readonly positionMs: number;
  readonly audioTrack?: string;
  /** The subtitles chosen, or null. */
  readonly subtitleTrack: string | null;
  /** The forced subtitles shown (none chosen), or null. */
  readonly forcedTrack: string | null;
  setAudioTrack(id: string): Promise<unknown>;
  /** Plays another copy of the title at the same place (a play of a copy). */
  setVersion(id: string): Promise<Playback | null>;
  /** The decision the playback plays by, or null before it plays. */
  readonly delivery: Decision | null;
  setSubtitleTrack(id: string | null): Promise<unknown>;
  segmentAt(ms?: number): Segment | null;
  continueHere(): Promise<unknown>;
  /** Stops for good; ended: the title counts as watched to its end. */
  close(options?: { ended?: boolean }): Promise<void>;
  /**
   * Starts at startMs: by default the options' startMs, else where the
   * profile left off. Resolves with the playback, or null when close() or
   * a newer start came first.
   */
  start(startMs?: number): Promise<Playback | null>;
  /** Limits the bitrate (kbps; 0: automatic). The stream restarts at the same place. */
  setMaxBitrate(kbps: number | null): Promise<Playback | null>;
  /** The bitrate limit chosen (kbps), or 0 for automatic. */
  readonly maxBitrate: number;
}


/** What a device plays, as deviceEnv() reads it once per page. */
export interface DeviceEnv {
  family: { firefox: boolean; chromium: boolean; safari: boolean; mac: boolean; android: boolean; webos: boolean; mobile: boolean };
  /** What Overfin's app says the screen shows in HDR, or null outside it. */
  app: string[] | null;
  hdrScreen: boolean;
  hls: 'hls.js' | 'native' | '';
  audioTracks: boolean;
  canPlay(type: string): string;
  mse(type: string): boolean;
  decode(config: MediaDecodingConfiguration): Promise<MediaCapabilitiesDecodingInfo | null>;
  /** The QUIRKS that apply. */
  quirks: string[];
  rules: { noFile: string[]; audioNever: string[]; hdrMapped: boolean; hdrFromApp: boolean };
}

export interface Decision {
  copy: Copy;
  audioTrack: string;
  /** '' for MSFP's baseline. */
  delivery: 'file' | 'remux' | 'transcode' | '';
  mediaType?: string;
  transcode?: TranscodeTargets;
  reason: Reason;
  level: number;
  converts: { video: boolean; audio: boolean };
  /** Nothing converted. */
  asIs: boolean;
  quirks: string[];
}

export interface Quirk {
  id: string;
  test(env: DeviceEnv): boolean;
  override: { noFile?: string[]; hdrMapped?: boolean; hdrFromApp?: boolean; audioNever?: string[] };
  date: string;
  found: string;
}

export const QUIRKS: Quirk[];
export const LEVELS: ['file', 'remux', 'audio', 'video', 'baseline'];
export const REASONS: Reason[];
export const BROWSER_TYPES: Record<string, string>;
export function deviceEnv(options?: { Hls?: unknown }): DeviceEnv;
/**
 * Chooses the copy, its audio and its delivery for this device: own copies
 * that play as they are first (the best picture), then a paired server's,
 * then own ones converted; the cheapest delivery that plays.
 */
export function chooseDelivery(copies: Copy[], env: DeviceEnv, wishes?: {
  versionId?: string; audioTrack?: string; audioLanguage?: string; audio?: string; language?: string;
  limits?: Title['limits']; encoders?: Encoders; maxBitrateKbps?: number; peers?: string[]; minLevel?: number;
  memory?: typeof fallbacks | null;
}): Promise<Decision | null>;
export function playRequest(decision: Decision, options?: { titleId: string; startPositionMs?: number; maxBitrateKbps?: number; device?: string; replaces?: string }): Record<string, unknown>;
export function streamUrls(base: string, answer: { ticket: string; protocol?: string }): {
  stream: string; subtitles(id: string): string; pictures(id: string): string; sheet(n: number): string;
};
export function previewSheets(layout: PreviewLayout | undefined, sheet: (n: number) => string): Trickplay | undefined;
export function dolbyVisionCodecs(codecs: string, dv?: CopyVideo['dv']): string;
export function audioMenu(copy: Copy, env?: DeviceEnv): CopyAudio[];
export function subtitleMenu(copy: Copy, served?: { id: string; form: string }[]): { tracks: CopySubtitle[]; forced: CopySubtitle[] };
export function forcedFor(copy: Copy, audioTrack: string, served?: { id: string; form: string }[]): CopySubtitle | null;
export function subtitlesFor(copy: Copy, language?: string, served?: { id: string; form: string }[]): CopySubtitle | null;
export function pictureSubtitle(track: CopySubtitle): boolean;
/** The fallback ladder's memory, on this device (localStorage). */
export const fallbacks: {
  key(copy: Copy, audioTrack: string): string;
  level(copy: Copy, audioTrack: string): number;
  remember(copy: Copy, audioTrack: string, level: number): void;
  count(): number;
  reset(): void;
};
/** Labels from facts, in French or English. */
export function copyLabel(copy: Copy, lang?: string, copies?: Copy[]): string;
export function audioLabel(track: CopyAudio, lang?: string): string;
export function subtitleLabel(track: CopySubtitle, lang?: string): string;
export function languageName(tag: string | undefined, lang?: string): string;
export function codecName(token: string): string;
export function channelsName(n: number | undefined, lang?: string): string;
export function rangeName(video?: CopyVideo): string;

/** A picture of a PGS track, as it comes compressed; pgsPixels() draws it. */
export interface PGSPicture {
  x: number;
  y: number;
  width: number;
  height: number;
  /** The part shown, when the track crops it. */
  crop?: { x: number; y: number; width: number; height: number };
  /** RGBA, 256 entries. */
  palette: Uint8ClampedArray;
  data: Uint8Array;
}

/** What a PGS track shows from startMs to endMs (Infinity: the last). */
export interface PGSCue {
  startMs: number;
  endMs: number;
  /** The screen the pictures are placed on. */
  width: number;
  height: number;
  forced: boolean;
  pictures: PGSPicture[];
}

/**
 * Reads a PGS track into its cues, in order; with clears, the moments the
 * screen is cleared too, as cues with no picture. Throws on a malformed track.
 */
export function decodePGS(bytes: Uint8Array | ArrayBuffer, options?: { clears?: boolean }): PGSCue[];
/** Reads WebVTT as Overfin rebuilds it into cues, in seconds. */
export function parseVTT(vtt: string): { start: number; end: number; text: string }[];
/** A picture's pixels, RGBA (width × height × 4). Throws when they overflow its size. */
export function pgsPixels(picture: PGSPicture): Uint8ClampedArray;
/** The cue showing at a time (ms), or null. */
export function cueAt(cues: PGSCue[], ms: number): PGSCue | null;

/** Draws a PGS track over a video, on a canvas laid over the video's box. */
export class PictureSubtitles {
  constructor(video: HTMLVideoElement, canvas: HTMLCanvasElement);
  readonly cues: PGSCue[];
  readonly url: string;
  /** Shows a track from its URL, its stretches fetched with part(). */
  show(url: string): void;
  /** Fetches one stretch of the track shown, once. */
  part(n: number): void;
  /** Merges a stretch's cues (decodePGS with clears) with those already here. */
  add(cues: PGSCue[]): void;
  /** Stops drawing and clears the canvas. */
  hide(): void;
}

export class Overfin {
  constructor(options?: { base?: string; onLoginRequired?: (loginUrl: string) => void });
  readonly base: string;

  request<T = unknown>(method: string, path: string, body?: unknown, options?: { keepalive?: boolean }): Promise<T>;
  loginUrl(returnTo?: string): string;
  stepUp(error: OverfinError, returnTo?: string): void;

  me(): Promise<Me>;
  createAccount(): Promise<Me>;
  claimAccount(username: string, password: string): Promise<Me>;
  /** Owner profile only: the account's language, among those offered. */
  setLanguage(language: string): Promise<Me>;
  /** Owner profile only, when the operator asks accounts what they like. */
  tastes(): Promise<TastesPage>;
  setTastes(tastes: { prefer?: 'series' | 'movies' | 'both'; genres?: number[] }): Promise<Tastes>;
  /** Migration only: whether the active profile may bring its history. */
  historyCopy(): Promise<{ available: boolean }>;
  copyHistory(username: string, password: string): Promise<void>;
  logout(): Promise<{ logoutUrl?: string }>;
  /** With Overfin's own sign-in: the email, and whether a forgotten password can be reset by email. */
  signinInfo(): Promise<{ email: string; hasPassword: boolean; reset: boolean; rules: { minLength: number; digit: boolean; special: boolean } }>;
  /** With Overfin's own sign-in: the email, and whether it still needs confirming. */
  addressInfo(): Promise<{ email: string; unconfirmed: boolean }>;
  /** Emails the link that confirms the address. */
  confirmAddress(): Promise<{ sent: boolean }>;
  /** Whose brand to show: Overfin's (without `custom`), or the operator's own, with "Powered by" Overfin. No sign-in needed. */
  branding(): Promise<Branding>;
  setPassword(password: string): Promise<{ hasPassword: boolean }>;
  deleteAccount(): Promise<null>;
  exportData(pin?: string): Promise<Record<string, unknown>>;

  profiles(): Promise<Profile[]>;
  profile(id: string): Promise<Profile>;
  createProfile(profile: { name: string; kind?: 'standard' | 'kids'; maxAge?: number; libraries?: string[] | null; blockedTags?: string[] | null; categories?: string[] | null }): Promise<Profile>;
  /**
   * Each profile may change its own `theme` (`''` follows the device); the rest is the owner's.
   * `libraries: null` gives every library again; `blockedTags: null` hides no tag.
   */
  updateProfile(id: string, changes: { name?: string; theme?: '' | 'light' | 'dark'; kind?: 'standard' | 'kids'; maxAge?: number; libraries?: string[] | null; blockedTags?: string[] | null; categories?: string[] | null; kidsTitles?: boolean | null; audioLanguage?: string; subtitleLanguage?: string; pin?: string }): Promise<Profile>;
  deleteProfile(id: string): Promise<null>;
  activateProfile(id: string, pin?: string): Promise<Profile>;
  setPin(id: string, pin: string, ownerPin?: string): Promise<null>;
  removePin(id: string): Promise<null>;
  /** The library's tags, to hide some from a profile (owner only). */
  tags(): Promise<string[]>;
  sessions(): Promise<Session[]>;
  endSession(id: string): Promise<null>;
  endOtherSessions(): Promise<null>;

  libraries(): Promise<Library[]>;
  /** A title's page, in a library or not. An episode also by TMDB's ID of it alone (title_episode_tmdb_1158146): the answer carries its whole ID, for the rest. */
  title(id: string): Promise<Title>;
  /** The titles that fit the profile best, among all or the newest (since 0.6). */
  recommended(scope?: 'all' | 'recent', limit?: number): Promise<TitleSummary[]>;
  /** A page of them, for the whole list (300 at most): nextCursor says whether there are more (since 0.10). */
  recommendedPage(scope?: 'all' | 'recent', options?: { limit?: number; cursor?: string }): Promise<TitlePage>;
  /** Titles like this one: those the server has first, then others when the operator shows them (since 0.6). */
  similar(id: string, limit?: number): Promise<TitleSummary[]>;
  /** The collections of films the profile has films of, without their films (since 0.7). */
  collections(): Promise<Collection[]>;
  /** A collection with its films, in release order (since 0.7). */
  collection(id: string): Promise<Collection>;
  person(id: string): Promise<Person>;
  /**
   * Several GET calls in one request: each a path as the methods use them
   * ('/titles/continue'), with its query or not. Resolves, in order, with
   * each body or its OverfinError (one failing doesn't fail the others).
   * At most 20; images, streams and logins can't be batched.
   */
  batch(calls: Array<string | [string, Record<string, string | number | boolean | string[] | undefined>]>): Promise<Array<any | OverfinError>>;
  /**
   * Titles and people; a title's ID or a link to it (imdb:tt0133093,
   * tmdb:movie/603, tvdb:81189…) finds that title, and byId says what was
   * read (since 0.10).
   */
  search(query: string): Promise<SearchResults>;
  /** A library's titles; kind is required. */
  titles(query?: TitleQuery): Promise<TitlePage>;
  pages(query?: TitleQuery): AsyncGenerator<TitlePage>;
  /** Started titles and each followed series' next episode, a library's or not. */
  continueWatching(limit?: number): Promise<TitleSummary[]>;
  /** What leaves the server soon for the profile, the first to leave first (since 0.10). */
  leaving(): Promise<TitleSummary[]>;
  /** A library's genres (every library's without one), or a category's, for filters. */
  genres(libraryId?: string, categoryId?: string): Promise<Genre[]>;
  /** The operator's categories (since 0.6); libraries: whether the menu shows the libraries. */
  categories(): Promise<{ items: Category[]; libraries: boolean }>;
  /** The titles added last, a library's or every library's. */
  latest(libraryId?: string, limit?: number): Promise<TitleSummary[]>;
  imageUrl(ref: ImageRef | undefined, size?: 'small' | 'medium' | 'large'): string;

  discover(list: 'trending' | 'popular' | 'upcoming', mediaKind: 'movie' | 'series',
    options?: { availability?: 'all' | 'library' | 'missing'; limit?: number; cursor?: string }): Promise<TitlePage>;

  setPlayed(id: string, played?: boolean): Promise<UserState>;
  setFavorite(id: string, favorite?: boolean): Promise<UserState>;
  /** Says the profile isn't interested in a film or a series (its recommendations leave it out), or takes that back (since 0.9). */
  setNotInterested(id: string, notInterested?: boolean): Promise<null>;
  forgetProgress(id: string): Promise<UserState>;
  /** What playing a series plays: the episode in progress (and where), the one after the last finished, or the first. */
  upNext(seriesId: string): Promise<{ title: TitleSummary; positionMs: number }>;
  clearHistory(): Promise<null>;

  watchlist(): Promise<TitleSummary[]>;

  onWatchlist(id: string): Promise<boolean>;
  addToWatchlist(id: string): Promise<null>;
  removeFromWatchlist(id: string): Promise<null>;

  wishlist(): Promise<Wishlist>;
  addToWishlist(id: string): Promise<null>;
  removeFromWishlist(id: string): Promise<null>;
  orderWishlist(ids: string[]): Promise<null>;
  wishesSeen(): Promise<null>;
  profileWishing(profileId: string): Promise<{ on: boolean }>;
  setProfileWishing(profileId: string, on: boolean): Promise<{ on: boolean }>;

  /** MSFP's play request (playRequest() builds it); the answer, MSFP's playback object. */
  startPlayback(request: Record<string, unknown>): Promise<Record<string, unknown>>;
  /** A copy's seek-bar previews before it plays (since 0.10). */
  previews(titleId: string, versionId: string): Promise<{ ticket: string; etag: string }>;
  playback(id: string): Promise<Playback>;
  currentPlayback(): Promise<Playback>;
  reportProgress(id: string, report: { positionMs: number; paused?: boolean; ended?: boolean }): Promise<null>;
  stopPlayback(id: string, positionMs: number, options?: { keepalive?: boolean }): Promise<null>;
  play(video: HTMLVideoElement, titleId: string, options?: PlayerOptions): Promise<Player>;
  /** Every language Overfin names, each in itself (the names tracks have). */
  languages(): Promise<{ languages: { tag: string; name: string }[] }>;
  /** Keeps the language the active profile chose for subtitles ('' for none), for its next playbacks. */
  setSubtitleLanguage(language: string): Promise<null>;
  /** Keeps the audio or subtitles chosen while watching, for the title ('' : none). */
  chooseTracks(playbackId: string, choice: { audioTrack?: string; subtitleTrack?: string }): Promise<null>;
}
