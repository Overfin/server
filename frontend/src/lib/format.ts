// Words and numbers as the panel writes them.
export { ago, day, when } from '@overfin/ui';

/** plural: "1 stream", "3 streams". */
export const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** toLocalInput turns an RFC 3339 time into a datetime-local value. */
export function toLocalInput(iso?: string): string {
  if (!iso) return '';
  const d = new Date(iso);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** numberIn reads a number field. Empty, or not a number, it refuses with
 * a message naming the field, so nothing is saved as 0 or "no limit" by
 * mistake; zero, when given, says what 0 means there. */
export function numberIn(value: unknown, label: string, zero?: string): number {
  const v = String(value ?? '').trim();
  if (v === '' || !Number.isFinite(Number(v))) throw new Error(`${label}: enter a number${zero ? ` (${zero})` : ''}.`);
  return Number(v);
}

/** clock is a position: "4:05", "1:02:07". */
export function clock(ms: number): string {
  const s = Math.floor(ms / 1000), h = Math.floor(s / 3600), m = Math.floor(s / 60) % 60;
  return `${h ? `${h}:${String(m).padStart(2, '0')}` : m}:${String(s % 60).padStart(2, '0')}`;
}

/** message is an error's text. */
export const message = (err: unknown) => (err instanceof Error ? err.message : String(err));

const featureNames: Record<string, string> = { library: 'Library', playback: 'Playback', watchlist: 'Watchlist', invites: 'Invitations', migration: 'Migration' };
export const featureName = (n: string) => featureNames[n] || n.charAt(0).toUpperCase() + n.slice(1);

/** languageName is a language tag in words: "fr-CA" is "Canadian French". */
export function languageName(tag: string): string {
  try {
    return new Intl.DisplayNames(['en'], { type: 'language' }).of(tag) || tag;
  } catch {
    return tag;
  }
}
