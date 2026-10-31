<!-- Seerr: optional, where requests go; tested before it's saved. -->
<script lang="ts">
  import { Card, Badge, Field, Status, Busy, ask, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { message } from '../../lib/format';
  import type { Line, Seerr } from '../../lib/types-server';

  let { sr, reload }: { sr: Seerr; reload: () => void } = $props();

  // svelte-ignore state_referenced_locally
  let url = $state(sr.url);
  let key = $state('');
  // svelte-ignore state_referenced_locally
  let st = $state<Line>({ text: sr.url ? 'Requests go to Seerr.' : 'Without Seerr, nobody can request titles.', kind: '' });
  const fail = (err: Error) => (st = { text: message(err), kind: 'error' });

  async function send(body: { url: string; key: string }) {
    if (!body.url.trim() && !sr.url) {
      toast('Nothing to save.');
      return;
    }
    st = { text: 'Testing…', kind: '' };
    const out = await api<{ summary: string; url: string }>('PUT', '/admin/api/seerr', body);
    st = { text: out.summary, kind: 'ok' };
    toast(out.url ? 'Seerr saved.' : 'Seerr disconnected.');
    reload();
  }

  async function off() {
    if (await ask('Disconnect Seerr? Requests stop until it\'s connected again.', { yes: 'Disconnect', danger: true })) await send({ url: '', key: '' });
  }
</script>

<Card title="Seerr" description="Optional: where requests go. Keep it off the internet; only Overfin needs to reach it.">
  {#snippet badge()}{#if sr.url}<Badge kind="ok">Connected</Badge>{:else}<Badge>Not connected</Badge>{/if}{/snippet}
  <div class="fields">
    <Field label="Address" hint="As Overfin reaches it." wide><input bind:value={url} autocomplete="off" spellcheck="false" placeholder="http://192.168.1.10:5055" /></Field>
    <Field label="API key" hint="In Seerr's settings, under General." wide><input type="password" bind:value={key} autocomplete="off" placeholder={sr.url ? 'Unchanged' : ''} /></Field>
  </div>
  {#snippet foot()}
    <Status text={st.text} kind={st.kind} />
    {#if sr.url}<Busy action={off} {fail}>Disconnect</Busy>{/if}
    <Busy kind="primary" action={() => send({ url, key })} {fail}>Test and save</Busy>
  {/snippet}
</Card>
