<!-- Where the library's index stands: what it counts, and what can't be
     matched yet. -->
<script lang="ts">
  import { Card, Badge, Stats } from '@overfin/ui';
  import { ago } from '../../lib/format';
  import type { Catalog } from '../../lib/types-server';

  let { c }: { c: Catalog } = $props();

  const t = $derived(c.titles || {});
  const n = (v?: number) => (v || 0).toLocaleString();
  const first = $derived(!c.fullAt || c.fullAt.startsWith('0001'));
  const parts = $derived.by(() => {
    const u = c.unmatched || {};
    return ([
      [u.movie, 'films', 'without a TMDB ID'], [u.series, 'series', 'without a TMDB ID'],
      [u.season, 'seasons', 'without their number'], [u.episode, 'episodes', 'without their season or episode number'],
    ] as [number | undefined, string, string][]).filter(([k]) => (k || 0) > 0).map(([k, label, why]) => `${k} ${label} ${why}`);
  });
  const stats = $derived([['movie', 'Films'], ['series', 'Series'], ['season', 'Seasons'], ['episode', 'Episodes']]
    .map(([k, label]) => ({ value: n(t[k]), label })));
</script>

<Card title="Library index" description={first ? 'The library is being read for the first time.' : `Read in full ${ago(c.fullAt)}; changes read ${ago(c.changesAt)}.`}>
  {#snippet badge()}
    {#if c.error}<Badge kind="err">Last read failed</Badge>{:else if first}<Badge kind="warn">First read</Badge>{:else}<Badge kind="ok">Up to date</Badge>{/if}
  {/snippet}
  <Stats items={stats} />
  {#if parts.length}
    <p class="hint">Can't be matched yet: {parts.join('; ')}. Refreshing their metadata in Jellyfin usually fixes it; Metadata lists them.</p>
  {/if}
  {#if c.error}<p class="error">{c.error}</p>{/if}
</Card>
