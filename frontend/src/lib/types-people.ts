// What People, Registration and Features & limits get from the API.

export interface Profile {
  id: string;
  name: string;
  owner?: boolean;
  kind?: string;
  maxAge?: number;
  hasPin?: boolean;
  pinLocked?: boolean;
}

export interface Session {
  id: string;
  device?: string;
  profile?: string;
  lastActiveAt?: string;
}

export interface Account {
  id: string;
  label?: string;
  person?: { name?: string; username?: string; email?: string; active?: boolean };
  profiles: Profile[];
  sessions: Session[];
  status: string;
  template: { id: string; name: string };
  subject?: string;
  createdAt?: string;
  expiresAt?: string;
  historyCopiedAt?: string;
}

/** Person: someone who signs in here (Overfin's own sign-in). */
export interface Person {
  id: string;
  email: string;
  accountId?: string;
  admin?: boolean;
  you?: boolean;
  disabled?: boolean;
  confirmed?: boolean;
  mustChange?: boolean;
}

export interface Registration {
  rules: { minLength: number; digit: boolean; special: boolean };
  mail: boolean;
}

export interface PluginSetting {
  key: string;
  label: string;
  hint?: string;
  text?: boolean;
  choices?: { value: string; label: string }[];
}

export interface Plugin {
  name: string;
  description?: string;
  needs?: string[];
  settings?: PluginSetting[];
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type FeatureSettings = Record<string, any>;

export interface FeaturesState {
  features: FeatureSettings;
  plugins: Plugin[];
  exposableFields: string[];
}
