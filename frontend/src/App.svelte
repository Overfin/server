<!-- Overfin's control panel: the sidebar's menu, and the page the address
     names (#/server, #/people…). -->
<script lang="ts">
  import { Shell, Dialogs, Toasts, Loading, Notice } from '@overfin/ui';
  import type { NavGroup } from '@overfin/ui';
  import { api } from './lib/api';
  import { message } from './lib/format';
  import { icons } from './lib/icons';
  import { route } from './lib/router.svelte';
  import Server from './pages/Server.svelte';
  import Features from './pages/Features.svelte';
  import Branding from './pages/Branding.svelte';
  import People from './pages/People.svelte';
  import Registration from './pages/Registration.svelte';
  import Templates from './pages/Templates.svelte';
  import Notices from './pages/Notices.svelte';
  import Playing from './pages/Playing.svelte';
  import Libraries from './pages/Libraries.svelte';
  import Categories from './pages/Categories.svelte';
  import Identification from './pages/Identification.svelte';
  import Valuation from './pages/Valuation.svelte';
  import Apps from './pages/Apps.svelte';

  const pages = {
    server: { label: 'Server', icon: icons.server, page: Server },
    features: { label: 'Features & limits', icon: icons.sliders, page: Features },
    branding: { label: 'Branding', icon: icons.palette, page: Branding },
    people: { label: 'People', icon: icons.people, page: People },
    registration: { label: 'Registration', icon: icons.mail, page: Registration },
    templates: { label: 'Templates', icon: icons.layers, page: Templates },
    notices: { label: 'Notices', icon: icons.bell, page: Notices },
    playing: { label: 'Now playing', icon: icons.play, page: Playing },
    libraries: { label: 'Libraries', icon: icons.folder, page: Libraries },
    categories: { label: 'Categories', icon: icons.grid, page: Categories },
    identification: { label: 'Identification', icon: icons.check, page: Identification },
    valuation: { label: 'Valuation', icon: icons.scale, page: Valuation },
    apps: { label: 'Apps', icon: icons.key, page: Apps },
  };
  type Tab = keyof typeof pages;
  const group = (label: string, tabs: Tab[]) => ({ label, items: tabs.map((t) => ({ href: `#/${t}`, label: pages[t].label, icon: pages[t].icon })) });
  const nav: NavGroup[] = [
    group('Server', ['server', 'features', 'branding']),
    group('People', ['people', 'registration', 'templates', 'notices', 'playing']),
    group('Library', ['libraries', 'categories', 'identification', 'valuation', 'apps']),
  ];

  let who = $state<string | null>(null);
  let problem = $state('');
  let signedOut = $state(false);

  const tab = $derived<Tab>(route.tab in pages ? (route.tab as Tab) : 'server');
  const Page = $derived(pages[tab].page);

  api<{ name?: string }>('GET', '/admin/api/session').then(
    (s) => (who = s.name ? `Signed in as ${s.name}` : 'Signed in'),
    (err) => (problem = message(err)),
  );

  async function signOut() {
    await api('POST', '/admin/api/logout', {}).catch(() => {});
    signedOut = true;
  }
</script>

{#if signedOut}
  <main class="single">
    <div class="center">
      <h1>Signed out</h1>
      <p class="muted">You left the control panel.</p>
      <p><a class="button" href="/admin/login?return=/admin/">Sign in again</a></p>
    </div>
  </main>
{:else if problem}
  <main class="single"><Notice warn>{problem}</Notice></main>
{:else if who === null}
  <main class="single"><Loading /></main>
{:else}
  <Shell name="Overfin" subtitle="Control panel" logo="/admin/logo.svg" {nav} active={`#/${tab}`} {who} {signOut} wide={route.wide}>
    {#key tab}<Page />{/key}
  </Shell>
{/if}

<Dialogs />
<Toasts />
