<!-- Libraries: those of Overfin's own Jellyfin, with their folders as
     Jellyfin sees them, and a scan on demand. -->
<script lang="ts">
  import { PageHead, Card, Badge, Busy, Notice, Loading, ask, toast } from '@overfin/ui';
  import { api } from '../lib/api';
  import { message } from '../lib/format';
  import { icons } from '../lib/icons';
  import { route } from '../lib/router.svelte';
  import Icon from '../parts/Icon.svelte';
  import LibraryForm from '../parts/library/LibraryForm.svelte';
  import type { Library } from '../lib/types-library';

  const kindNames: Record<string, string> = { films: 'Films', series: 'Series' };

  let items = $state<Library[] | null>(null);
  let problem = $state('');
  // editing: the library whose form is open; null for a new one.
  let editing = $state<Library | null | undefined>(undefined);
  let timer: ReturnType<typeof setTimeout> | undefined;

  async function load() {
    try {
      ({ items } = await api<{ items: Library[] }>('GET', '/admin/api/libraries'));
    } catch (err) {
      problem = message(err);
      return;
    }
    // While Jellyfin scans, its progress, unless a form is open.
    clearTimeout(timer);
    if (items.some((l) => l.scanning)) {
      timer = setTimeout(() => {
        if (route.tab === 'libraries' && editing === undefined && !document.querySelector('dialog[open]')) load();
      }, 3000);
    }
  }

  $effect(() => () => clearTimeout(timer));

  async function scan() {
    await api('POST', '/admin/api/libraries/scan', {});
    toast('Jellyfin is reading every folder again.');
    setTimeout(load, 1500);
  }

  async function remove(lib: Library) {
    if (!(await ask(`Remove the library ${lib.name}? Its files stay where they are; Overfin and its apps stop showing them.`, { yes: 'Remove', danger: true }))) return;
    await api('DELETE', `/admin/api/libraries/${encodeURIComponent(lib.name)}`);
    toast(`${lib.name} removed.`);
    load();
  }

  load();
</script>

<PageHead title="Libraries" description="Where your films and series are, folder by folder, as Jellyfin sees them. New files show up by themselves; a scan reads everything again.">
  {#snippet actions()}
    <Busy action={scan}>Scan now</Busy>
    <button class="primary" type="button" onclick={() => (editing = null)}>Add a library</button>
  {/snippet}
</PageHead>

{#if problem}
  <Notice warn>{problem}</Notice>
{:else if !items}
  <Loading />
{:else if items.length}
  <div class="cards">
    {#each items as lib (lib.name)}
      <Card title={lib.name}>
        {#snippet badge()}
          {#if lib.scanning}<Badge kind="info">Scanning {Math.round(lib.progress || 0)}%</Badge>{:else}<Badge kind="plain">{kindNames[lib.kind] || lib.kind}</Badge>{/if}
        {/snippet}
        <ul class="list">
          {#each lib.folders as f (f)}<li><Icon paths={icons.folder} /><div class="mono">{f}</div></li>{/each}
        </ul>
        <div class="actions-row">
          <button type="button" class="small" onclick={() => (editing = lib)}>Edit</button>
          <Busy kind="danger" small action={() => remove(lib)}>Remove</Busy>
        </div>
      </Card>
    {/each}
  </div>
{:else}
  <p class="notice">No library yet. Add one with the folders where your films or series are.</p>
{/if}

{#if editing !== undefined}
  <LibraryForm lib={editing} saved={load} close={() => (editing = undefined)} />
{/if}
