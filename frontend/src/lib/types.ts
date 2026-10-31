// What the control panel's API answers, as far as the pages use it. Pages
// may add the types they alone use here.

export interface Category {
  id: string;
  names: Record<string, string>;
  rules: CategoryRules;
  count: number;
  home: boolean;
}

export interface CategoryRules {
  kinds?: string[];
  genres?: number[];
  languages?: string[];
  libraries?: string[];
  tags?: string[];
  maxAge?: number | null;
  except?: string[];
}

export interface Categories {
  items: Category[];
  defaultLanguage: string;
  languages: string[];
  genres: { id: number; name: string }[];
  originalLanguages: string[];
  libraries: { id: string; name: string }[];
  tags: string[];
  librariesShown: boolean;
}

export interface Template {
  id: string;
  name: string;
  maxStreams: number;
  maxProfiles: number;
  reserveStreams: boolean;
  categories?: string[] | null;
  accounts: number;
  default?: boolean;
}
