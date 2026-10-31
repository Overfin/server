// What the library side of the panel's API answers: libraries, branding,
// identification, valuation.

export interface Library {
  name: string;
  kind: string;
  folders: string[];
  scanning?: boolean;
  progress?: number;
}

export interface Folders {
  path: string;
  parent: string;
  folders: string[];
}

export interface Branding {
  on: boolean;
  name: string;
  files: Record<string, string | undefined>;
}

export interface Home {
  missingTitles: boolean;
}

export interface Reason {
  code: string;
  text: string;
}

export interface Identified {
  itemId: string;
  kind: string;
  name: string;
  tmdbName?: string;
  tmdbYear?: number;
  originalName?: string;
  language?: string;
  folderName?: string;
  fileName?: string;
  tmdb?: string;
  jellyfinTmdb?: string;
  overridden?: boolean;
  edited?: boolean;
  runtimeMin?: number;
  episodes?: number;
  reasons: Reason[];
}

export interface Candidate {
  tmdb: string;
  name: string;
  year?: number;
  originalName?: string;
  language?: string;
  overview?: string;
  poster?: string;
}

export interface Valued {
  id: string;
  name?: string;
  season?: number;
  score: number;
  inLibrary: boolean;
  parts: Record<string, number>;
}

export interface Valuation {
  criteria: { name: string; weight: number }[];
  max: number;
  askTastes: boolean;
  discoveryShare: number;
  accounts: number;
  titles: number;
}

// The metadata editor: a title's texts and artwork in each language.
export interface MetadataText {
  language: string;
  default: boolean;
  name: string;
  overview: string;
  tmdbName: string;
  tmdbOverview: string;
  nameEdited: boolean;
  overviewEdited: boolean;
  untranslated: boolean;
}

export type ImageType = 'poster' | 'backdrop' | 'logo';

export interface MetadataImage {
  url?: string;
  edited: '' | 'language' | 'all';
}

export interface MetadataArtwork {
  language: string; // a regional tag, or '*' for every language
  images: Record<ImageType, MetadataImage>;
}

export interface Metadata {
  kind: string;
  tmdb: string;
  name: string;
  year?: number;
  texts: MetadataText[];
  artwork: MetadataArtwork[];
}

export interface ImageChoice {
  value: string;
  url: string;
  language?: string;
  width?: number;
  height?: number;
}
