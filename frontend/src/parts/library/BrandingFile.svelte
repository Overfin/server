<!-- One of branding's files: what it's for, its preview, upload and
     remove. -->
<script lang="ts">
  import { Status, ask, toast } from '@overfin/ui';
  import { api, upload } from '../../lib/api';
  import { message } from '../../lib/format';
  import type { Branding } from '../../lib/types-library';

  let { kind, label, hint, branding, changed }: { kind: string; label: string; hint: string; branding: Branding; changed: (b: Branding) => void } = $props();

  let picker = $state<HTMLInputElement>();
  let busy = $state(false);
  let failure = $state('');
  const url = $derived(branding.files[kind]);

  async function run(action: () => Promise<void>) {
    busy = true;
    failure = '';
    try {
      await action();
    } catch (err) {
      failure = message(err);
    } finally {
      busy = false;
    }
  }

  function picked() {
    const file = picker?.files?.[0];
    if (!file) return;
    run(async () => {
      changed(await upload<Branding>(`/admin/api/branding/files/${kind}`, file));
      toast(`${label} saved.`);
    });
  }

  function remove() {
    run(async () => {
      if (!(await ask(`Remove this file (${label})? It's deleted from the server.`, { yes: 'Remove', danger: true }))) return;
      changed(await api<Branding>('DELETE', `/admin/api/branding/files/${kind}`));
    });
  }
</script>

<div class="brand-file">
  <div>
    <div class="label">{label}</div>
    <div class="hint">{hint}</div>
    <Status text={failure} kind={failure ? 'error' : ''} />
  </div>
  {#if url}
    <div class="brand-preview" class:light={kind === 'banner-light'} class:dark={kind !== 'banner-light'} class:square={kind === 'logo'}><img src={url} alt={label} /></div>
  {:else}
    <div class="hint">None.</div>
  {/if}
  <div class="row">
    <input bind:this={picker} type="file" accept="image/png,image/jpeg,image/webp,image/gif" hidden onchange={picked} />
    <button type="button" disabled={busy} onclick={() => picker?.click()}>{url ? 'Replace…' : 'Upload…'}</button>
    {#if url}<button type="button" class="danger" disabled={busy} onclick={remove}>Remove</button>{/if}
  </div>
</div>
