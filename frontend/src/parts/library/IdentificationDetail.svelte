<!-- The selected title: its files, the TMDB title it's taken for, why it
     may be wrong, and TMDB searched for the right one. -->
<script lang="ts">
  import { Badge, Busy, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { message } from '../../lib/format';
  import type { Candidate, Identified } from '../../lib/types-library';

  let { title: t, reload }: { title: Identified; reload: () => Promise<void> } = $props();

  const facts = $derived([
    t.folderName && ['Folder', t.folderName],
    t.fileName && ['File', t.fileName],
    t.runtimeMin && ['Length', `${t.runtimeMin} min`],
    t.episodes && ['Episodes here', String(t.episodes)],
    ['TMDB', t.tmdb ? `${t.tmdb}${t.overridden && t.jellyfinTmdb !== t.tmdb ? ` (Jellyfin said ${t.jellyfinTmdb || 'none'})` : ''}` : 'none'],
    t.originalName && ['Original title', `${t.originalName}${t.language ? ` (${t.language})` : ''}`],
  ].filter(Boolean) as [string, string][]);

  // TMDB, searched from the folder's name by default.
  const guess = (x: Identified) => (x.folderName || x.fileName || x.name).replace(/\[[^\]]*\]|\{[^}]*\}/g, '').replace(/\.[a-z0-9]{2,4}$/i, '').replace(/[._]/g, ' ')
    .replace(/^(.+?)\s*\(?\b(?:19|20)\d{2}\b\)?\s*$/, '$1').trim();
  // The search starts from the title shown; the detail is made again for each one.
  // svelte-ignore state_referenced_locally
  let query = $state(guess(t));
  let searching = $state(false);
  let results = $state<Candidate[] | null>(null);
  let failure = $state('');

  async function act(method: string, body: unknown, done: string) {
    await api(method, `/admin/api/identification/${t.itemId}`, body);
    toast(done);
    await reload();
  }

  async function find(e?: SubmitEvent) {
    e?.preventDefault();
    searching = true;
    failure = '';
    results = null;
    try {
      ({ items: results } = await api<{ items: Candidate[] }>('POST', '/admin/api/identification/search', { query, kind: t.kind }));
    } catch (err) {
      failure = message(err);
    } finally {
      searching = false;
    }
  }
</script>

<div class="detail-head"><div>
  <h2>{t.tmdbName ? `${t.tmdbName}${t.tmdbYear ? ` (${t.tmdbYear})` : ''}` : 'No TMDB title'}</h2>
  <div class="hint">{t.kind === 'movie' ? 'Film' : 'Series'}</div>
</div></div>
{#if t.overridden}<Badge kind="ok">Fixed here</Badge>{/if}
<h3>About</h3>
<dl class="facts">
  {#each facts as [k, v] (k)}<dt>{k}</dt><dd>{v}</dd>{/each}
</dl>
{#if t.reasons.length > 0}
  <div class="notice warn spaced"><strong>Why it may be wrong</strong>
    <ul>{#each t.reasons as r, i (i)}<li>{r.text}</li>{/each}</ul>
  </div>
{/if}
{#if (t.reasons.length && t.tmdb) || t.overridden}
  <div class="actions-row">
    {#if t.reasons.length && t.tmdb}<Busy action={() => act('PUT', { tmdb: t.tmdb }, 'Kept as it is.')}>It's right</Busy>{/if}
    {#if t.overridden}<Busy action={() => act('DELETE', undefined, 'Back to Jellyfin\'s title.')}>Back to Jellyfin's</Busy>{/if}
  </div>
{/if}
<h3>Find the right one on TMDB</h3>
<form class="toolbar" onsubmit={find}>
  <input type="search" bind:value={query} aria-label="Search TMDB" />
  <button type="submit" disabled={searching}>Search</button>
</form>
<ul class="list candidates">
  {#if searching}
    <li class="hint">Searching…</li>
  {:else if failure}
    <li class="error">{failure}</li>
  {:else if results}
    {#each results as c (c.tmdb)}
      <li>
        {#if c.poster}<img src={c.poster} alt="" loading="lazy" />{:else}<span class="noposter"></span>{/if}
        <div>
          <div class="name">{`${c.name}${c.year ? ` (${c.year})` : ''}`}</div>
          <div class="hint">{[c.originalName && c.originalName !== c.name ? c.originalName : '', c.language, `TMDB ${c.tmdb}`].filter(Boolean).join(' · ')}</div>
          {#if c.overview}<div class="hint clamp">{c.overview}</div>{/if}
        </div>
        {#if c.tmdb === t.tmdb}<Badge kind="info">Current</Badge>{:else}<Busy small action={() => act('PUT', { tmdb: c.tmdb }, 'Title fixed.')}>Use this</Busy>{/if}
      </li>
    {:else}
      <li class="hint">TMDB found nothing.</li>
    {/each}
  {/if}
</ul>
