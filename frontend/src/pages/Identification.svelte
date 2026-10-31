<!-- Identification: every film and series with the TMDB title it's taken
     for, those that may be wrong first and why. A title is fixed by picking
     the right one on TMDB, confirmed by keeping its own, or given back to
     Jellyfin's. -->
<script lang="ts">
  import { PageHead, Badge, Notice, Loading } from '@overfin/ui';
  import { api } from '../lib/api';
  import { message } from '../lib/format';
  import { route } from '../lib/router.svelte';
  import IdentificationDetail from '../parts/library/IdentificationDetail.svelte';
  import type { Identified } from '../lib/types-library';

  route.wide = true;

  const reasonNames: Record<string, string> = { 'no-tmdb': 'No TMDB title', 'tmdb-tag': 'Tagged', name: 'Name differs', year: 'Year differs', seasons: 'Seasons differ', runtime: 'Length differs' };
  const reasonName = (code: string) => reasonNames[code] || code;

  let items = $state<Identified[] | null>(null);
  let suspects = $state(0);
  let problem = $state('');
  let search = $state('');
  let filter = $state('suspects');
  let selected = $state('');
  let first = true;

  async function load() {
    try {
      ({ items, suspects } = await api<{ items: Identified[]; suspects: number }>('GET', '/admin/api/identification'));
      if (first && !suspects) filter = '';
      first = false;
    } catch (err) {
      problem = message(err);
    }
  }

  const titleOf = (t: Identified) => (t.tmdbName ? `${t.tmdbName}${t.tmdbYear ? ` (${t.tmdbYear})` : ''}` : t.name);
  const shown = $derived.by(() => {
    const q = search.trim().toLowerCase();
    return (items || []).filter((t) => (filter !== 'suspects' || t.reasons.length) && (filter !== 'fixed' || t.overridden) &&
      (!q || [t.name, t.tmdbName, t.originalName, t.folderName, t.fileName, t.tmdb].some((v) => (v || '').toLowerCase().includes(q))));
  });
  const current = $derived(items?.find((t) => t.itemId === selected));

  load();
</script>

<PageHead title="Identification" description={'Each film and series, as the TMDB title Jellyfin matched it to; those that may be wrong come first, with why. A fix is kept by Overfin: Jellyfin isn\'t changed, and its scans don\'t undo it.'} />

{#if problem}
  <Notice warn>{problem}</Notice>
{:else if !items}
  <Loading />
{:else}
  <div class="split">
    <div>
      <div class="toolbar">
        <input type="search" placeholder="Search titles, folders, files" aria-label="Search" bind:value={search} />
        <select aria-label="Show" bind:value={filter}>
          <option value="suspects">To check ({suspects})</option>
          <option value="">All ({items.length})</option>
          <option value="fixed">Fixed here</option>
        </select>
      </div>
      <div class="table-wrap">
        <table>
          <thead><tr><th>Title</th><th class="hide-narrow">Kind</th><th>Check</th></tr></thead>
          <tbody>
            {#each shown.slice(0, 500) as t (t.itemId)}
              <tr data-id={t.itemId} class:selected={t.itemId === selected} onclick={() => (selected = t.itemId)}>
                <td><div class="name">{titleOf(t)}</div><div class="hint">{t.folderName || t.fileName || ''}</div></td>
                <td class="hide-narrow">{t.kind === 'movie' ? 'Film' : 'Series'}</td>
                <td>
                  {#if t.overridden}<Badge kind="ok">Fixed here</Badge>
                  {:else if t.reasons.length}<Badge kind="warn">{t.reasons.length === 1 ? reasonName(t.reasons[0].code) : `${t.reasons.length} doubts`}</Badge>
                  {:else}<span class="hint nowrap">Looks right</span>{/if}
                </td>
              </tr>
            {:else}
              <tr><td colspan="3" class="empty">{filter === 'suspects' ? 'Nothing to check: every title looks right.' : 'Nothing matches.'}</td></tr>
            {/each}
            {#if shown.length > 500}
              <tr><td colspan="3" class="empty">{shown.length - 500} more: search to narrow.</td></tr>
            {/if}
          </tbody>
        </table>
      </div>
    </div>
    <aside class="detail">
      {#if current}
        {#key current}<IdentificationDetail title={current} reload={load} />{/key}
      {:else}
        <div class="placeholder">Select a title to check it.</div>
      {/if}
    </aside>
  </div>
{/if}
