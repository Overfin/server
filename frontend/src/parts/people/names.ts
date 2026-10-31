// How People names accounts and their states.
import type { Account } from '../../lib/types-people';

export const statusText: Record<string, string> = { active: 'Active', disabled: 'Disabled', expired: 'Expired' };
export const statusKind: Record<string, '' | 'ok' | 'err' | 'warn'> = { active: 'ok', disabled: 'err', expired: 'warn' };
export const lastActive = (a: Account) => a.sessions.map((s) => s.lastActiveAt || '').sort().at(-1) || '';
// nameOf is the best name for an account: the identity provider's, then the
// operator's label, then the owner profile's name.
export const nameOf = (a: Account) => a.person?.name || a.person?.username || a.label || a.profiles.find((p) => p.owner)?.name || 'Unnamed';
export const subline = (a: Account) => [a.person?.username && a.person.name ? a.person.username : '', a.person?.email && a.person.email !== nameOf(a) ? a.person.email : '']
  .filter(Boolean).join(' · ');
