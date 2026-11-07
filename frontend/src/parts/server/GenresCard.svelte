<!-- Genres left out: titles in them that no library has are hidden
     everywhere on the site; those a library has still show. Chosen among
     TMDB's genres, for films and for series, in a window. -->
<script lang="ts">
  import { Card, Setting, Notice, Loading, FormModal, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { message } from '../../lib/format';
  import type { GenresLeftOut } from '../../lib/types-server';

  let data = $state<GenresLeftOut | null>(null);
  let problem = $state('');
  let editing = $state<GenresLeftOut | null>(null);

  api<GenresLeftOut>('GET', '/admin/api/genres-left-out').then((d) => (data = d), (err) => (problem = message(err)));

  const kinds = [['movie', 'Films'], ['series', 'Series']] as const;
  const named = (d: GenresLeftOut, kind: 'movie' | 'series') =>
    d[kind].filter((g) => g.left).map((g) => g.name).join(', ') || 'None';

  async function save() {
    const e = editing!;
    data = await api<GenresLeftOut>('PUT', '/admin/api/genres-left-out', {
      movie: e.movie.filter((g) => g.left).map((g) => g.id),
      series: e.series.filter((g) => g.left).map((g) => g.id),
    });
    toast('Genres left out saved.');
  }
</script>

<Card title="Genres left out" description="Titles in these genres that no library has are hidden everywhere: search, discovery, people's pages, collections, wishlists and Overfin's suggestions. A title a library has always shows.">
  {#if problem}
    <Notice warn>{problem}</Notice>
  {:else if !data}
    <Loading />
  {:else}
    {#each kinds as [kind, label] (kind)}
      <Setting {label} hint="">
        {#snippet children()}<span>{named(data!, kind)}</span>{/snippet}
      </Setting>
    {/each}
  {/if}
  {#snippet foot()}
    <button type="button" disabled={!data} onclick={() => (editing = structuredClone($state.snapshot(data)))}>Edit</button>
  {/snippet}
</Card>

{#if editing}
  <FormModal title="Genres left out" description="A title no library has is hidden when it's in any of the genres ticked, as TMDB lists its genres." wide {save} close={() => (editing = null)}>
    {#each kinds as [kind, label] (kind)}
      <section>
        <h3>{label}</h3>
        <div class="checks">
          {#each editing[kind] as g (g.id)}
            <label class="inline"><input type="checkbox" bind:checked={g.left} aria-label={`${label}: ${g.name}`} /> {g.name}</label>
          {/each}
        </div>
      </section>
    {/each}
  </FormModal>
{/if}

