<!-- Browses folders as Jellyfin sees them, and picks one. -->
<script lang="ts">
  import { toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { message } from '../../lib/format';
  import { icons } from '../../lib/icons';
  import Icon from '../Icon.svelte';
  import type { Folders } from '../../lib/types-library';

  let { pick, close }: { pick: (path: string) => void; close: () => void } = $props();

  let typed = $state('');
  let shown = $state<Folders | null>(null);

  async function open(path: string) {
    try {
      const d = await api<Folders>('GET', `/admin/api/folders?path=${encodeURIComponent(path)}`);
      typed = d.path;
      shown = d;
    } catch (err) {
      toast(message(err), true);
    }
  }

  open('');
</script>

<div class="box">
  {#if shown}
    {@const d = shown}
    <div class="row">
      <input class="mono" placeholder="/media/films" autocomplete="off" spellcheck="false" bind:value={typed}
        onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); open(typed); } }} />
      <button type="button" onclick={() => open(typed)}>Go</button>
      <button type="button" class="primary" disabled={!d.path} onclick={() => pick(d.path)}>Choose this folder</button>
      <button type="button" class="ghost" onclick={close}>Cancel</button>
    </div>
    <ul class="list">
      {#if d.parent !== ''}
        <li><a href="#up" onclick={(e) => { e.preventDefault(); open(d.parent); }}>↑ Up</a></li>
      {/if}
      <!-- At the top, Jellyfin's drives and mounts ("/", "/media"): whole paths. -->
      {#each d.folders as f (f)}
        <li><Icon paths={icons.folder} /><a href="#open" class="mono" onclick={(e) => { e.preventDefault(); open(f); }}>{d.path ? f.split('/').pop() : f}</a></li>
      {/each}
      {#if !d.folders.length}<li><div class="hint">No folders in here.</div></li>{/if}
    </ul>
  {/if}
</div>
