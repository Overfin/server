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
