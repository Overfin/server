<!-- Federation: pairing this server with other Overfin servers, by a
     one-time code one operator sends the other. Pairing only, so far:
     nothing is shared yet (ROADMAP.md milestone 6). -->
<script lang="ts">
  import { onDestroy } from 'svelte';
  import { PageHead, Card, Field, FormModal, Modal, Secret, Setting, Empty, Loading, Notice, ask, toast } from '@overfin/ui';
  import { api } from '../lib/api';
  import { ago, day, when, message } from '../lib/format';

  interface Peer {
    id: string;
    name: string;
    address: string;
    state: 'ours' | 'theirs' | 'paired' | 'gone';
    fingerprint: string;
    createdAt: string;
    pairedAt?: string;
    seenAt?: string;
  }
  interface Code {
    id: string;
    note: string;
    createdAt: string;
    expiresAt: string;
  }
  interface State {
    available: boolean;
    address?: string;
    fingerprint?: string;
    codes?: Code[];
    peers?: Peer[];
  }
  interface Preview {
    address: string;
    fingerprint: string;
    expiresAt: string;
    expired: boolean;
  }

  let s = $state<State | null>(null);
  let problem = $state('');
  // Making a code: its note, then the code itself, shown once.
  let making = $state(false);
  let note = $state('');
  let made = $state<{ note: string; code: string } | null>(null);
  // Using another server's code: pasted, then what it says, to confirm.
  let using = $state(false);
  let pasted = $state('');
  let preview = $state<Preview | null>(null);
  let pairing = $state(false);

  async function load() {
    try {
      s = await api<State>('GET', '/admin/api/federation');
    } catch (err) {
      problem = message(err);
    }
  }

  // While another operator is to accept, the page asks again by itself
  // (the server checks with the other in the background).
  const timer = setInterval(() => {
    if (s?.peers?.some((p) => p.state === 'theirs')) load();
  }, 10_000);
  onDestroy(() => clearInterval(timer));

  function newCode() {
    note = '';
    making = true;
  }

  async function make() {
    const c = await api<{ note: string; code: string }>('POST', '/admin/api/federation/codes', { note });
    await load();
    setTimeout(() => (made = c), 0);
  }

  async function withdraw(c: Code) {
    if (!(await ask(`Withdraw the code${c.note ? ` "${c.note}"` : ''}? It stops working at once.`, { yes: 'Withdraw', danger: true }))) return;
    try {
      await api('DELETE', `/admin/api/federation/codes/${c.id}`);
      toast('Code withdrawn.');
    } catch (err) {
      toast(message(err), true);
    }
    await load();
  }

  function useCode() {
    pasted = '';
    using = true;
  }

  async function read() {
    const p = await api<Preview>('POST', '/admin/api/federation/preview', { code: pasted });
    setTimeout(() => (preview = p), 0);
  }

  async function pair() {
    pairing = true;
    try {
      await api('POST', '/admin/api/federation/peers', { code: pasted });
      preview = null;
      toast('Sent. The other server now waits for its operator to accept.');
    } catch (err) {
      toast(message(err), true);
    } finally {
      pairing = false;
    }
    await load();
  }

  async function accept(p: Peer) {
    const yes = await ask(
      `Accept ${p.name} (${p.address})? Compare its fingerprint with its operator first, by phone or a message you trust: ${p.fingerprint}. If they differ, refuse it.`,
      { yes: 'Accept' },
    );
    if (!yes) return;
    try {
      await api('POST', `/admin/api/federation/peers/${p.id}/accept`, {});
      toast(`Paired with ${p.name}.`);
    } catch (err) {
      toast(message(err), true);
    }
    await load();
  }

  async function check(p: Peer) {
    try {
      const r = await api<{ peer: Peer; unreachable?: boolean }>('POST', `/admin/api/federation/peers/${p.id}/check`, {});
      if (r.unreachable) toast(`${p.name} couldn't be reached.`, true);
      else toast(stateText(r.peer));
    } catch (err) {
      toast(message(err), true);
    }
    await load();
  }

  async function end(p: Peer) {
    const what = p.state === 'ours' ? `Refuse ${p.name}?` : p.state === 'paired' ? `End the pairing with ${p.name}? It ends at once, on both servers.` : `Remove ${p.name}?`;
    const yes = p.state === 'ours' ? 'Refuse' : p.state === 'paired' ? 'End' : 'Remove';
    if (!(await ask(what, { yes, danger: true }))) return;
    try {
      await api('DELETE', `/admin/api/federation/peers/${p.id}`);
      toast(p.state === 'paired' ? 'Pairing ended.' : 'Removed.');
    } catch (err) {
      toast(message(err), true);
    }
    await load();
  }

  function stateText(p: Peer): string {
    switch (p.state) {
      case 'ours':
        return 'Waiting for you to accept';
      case 'theirs':
        return 'Waiting for its operator to accept';
      case 'paired':
        return `Paired ${p.pairedAt ? day(p.pairedAt) : ''}`.trim();
      case 'gone':
        return 'It no longer knows this server';
    }
  }

  load();
</script>

<PageHead title="Federation" description="Pair this server with other Overfin servers. One operator makes a code and sends it to the other, who pastes it here; the first then accepts, after you both compared fingerprints. Pairing only for now: nothing is shared yet.">
  {#snippet actions()}
    {#if s?.available}
      <button type="button" onclick={useCode}>Use a code</button>
      <button type="button" class="primary" onclick={newCode}>New code</button>
    {/if}
  {/snippet}
</PageHead>

{#if problem}
  <Notice warn>{problem}</Notice>
{:else if !s}
  <Loading />
{:else if !s.available}
  <Notice warn>Federation needs the site's address, which codes give to other servers: set <code>site.url</code> in the configuration (or an HTTPS domain), as http(s)://host with nothing after it.</Notice>
{:else}
  <Card title="This server">
    <Setting label="Address" hint="Where other servers reach this one, as codes give it.">
      {#snippet children()}<code>{s?.address}</code>{/snippet}
    </Setting>
    <Setting label="Fingerprint" hint="What the other operator compares, to be sure the code is really this server's.">
      {#snippet children()}<code class="fingerprint">{s?.fingerprint}</code>{/snippet}
    </Setting>
    {#if s.address?.startsWith('http://')}
      <Notice warn>The address is plain http: requests between servers are still signed, so neither can be impersonated, but what they say can be read on the way.</Notice>
    {/if}
  </Card>

  <Card title="Servers">
    <div class="table-wrap">
      <table>
        <thead><tr><th>Server</th><th>Status</th><th class="hide-narrow">Fingerprint</th><th><span class="visually-hidden">Actions</span></th></tr></thead>
        <tbody>
          {#each s.peers ?? [] as p (p.id)}
            <tr>
              <td><div class="name">{p.name}</div><div class="hint">{p.address}</div></td>
              <td><div>{stateText(p)}</div>{#if p.seenAt}<div class="hint">heard from {ago(p.seenAt)}</div>{/if}</td>
              <td class="hide-narrow"><code class="fingerprint">{p.fingerprint}</code></td>
              <td class="row-actions">
                {#if p.state === 'ours'}
                  <button type="button" class="small primary" onclick={() => accept(p)}>Accept</button>
                  <button type="button" class="small danger" onclick={() => end(p)}>Refuse</button>
                {:else if p.state === 'paired'}
                  <button type="button" class="small" onclick={() => check(p)}>Check</button>
                  <button type="button" class="small danger" onclick={() => end(p)}>End</button>
                {:else}
                  <button type="button" class="small" onclick={() => check(p)}>Check</button>
                  <button type="button" class="small danger" onclick={() => end(p)}>Remove</button>
                {/if}
              </td>
            </tr>
          {:else}
            <tr><td colspan="4"><Empty>No server yet: make a code, or use another server's.</Empty></td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </Card>

  {#if s.codes?.length}
    <Card title="Codes not used yet">
      <div class="table-wrap">
        <table>
          <thead><tr><th>For</th><th>Works until</th><th><span class="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {#each s.codes as c (c.id)}
              <tr>
                <td><div class="name">{c.note || 'No note'}</div><div class="hint">made {ago(c.createdAt)}</div></td>
                <td class="hint">{when(c.expiresAt)}</td>
                <td class="row-actions"><button type="button" class="small danger" onclick={() => withdraw(c)}>Withdraw</button></td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </Card>
  {/if}
{/if}

{#if making}
  <FormModal title="New code" description="One code pairs one server, once, within a day." saveLabel="Make the code" save={make} close={() => (making = false)}>
    <Field label="For" wide hint="A note for you, to tell your codes apart."><input type="text" bind:value={note} placeholder="e.g. Marc's server" maxlength="80" /></Field>
  </FormModal>
{/if}

{#if made}
  <Modal title="Pairing code" close={() => (made = null)}>
    <Secret value={made.code} after={`Send it to the other server's operator however you like. They paste it in their panel, under Federation, "Use a code"; then their server appears here, for you to accept once you've compared fingerprints. It works once, until ${when(new Date(Date.now() + 24 * 3600 * 1000).toISOString())}.`}>Copy it now: it won't be shown again.</Secret>
    {#snippet foot()}
      <span class="grow"></span>
      <button type="button" class="primary" onclick={() => (made = null)}>Done</button>
    {/snippet}
  </Modal>
{/if}

{#if using}
  <FormModal title="Use a code" description="The code another server's operator sent you." saveLabel="Read the code" save={read} close={() => (using = false)}>
    <Field label="Code" wide><textarea bind:value={pasted} rows="4" spellcheck="false" autocomplete="off" placeholder="overfin-pair-1.…" required></textarea></Field>
  </FormModal>
{/if}

{#if preview}
  <Modal title="Pair with this server?" close={() => (preview = null)}>
    <Setting label="Address">
      {#snippet children()}<code>{preview?.address}</code>{/snippet}
    </Setting>
    <Setting label="Fingerprint" hint="Compare it with the other operator, by phone or a message you trust: if it differs, the code was changed on the way.">
      {#snippet children()}<code class="fingerprint">{preview?.fingerprint}</code>{/snippet}
    </Setting>
    {#if preview.expired}
      <Notice warn>This code stopped working {ago(preview.expiresAt)}: ask for a new one.</Notice>
    {/if}
    {#snippet foot()}
      <span class="grow"></span>
      <button type="button" onclick={() => (preview = null)}>Cancel</button>
      <button type="button" class="primary" disabled={!preview || preview.expired || pairing} onclick={pair}>Pair</button>
    {/snippet}
  </Modal>
{/if}

<style>
  .fingerprint {
    white-space: nowrap;
    letter-spacing: 0.02em;
  }
</style>
