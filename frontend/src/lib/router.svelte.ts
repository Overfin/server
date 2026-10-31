// The panel's pages, by the address's #/name (sign-in comes back to the
// page it left, by the hash). The layout's width is the page's to say.
const aliases: Record<string, string> = { settings: 'server', users: 'people', 'm/invites': 'registration' };

function current(): string {
  const tab = location.hash.replace(/^#\/?/, '') || 'server';
  return aliases[tab] || tab;
}

export const route = $state({ tab: current(), wide: false });

addEventListener('hashchange', () => {
  route.tab = current();
  route.wide = false;
  window.scrollTo(0, 0);
});
