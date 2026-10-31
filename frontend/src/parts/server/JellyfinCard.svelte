<!-- Overfin's own Jellyfin: its address, tested before it's saved, and
     access to Jellyfin itself. -->
<script lang="ts">
  import { Card, Badge, Field, Status, Busy, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { message } from '../../lib/format';
  import type { Jellyfin, Line } from '../../lib/types-server';
  import JellyfinAccess from './JellyfinAccess.svelte';

  let { j }: { j: Jellyfin } = $props();

  const description = 'Overfin\'s own Jellyfin, where your media is read. Overfin is the only one that talks to it, with a key it made itself.';
  // svelte-ignore state_referenced_locally
  let url = $state(j.url);
  let st = $state<Line>({ text: 'Connected.', kind: '' });

  async function save() {
    st = { text: 'Testing…', kind: '' };
    const out = await api<{ url: string; summary: string }>('PUT', '/admin/api/jellyfin', { url });
    url = out.url;
    st = { text: out.summary, kind: 'ok' };
    toast('Jellyfin saved.');
  }
</script>

{#if j.inFile}
  <Card title="Jellyfin" {description}>
    {#snippet badge()}<Badge kind="ok">Connected</Badge>{/snippet}
    <div class="box">
      <div>{j.url}</div>
      <div class="hint">Set in Overfin's configuration (its file, or OVERFIN_JELLYFIN_URL): change it there.</div>
    </div>
    <JellyfinAccess access={j.access} />
  </Card>
{:else}
  <Card title="Jellyfin" {description}>
    {#snippet badge()}<Badge kind="ok">Connected</Badge>{/snippet}
    <div class="fields">
      <Field label="Address" wide hint="As Overfin reaches it; a new address for the same server. Not localhost: in a container, that's Overfin itself.">
        <input bind:value={url} autocomplete="off" spellcheck="false" />
      </Field>
    </div>
    <JellyfinAccess access={j.access} />
    {#snippet foot()}
      <Status text={st.text} kind={st.kind} />
      <Busy kind="primary" action={save} fail={(err) => (st = { text: message(err), kind: 'error' })}>Test and save</Busy>
    {/snippet}
  </Card>
{/if}
