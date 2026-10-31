// What the Server page's API calls answer, as far as it uses them.

export interface JellyfinAccess {
  open?: boolean;
  since?: string;
}

export interface Jellyfin {
  url: string;
  inFile?: boolean;
  access?: JellyfinAccess;
}

export interface GpuFamily {
  id: string;
  name: string;
  vendor?: string;
  examples?: string;
  device?: boolean;
  decodes?: string[];
  encodes?: string[];
  note?: string;
}

export interface Transcoding {
  families: GpuFamily[];
  family?: string;
  device?: string;
  defaultDevice: string;
}

export interface TranscodingTest {
  running?: boolean;
  total?: number;
  error?: string;
  results?: { kind: string; title: string; ok: boolean; speed: number; firstPicture: number; reason?: string }[];
}

export interface Previews {
  on: boolean;
}

export interface Mail {
  inFile?: boolean;
  host?: string;
  port?: number;
  tls?: string;
  from?: string;
  username?: string;
  hasPassword?: boolean;
}

export interface Catalog {
  titles?: Record<string, number>;
  unmatched?: Record<string, number>;
  fullAt?: string;
  changesAt?: string;
  error?: string;
}

export interface Seerr {
  url: string;
}

export interface Ratings {
  token: { set: boolean; inEnv: boolean };
  source: boolean;
  country: string;
  countries: string[];
  shown: string;
  fetched: number;
  total: number;
}

export interface Languages {
  choices: { tag: string; name: string }[];
  offered: string[];
  default: string;
}

export interface IntroSkipper {
  version: string;
  status: { installed: boolean; version?: string; state?: string };
  progress: { running: boolean; step?: string; error?: string };
}

export interface MailTemplates {
  dir: string;
  emails: { name: string; html?: string; text?: string }[];
}

export type Line = { text: string; kind: '' | 'ok' | 'error' };
