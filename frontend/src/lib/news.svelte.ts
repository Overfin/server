// What happened while the panel is open: invitations used and accounts
// made, asked every few seconds in the background (it doesn't keep a
// forgotten tab signed in). Each is said in the corner, on any page, and
// the lists that show them reload: they watch news.changed.

import { toast } from '@overfin/ui';
import { api } from './api';

export const news = $state({ changed: 0 });

type Accounts = { now: number; accounts: { id: string; invitation?: string }[] };
type Uses = { items: { id: string; invitation: string; template: string }[] };

let since = 0;
let timer: ReturnType<typeof setTimeout> | undefined;
const seen = new Set<string>();

const named = (name: string) => (name ? `“${name}”` : 'without a name');

async function ask() {
  clearTimeout(timer);
  let told = false;
  try {
    const got = await api<Accounts>('GET', since ? `/admin/api/news?since=${since}` : '/admin/api/news', undefined, true);
    if (since) {
      // Registrations first: their accounts come later.
      const uses = await api<Uses>('GET', `/admin/api/modules/invites/uses?since=${since}`, undefined, true).catch(() => ({ items: [] }));
      for (const u of uses.items) {
        if (seen.has(`u${u.id}`)) continue;
        seen.add(`u${u.id}`);
        toast(`Invitation ${named(u.invitation)} was used${u.template ? ` (${u.template})` : ''}.`);
        told = true;
      }
      for (const a of got.accounts) {
        if (seen.has(`a${a.id}`)) continue;
        seen.add(`a${a.id}`);
        toast(a.invitation ? `An account was created from invitation ${named(a.invitation)}.` : 'A new account joined.');
        told = true;
      }
    }
    // A few seconds back from the server's time: what lands meanwhile is
    // asked again, and said once.
    since = got.now - 5;
  } catch {
    // The network, or the session's end (api handles that): next time.
  }
  if (told) news.changed++;
  timer = setTimeout(ask, document.hidden ? 60000 : 10000);
}

/** watchNews starts asking; once is enough, whatever page is open. */
export function watchNews() {
  if (!timer && !since) ask();
}
