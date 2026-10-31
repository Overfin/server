<!-- Adds a library (lib null) or changes one, in a window: its name, its
     kind when it's new, and its folders, picked as Jellyfin sees them. -->
<script lang="ts">
  import { FormModal, Field, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { icons } from '../../lib/icons';
  import Icon from '../Icon.svelte';
  import FolderPicker from './FolderPicker.svelte';
  import type { Library } from '../../lib/types-library';

  let { lib, saved, close }: { lib: Library | null; saved: () => void; close: () => void } = $props();

  // The form starts from the library; it's made again for each one.
  // svelte-ignore state_referenced_locally
  let name = $state(lib?.name || '');
  let kind = $state('films');
  // svelte-ignore state_referenced_locally
  let folders = $state<string[]>([...(lib?.folders || [])]);
  let picking = $state(false);

  async function save() {
    if (lib) {
      await api('PATCH', `/admin/api/libraries/${encodeURIComponent(lib.name)}`, { name, folders });
      toast(`${name} saved.`);
    } else {
      await api('POST', '/admin/api/libraries', { name, kind, folders });
      toast(`${name} added; Jellyfin is reading it.`);
    }
    saved();
  }
</script>

<FormModal title={lib ? `Edit ${lib.name}` : 'Add a library'} wide saveLabel={lib ? 'Save' : 'Add'} {save} {close}
  description="Films and series go in libraries of their own. Folders are as Jellyfin sees them: in a container, its paths, not the host's.">
  <div class="fields">
    <Field label="Name"><input bind:value={name} autocomplete="off" placeholder="Films" required /></Field>
    {#if !lib}
      <Field label="Holds">
        <select bind:value={kind}><option value="films">Films</option><option value="series">Series</option></select>
      </Field>
    {/if}
  </div>
  <h3>Folders</h3>
  <ul class="list">
    {#each folders as f, i (f)}
      <li><Icon paths={icons.folder} /><div class="mono">{f}</div>
        <button type="button" class="small ghost" onclick={() => folders.splice(i, 1)}>Remove</button></li>
    {:else}
      <li><div class="hint">No folder yet.</div></li>
    {/each}
  </ul>
  <div class="actions-row"><button type="button" class="small" onclick={() => (picking = true)}>Add a folder</button></div>
  {#if picking}
    <FolderPicker pick={(f) => { if (!folders.includes(f)) folders.push(f); picking = false; }} close={() => (picking = false)} />
  {/if}
</FormModal>
