<!-- Branding: the operator's own name, logo and banners, instead of
     Overfin's, wherever people look (the control panel stays Overfin's),
     and the site's home page. -->
<script lang="ts">
  import { PageHead, Card, Setting, Badge, Status, Busy, Notice, Loading, toast } from '@overfin/ui';
  import { api } from '../lib/api';
  import { message } from '../lib/format';
  import BrandingFile from '../parts/library/BrandingFile.svelte';
  import type { Branding, Home } from '../lib/types-library';

  // The files branding may have: what each is for.
  const kinds = [
    ['logo', 'Logo', 'Square: the icon in browser tabs. Without one, Overfin\'s.'],
    ['banner-dark', 'Banner', 'Your logo and your name as one picture, with a transparent background, for dark pages: about 4:1, at least 64 pixels tall. It\'s shown as tall as the header\'s logo, never wider than a few centimeters. Without one, your name shows as text.'],
    ['banner-light', 'Banner for light pages', 'Optional: for the sign-in page on light screens, when the banner doesn\'t suit them (the site\'s header is dark on every page). Without one, the banner shows there too.'],
  ];

  let b = $state<Branding | null>(null);
  let home = $state<Home | null>(null);
  let problem = $state('');
  let on = $state(false);
  let name = $state('');
  let missing = $state(false);
  let failure = $state('');
  let homeFailure = $state('');

  function settle(next: Branding) {
    b = next;
    on = next.on;
    name = next.name;
  }

  Promise.all([api<Branding>('GET', '/admin/api/branding'), api<Home>('GET', '/admin/api/home')]).then(
    ([br, h]) => {
      settle(br);
      home = h;
      missing = h.missingTitles;
    },
    (err) => (problem = message(err)),
  );

  async function save() {
    failure = '';
    if (on === b!.on && name.trim() === b!.name) {
      toast('Nothing to save.');
      return;
    }
    const wasOn = b!.on;
    settle(await api<Branding>('PUT', '/admin/api/branding', { on, name }));
    // Say it was switched only when it was.
    toast(b!.on === wasOn ? 'Saved.' : b!.on ? 'Your branding is on.' : 'Overfin\'s branding is back.');
  }

  async function saveHome() {
    homeFailure = '';
    home = await api<Home>('PUT', '/admin/api/home', { missingTitles: missing });
    toast('Home page saved.');
  }

  // The site's header (dark on every page), and the sign-in page on a
  // light screen, as people will see them.
  const banner = (light: boolean) => (light ? (b?.files['banner-light'] || b?.files['banner-dark']) : b?.files['banner-dark']);
</script>

<PageHead title="Branding" description="Your site's name, logo and banner, instead of Overfin's, and its home page." />

{#if problem}
  <Notice warn>{problem}</Notice>
{:else if !b || !home}
  <Loading />
{:else}
  <div class="cards">
    <Card title="Your branding" description={'On, your site shows your name, logo and banner instead of Overfin\'s: the site, its sign-in page, its emails. It then says "Powered by Overfin", discreetly. The control panel stays Overfin\'s.'}>
      {#snippet badge()}{#if b!.on}<Badge kind="ok">On</Badge>{:else}<Badge kind="plain">Overfin's</Badge>{/if}{/snippet}
      <Setting label="Use your own branding" hint="Off, everything shows Overfin's, and your files stay here unused.">
        {#snippet children(ids)}<input type="checkbox" bind:checked={on} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
      <Setting label="Name" hint="Your site's name: in browser tabs, and as the sender of its emails.">
        {#snippet children(ids)}<input autocomplete="off" spellcheck="false" maxlength="40" bind:value={name} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
      {#snippet foot()}
        <Status text={failure} kind={failure ? 'error' : ''} />
        <Busy kind="primary" action={save} fail={(err) => (failure = err.message)}>Save</Busy>
      {/snippet}
    </Card>

    <Card title="Home page" description="What your site's home page shows besides the libraries.">
      <Setting label="Titles no library has" hint="Also show them among what's trending and in the slideshow, to be requested (which ones: Server, TMDB, Titles shown). Off: the home page shows the libraries only.">
        {#snippet children(ids)}<input type="checkbox" bind:checked={missing} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
      {#snippet foot()}
        <Status text={homeFailure} kind={homeFailure ? 'error' : ''} />
        <Busy kind="primary" action={saveHome} fail={(err) => (homeFailure = err.message)}>Save</Busy>
      {/snippet}
    </Card>

    <Card title="Files" description={'Kept as you upload them: PNG works everywhere, emails included (JPEG too; not SVG, which emails can\'t show). 4 MB at most each.'}>
      {#each kinds as [kind, label, hint] (kind)}
        <BrandingFile {kind} {label} {hint} branding={b} changed={settle} />
      {/each}
    </Card>

    <Card title="Preview" description="The top of your site (dark on every page), then the sign-in page on a light screen.">
      {#each [false, true] as light (light)}
        {@const src = banner(light)}
        <div class="brand-preview header" class:dark={!light} class:light>
          {#if src}<img {src} alt={b.name} />{:else}<strong>{b.name || 'Your name'}</strong>{/if}
        </div>
      {/each}
    </Card>
  </div>
{/if}
