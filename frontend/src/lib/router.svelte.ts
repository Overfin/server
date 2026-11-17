// The panel's pages, by the address's #/name (sign-in comes back to the
// page it left, by the hash). The layout's width is the page's to say.
import { ask } from '@overfin/ui';

const aliases: Record<string, string> = { settings: 'server', users: 'people', 'm/invites': 'registration', identification: 'metadata' };

function current(): string {
  const tab = location.hash.replace(/^#\/?/, '') || 'server';
  return aliases[tab] || tab;
}

export const route = $state({ tab: current(), wide: false });

// Unsaved changes: a page (or a card) with a form says whether it has
// some; leaving the page, signing out or closing the tab then asks first.
const guards = new Set<() => boolean>();

/** unsaved registers dirty, which says whether there are unsaved changes;
 *  the function it returns takes it back (an $effect's cleanup). */
export function unsaved(dirty: () => boolean): () => void {
  guards.add(dirty);
  return () => guards.delete(dirty);
}

const anyUnsaved = () => [...guards].some((dirty) => dirty());

/** mayLeave asks, when there are unsaved changes, whether to leave them. */
export async function mayLeave(): Promise<boolean> {
  if (!anyUnsaved()) return true;
  return ask('Leave without saving? Your changes on this page are lost.', { title: 'Unsaved changes', yes: 'Leave', danger: true });
}

/** leaveFreely forgets the unsaved changes: the page itself goes elsewhere
 *  and keeps them (a sign-in for a change that needs one). */
export function leaveFreely() {
  guards.clear();
}

let shown = location.hash;

addEventListener('hashchange', async () => {
  const to = location.hash;
  if (to === shown) return;
  if (anyUnsaved()) {
    // The address goes back to the page while the question is open.
    history.replaceState(history.state, '', shown || location.pathname + location.search);
    if (!(await mayLeave())) return;
    guards.clear();
    history.replaceState(history.state, '', to);
  }
  shown = to;
  route.tab = current();
  route.wide = false;
  window.scrollTo(0, 0);
});

addEventListener('beforeunload', (e) => {
  if (anyUnsaved()) e.preventDefault();
});
