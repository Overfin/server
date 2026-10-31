<!-- Whether Jellyfin makes seek-bar previews, off by default. -->
<script lang="ts">
  import { Card, Badge, Setting, Status, Busy, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { message } from '../../lib/format';
  import type { Line, Previews } from '../../lib/types-server';

  let { pv, reload }: { pv: Previews; reload: () => void } = $props();

  // svelte-ignore state_referenced_locally
  let on = $state(pv.on);
  let st = $state<Line>({ text: '', kind: '' });

  async function save() {
    if (on === pv.on) return;
    const out = await api<Previews>('PUT', '/admin/api/previews', { on });
    toast(out.on ? 'Jellyfin makes previews now.' : 'Jellyfin makes no more previews.');
    reload();
  }
</script>

<Card title="Seek-bar previews" description="Small pictures shown above the seek bar while seeking. Jellyfin makes them from each video: it takes time, the GPU, and disk space (a few MB per video).">
  {#snippet badge()}{#if pv.on}<Badge kind="ok">On</Badge>{:else}<Badge kind="plain">Off</Badge>{/if}{/snippet}
  <Setting label="Make previews" hint="On: Jellyfin makes them for every video, new ones as they arrive; the first time, with a large library, that takes days. Off: no more are made.">
    {#snippet children(ids)}<input type="checkbox" bind:checked={on} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
  </Setting>
  {#snippet foot()}
    <Status text={st.text} kind={st.kind} />
    <Busy kind="primary" action={save} fail={(err) => (st = { text: message(err), kind: 'error' })}>Save</Busy>
  {/snippet}
</Card>
