<!-- Federation: pairing this server with other Overfin servers, by a
     one-time code one operator sends the other. Pairing only, so far:
     nothing is shared yet (ROADMAP.md milestone 6). -->
<script lang="ts">
  import { onDestroy } from 'svelte';
  import { PageHead, Card, Field, FormModal, Modal, Secret, Setting, Empty, Loading, Notice, ask, toast } from '@overfin/ui';
  import { api } from '../lib/api';
  import { ago, day, when, message, plural } from '../lib/format';

  interface Peer {
    id: string;
    name: string;
    address: string;
    state: 'ours' | 'theirs' | 'paired' | 'gone';
    fingerprint: string;
    createdAt: string;
    pairedAt?: string;
    seenAt?: string;
    offers: number;
    wants: number;
    titles: number;
  }
  interface Lib {
    id: string;
    name: string;
    kind: string;
    picked: boolean;
    gone?: boolean;
    label?: string;
    shown?: string;
  }
  interface Limits {
    streams: number;
    transcodes: number;
    bitrateKbps: number;
  }
  interface Sharing {
    ours: Lib[];
    theirs: Lib[];
    reachable: boolean;
    limits: Limits;
    theirLimits?: Limits;
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
  // Sharing with a paired server: what we offer, what we want.
  let sharingWith = $state<Peer | null>(null);
  let sharing = $state<Sharing | null>(null);
  let offer = $state<string[]>([]);
  let want = $state<string[]>([]);
  // Your names for the wanted libraries' entries on the site, by ID.
  let labels = $state<Record<string, string>>({});
  let limits = $state<Limits>({ streams: 2, transcodes: 1, bitrateKbps: 0 });

  async function load() {
    try {
      s = await api<State>('GET', '/admin/api/federation');
    } catch (err) {
      problem = message(err);
    }
  }

  // The page asks again by itself every 10 seconds while it's on screen:
  // another server using a code, the other operator accepting, what they
  // offer, how many titles came (the server checks with the others in the
  // background). Open windows keep what's typed in them: only the lists
  // change.
  const timer = setInterval(() => {
    if (!document.hidden) load();
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

  async function openSharing(p: Peer) {
    try {
      const sh = await api<Sharing>('GET', `/admin/api/federation/peers/${p.id}/sharing`);
      offer = sh.ours.filter((l) => l.picked).map((l) => l.id);
      want = sh.theirs.filter((l) => l.picked).map((l) => l.id);
      labels = Object.fromEntries(sh.theirs.map((l) => [l.id, l.label ?? '']));
      limits = { ...sh.limits };
      sharing = sh;
      sharingWith = p;
    } catch (err) {
      toast(message(err), true);
    }
  }

  async function saveSharing() {
    if (!sharingWith) return;
    const n = (v: unknown) => Number(v) || 0;
    await api('PUT', `/admin/api/federation/peers/${sharingWith.id}/sharing`, {
      offer,
      want,
      labels: Object.fromEntries(want.map((id) => [id, (labels[id] ?? '').trim()])),
      limits: { streams: n(limits.streams), transcodes: n(limits.transcodes), bitrateKbps: n(limits.bitrateKbps) },
    });
    toast('Sharing saved.');
    await load();
  }

  const kinds: Record<string, string> = { movies: 'Films', tvshows: 'Series', music: 'Music', musicvideos: 'Music videos', homevideos: 'Home videos', boxsets: 'Collections', books: 'Books', photos: 'Photos', mixed: 'Mixed' };

  const limitsText = (l: Limits) =>
    `${plural(l.streams, 'stream')} at once, ${plural(l.transcodes, 'transcode')} among them, ${l.bitrateKbps ? `${l.bitrateKbps} kbps at most` : 'no bitrate cap'}`;

  const shareText = (p: Peer) => `Offers ${plural(p.offers, 'library', 'libraries')} · wants ${p.wants}${p.wants ? ` (${plural(p.titles, 'title')})` : ''}`;
  // The kind, unless the name already says it ("Films").
  const kindOf = (l: Lib) => (kinds[l.kind] && kinds[l.kind].toLowerCase() !== l.name.toLowerCase() ? kinds[l.kind] : '');

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

<PageHead title="Federation" description="Pair this server with other Overfin servers. One operator makes a code and sends it to the other, who pastes it here; the first then accepts, after you both compared fingerprints. Then each picks, library by library, what it offers the other and what it wants: only what one offers and the other wants is shared. Its titles then show on your site beside yours, and play straight from it.">
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
    <Setting stack label="Fingerprint" hint="What the other operator compares, to be sure the code is really this server's.">
      {#snippet children()}<code class="fingerprint">{s?.fingerprint}</code>{/snippet}
    </Setting>
    {#if s.address?.startsWith('http://')}
      <Notice warn>The address is plain http: requests between servers are still signed, so neither can be impersonated, but what they say can be read on the way.</Notice>
    {/if}
  </Card>

  <Card title="Servers">
    <div class="table-wrap">
      <table>
        <thead><tr><th>Server</th><th class="hide-narrow">Status</th><th class="hide-narrow">Fingerprint</th><th><span class="visually-hidden">Actions</span></th></tr></thead>
        <tbody>
          {#each s.peers ?? [] as p (p.id)}
            <tr>
              <td><div class="name">{p.name}</div><div class="hint">{p.address}</div><div class="only-narrow">{stateText(p)}</div>{#if p.state === 'paired'}<div class="hint only-narrow">{shareText(p)}</div>{/if}</td>
              <td class="hide-narrow"><div>{stateText(p)}</div>{#if p.state === 'paired'}<div class="hint">{shareText(p)}</div>{/if}{#if p.seenAt}<div class="hint">heard from {ago(p.seenAt)}</div>{/if}</td>
              <td class="hide-narrow"><code class="fingerprint">{p.fingerprint}</code></td>
              <td class="row-actions">
                {#if p.state === 'ours'}
                  <button type="button" class="small primary" onclick={() => accept(p)}>Accept</button>
                  <button type="button" class="small danger" onclick={() => end(p)}>Refuse</button>
                {:else if p.state === 'paired'}
                  <button type="button" class="small primary" onclick={() => openSharing(p)}>Sharing</button>
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

{#if sharingWith && sharing}
  <FormModal title={`Sharing with ${sharingWith.name}`} description="Only what one server offers and the other wants is shared." saveLabel="Save" save={saveSharing} close={() => { sharingWith = null; sharing = null; }}>
    <div class="setting stack">
      <div><div class="label">You offer</div><div class="hint">Your libraries {sharingWith.name} may take. You only ever offer your own.</div></div>
      <div class="checks all">
        {#each sharing.ours as l (l.id)}<label class="inline"><input type="checkbox" value={l.id} bind:group={offer} /> {l.name}{#if kindOf(l)} <span class="hint">({kindOf(l)})</span>{/if}</label>
        {:else}<span class="hint">This server has no library yet.</span>{/each}
      </div>
    </div>
    <div class="setting stack">
      <div><div class="label">You want</div><div class="hint">Out of what {sharingWith.name} offers you.</div></div>
      {#if !sharing.reachable}<Notice warn>{sharingWith.name} can't be reached now: what you want can't be changed until it can.</Notice>{/if}
      <div class="checks all">
        {#each sharing.theirs as l (l.id)}<label class="inline"><input type="checkbox" value={l.id} bind:group={want} disabled={!sharing.reachable} /> {l.name}{#if kindOf(l)} <span class="hint">({kindOf(l)})</span>{/if}{#if l.gone} <span class="hint">· no longer offered</span>{/if}</label>
        {:else}<span class="hint">{sharing.reachable ? `${sharingWith.name} offers you nothing yet.` : ''}</span>{/each}
      </div>
      {#if sharing.theirLimits}<div class="hint">{sharingWith.name} lets you use {limitsText(sharing.theirLimits)}.</div>{/if}
    </div>
    {#if want.length}
      <div class="setting stack">
        <div><div class="label">Names on your site</div><div class="hint">Each library you want is an entry of the site's menu, beside yours, while "Libraries in the menu" is on (Library → Categories); your categories take its titles in by their rules either way. Leave a name empty for the one {sharingWith.name} gives.</div></div>
        {#each sharing.theirs.filter((l) => want.includes(l.id)) as l (l.id)}
          <label class="stack">{l.name} <input type="text" maxlength="64" bind:value={labels[l.id]} placeholder={l.shown ?? l.name} /></label>
        {/each}
      </div>
    {/if}
    <Setting label="Streams at once" hint={`What ${sharingWith.name}'s people may play from this server at the same time.`}>
      {#snippet children(ids)}<input type="number" min="0" max="50" bind:value={limits.streams} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
    </Setting>
    <Setting label="Transcodes among them" hint="Re-encoded video, which is what costs. Your server's own reserve (Features & limits) applies too.">
      {#snippet children(ids)}<input type="number" min="0" max="50" bind:value={limits.transcodes} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
    </Setting>
    <Setting label="Bitrate cap (kbps)" hint="0: no cap.">
      {#snippet children(ids)}<input type="number" min="0" bind:value={limits.bitrateKbps} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
    </Setting>
  </FormModal>
{/if}

{#if preview}
  <Modal title="Pair with this server?" close={() => (preview = null)}>
    <Setting label="Address">
      {#snippet children()}<code>{preview?.address}</code>{/snippet}
    </Setting>
    <Setting stack label="Fingerprint" hint="Compare it with the other operator, by phone or a message you trust: if it differs, the code was changed on the way.">
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
  .only-narrow {
    display: none;
  }
  @media (max-width: 860px) {
    .only-narrow {
      display: block;
    }
  }
</style>
