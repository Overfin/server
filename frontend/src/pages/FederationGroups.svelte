<!-- Groups (MSFP groups part): servers whose operators know each other
     join one group with a code, then pair with each member that accepts,
     without a code for each pair. The list of groups, and a window for
     each: its members, the requests waiting for you, its bans, what you
     offer it, and leaving it. -->
<script lang="ts">
  import { Card, Field, FormModal, Modal, Setting, Badge, Empty, Notice, Tabs, ask, toast } from '@overfin/ui';
  import { api } from '../lib/api';
  import { ago, day, message, plural, when } from '../lib/format';
  import { readingLang, readingText, type ReadFailed } from '../lib/reading';

  export interface Member {
    id: string;
    name: string;
    fingerprint: string;
    role: 'owner' | 'admin' | 'member';
    invitedBy?: string;
    joinedAt: string;
    peer?: string;
    paired?: boolean;
    // individual: also paired directly with it.
    individual?: boolean;
    refused?: boolean;
    asked?: boolean;
    me?: boolean;
    // readFailed: why the last reading of its titles failed.
    readFailed?: ReadFailed;
  }
  export interface Group {
    id: string;
    name: string;
    capPerDay: number;
    role: 'owner' | 'admin' | 'member';
    me: string;
    owner: string;
    heir?: string;
    accept: boolean;
    offers: number;
    pool: Limits;
    dissolved?: boolean;
    joining?: boolean;
    noWayIn?: boolean;
    members: Member[];
    banned: { id: string; name: string; fingerprint: string; reason?: string }[];
    requests: { mail: string; from: string; name: string; fingerprint: string; receivedAt: string }[];
    // Servers already paired with this one that used its code for the
    // group, waiting for you.
    joins: { peer: string; name: string; fingerprint: string; receivedAt: string }[];
    links: { peer: string; name: string; only: boolean }[];
  }
  interface Limits {
    streams: number;
    transcodes: number;
    bitrateKbps: number;
    downloads: number;
    downloadsPerDay: number;
    downloadKbps: number;
    scansPerDay: number;
  }
  interface Lib {
    id: string;
    name: string;
    kind: string;
    picked: boolean;
  }
  // A server that used one of this server's invites to a group, waiting
  // for you; an invite not used yet.
  export interface JoinWaiting {
    id: string;
    name: string;
    address: string;
    fingerprint: string;
    joiningGroup?: string;
  }
  export interface Invite {
    id: string;
    note: string;
    createdAt: string;
    expiresAt: string;
    uses: number;
    group?: string;
  }
  interface Shows {
    member: string;
    name: string;
    libraries: { id: string; name: string; kind: string; shown: boolean }[];
  }

  let {
    groups,
    uncallable = false,
    reload,
    invite,
    pairDirectly,
    joining = [],
    invites = [],
    acceptJoining,
    refuseJoining,
    withdraw,
    copyInvite,
    sendInvite,
  }: {
    groups: Group[];
    uncallable?: boolean;
    reload: () => Promise<void>;
    /** invite opens the Invite window for a group. */
    invite: (g: Group) => void;
    /** pairDirectly asks a server known through a group to pair individually. */
    pairDirectly: (peer: string) => void;
    /** joining are the servers that used an invite to a group, waiting for you. */
    joining?: JoinWaiting[];
    /** invites are the invites not used yet (each group's are shown in it). */
    invites?: Invite[];
    acceptJoining: (p: JoinWaiting) => void;
    refuseJoining: (p: JoinWaiting) => void;
    withdraw: (c: Invite) => void;
    copyInvite: (c: Invite) => void;
    /** sendInvite emails an invite; undefined while email is off. */
    sendInvite?: (c: Invite) => void;
  } = $props();

  // The group whose window is open, by ID: the list reloads under it.
  let openID = $state('');
  const open = $derived(groups.find((g) => g.id === openID) ?? null);
  let tab = $state('members');
  // Forms: creating a group or changing its settings; what you offer it;
  // a member's actions; banning; blocking; leaving; dissolving; giving.
  // A group's settings, all in one window: what you share with it and its
  // limits, what you show from it, how new members are accepted, and, for
  // the owner, its name and invites a day. Creating a group asks the same.
  let editing = $state<{ group?: Group; name: string; capPerDay: number; accept: boolean; ours: Lib[]; offer: string[]; limits: Limits; more: boolean; shows: Shows[]; hidden: string[] } | null>(null);
  let memberID = $state('');
  const member = $derived(open?.members.find((m) => m.id === memberID) ?? null);
  let banning = $state<{ member: Member; reason: string } | null>(null);
  let blocking = $state<{ member: Member; invitees: boolean } | null>(null);
  let leaving = $state<{ ask: string[] } | null>(null);
  let dissolving = $state<{ keep: boolean } | null>(null);
  let giving = $state<{ to: string } | null>(null);

  const roleName = { owner: 'Owner', admin: 'Admin', member: 'Member' };
  const isOwner = (g: Group) => g.role === 'owner';
  const canInvite = (g: Group) => (g.role === 'owner' || g.role === 'admin') && !g.dissolved && !uncallable;
  // Who may ban whom: the owner anyone but itself; an admin members only.
  const canBan = (g: Group, m: Member) => !m.me && m.role !== 'owner' && (g.role === 'owner' || (g.role === 'admin' && m.role === 'member'));

  async function act(what: () => Promise<unknown>, done: string) {
    try {
      await what();
      if (done) toast(done);
    } catch (err) {
      toast(message(err), true);
    }
    await reload();
  }

  function openGroup(g: Group, at = 'members') {
    tab = at;
    openID = g.id;
  }

  const n = (v: unknown) => Number(v) || 0;
  const limitsOut = (l: Limits) => ({ streams: n(l.streams), transcodes: n(l.transcodes), bitrateKbps: n(l.bitrateKbps), downloads: n(l.downloads), downloadsPerDay: n(l.downloadsPerDay), downloadKbps: n(l.downloadKbps), scansPerDay: n(l.scansPerDay) });

  async function create() {
    try {
      const ours = await api<Lib[]>('GET', '/admin/api/federation/libraries');
      editing = { name: '', capPerDay: 5, accept: false, ours, offer: [], limits: { streams: 2, transcodes: 1, bitrateKbps: 0, downloads: 0, downloadsPerDay: 0, downloadKbps: 0, scansPerDay: 0 }, more: false, shows: [], hidden: [] };
    } catch (err) {
      toast(message(err), true);
    }
  }

  async function settings(g: Group) {
    try {
      const [sh, shows] = await Promise.all([
        api<{ ours: Lib[]; limits: Limits }>('GET', `/admin/api/federation/groups/${g.id}/sharing`),
        api<Shows[]>('GET', `/admin/api/federation/groups/${g.id}/shows`),
      ]);
      editing = {
        group: g, name: g.name, capPerDay: g.capPerDay, accept: g.accept, more: true,
        ours: sh.ours, offer: sh.ours.filter((l) => l.picked).map((l) => l.id), limits: { ...sh.limits },
        shows, hidden: shows.flatMap((m) => m.libraries.filter((l) => !l.shown).map((l) => showKey(m.member, l.id))),
      };
    } catch (err) {
      toast(message(err), true);
    }
  }

  async function saveEditing() {
    if (!editing) return;
    const g = editing.group;
    if (g) {
      if (isOwner(g) && (editing.name !== g.name || Number(editing.capPerDay) !== g.capPerDay)) {
        await api('PUT', `/admin/api/federation/groups/${g.id}`, { name: editing.name, capPerDay: n(editing.capPerDay) });
      }
      await api('PUT', `/admin/api/federation/groups/${g.id}/sharing`, { offer: editing.offer, limits: limitsOut(editing.limits) });
      const hidden = editing.hidden.map((k) => ({ member: k.split(' ')[0], library: k.split(' ').slice(1).join(' ') }));
      await api('PUT', `/admin/api/federation/groups/${g.id}/shows`, { hidden });
      if (editing.accept !== g.accept) await api('PUT', `/admin/api/federation/groups/${g.id}/answer`, { accept: editing.accept });
      toast('Saved.');
    } else {
      const made = await api<Group>('POST', '/admin/api/federation/groups', {
        name: editing.name, capPerDay: n(editing.capPerDay) || 5, offer: editing.offer, limits: limitsOut(editing.limits),
      });
      if (editing.accept) await api('PUT', `/admin/api/federation/groups/${made.id}/answer`, { accept: true });
      toast(`Group "${made.name}" created. Invite servers to it.`);
    }
    await reload();
  }

  // What you show from the group: each member's libraries, all shown
  // unless unticked. A key is member + library.
  const showKey = (m: string, l: string) => `${m} ${l}`;
  function toggleShown(m: string, l: string, shown: boolean) {
    if (!editing) return;
    const k = showKey(m, l);
    editing.hidden = shown ? editing.hidden.filter((x) => x !== k) : [...editing.hidden, k];
  }



  async function acceptRequest(g: Group, r: Group['requests'][number]) {
    const yes = await ask(`Pair with ${r.name}? It's a member of "${g.name}": no fingerprint to compare, the group vouches for it. Its fingerprint: ${r.fingerprint}.`, { yes: 'Pair' });
    if (yes) await act(() => api('POST', `/admin/api/federation/groups/${g.id}/requests/${encodeURIComponent(r.mail)}/accept`, {}), `Pairing with ${r.name}.`);
  }

  async function refuseRequest(g: Group, r: Group['requests'][number]) {
    const yes = await ask(`Refuse ${r.name}? It isn't told; its requests are dropped until you change your mind (in Members).`, { yes: 'Refuse', danger: true });
    if (yes) await act(() => api('POST', `/admin/api/federation/groups/${g.id}/requests/${encodeURIComponent(r.mail)}/refuse`, {}), 'Refused.');
  }

  async function acceptJoin(g: Group, j: Group['joins'][number]) {
    const yes = await ask(`Accept ${j.name} into "${g.name}"? You're already paired with it; it used your code. Its fingerprint: ${j.fingerprint}.`, { yes: 'Accept' });
    if (yes) await act(() => api('POST', `/admin/api/federation/groups/${g.id}/joins/${encodeURIComponent(j.peer)}/accept`, {}), `${j.name} joins "${g.name}".`);
  }

  async function refuseJoin(g: Group, j: Group['joins'][number]) {
    const yes = await ask(`Refuse ${j.name} into "${g.name}"? It isn't told; your pairing with it stays.`, { yes: 'Refuse', danger: true });
    if (yes) await act(() => api('POST', `/admin/api/federation/groups/${g.id}/joins/${encodeURIComponent(j.peer)}/refuse`, {}), 'Refused.');
  }

  async function giveUp(g: Group) {
    const yes = await ask(`Give up joining "${g.name}"? Your pairings stay.`, { yes: 'Give up', danger: true });
    if (!yes) return;
    await act(() => api('POST', `/admin/api/federation/groups/${g.id}/leave`, { ask: [] }), '');
    openID = '';
  }

  async function askMember(g: Group, m: Member) {
    await act(() => api('POST', `/admin/api/federation/groups/${g.id}/asks/${encodeURIComponent(m.id)}`, {}), `Asked ${m.name}. Its operator answers.`);
  }

  async function liftRefusal(g: Group, m: Member) {
    await act(() => api('DELETE', `/admin/api/federation/groups/${g.id}/refusals/${encodeURIComponent(m.id)}`), `${m.name}'s requests are heard again.`);
  }

  async function setAdmin(g: Group, m: Member, on: boolean) {
    const yes = await ask(on ? `Make ${m.name} an admin of "${g.name}"? Admins invite servers and ban members.` : `Remove ${m.name}'s admin role?`, { yes: on ? 'Make admin' : 'Remove the role', danger: !on });
    if (yes) await act(() => api('PUT', `/admin/api/federation/groups/${g.id}/admins/${encodeURIComponent(m.id)}`, { on }), 'Done. The members are told.');
  }

  async function setHeir(g: Group, m: Member | null) {
    const yes = await ask(m ? `Name ${m.name} heir of "${g.name}"? It takes the group if you step down or leave without giving it to someone else.` : 'Name no heir? The oldest admin then takes the group, or the oldest member.', { yes: 'Name' });
    if (yes) await act(() => api('PUT', `/admin/api/federation/groups/${g.id}/heir`, { member: m?.id ?? '' }), 'Done. The members are told.');
  }

  async function saveGiving() {
    if (!open || !giving) return;
    await api('POST', `/admin/api/federation/groups/${open.id}/give`, { member: giving.to });
    toast('The group is given. You stay in it, as a member.');
    await reload();
  }

  async function saveBan() {
    if (!open || !banning) return;
    await api('POST', `/admin/api/federation/groups/${open.id}/bans`, { member: banning.member.id, reason: banning.reason.trim() });
    toast(`${banning.member.name} is banned. The members are told, and so is it.`);
    memberID = '';
    await reload();
  }

  async function unban(g: Group, b: Group['banned'][number]) {
    const yes = await ask(`Lift ${b.name || 'this server'}'s ban? It can be invited again; nothing more.`, { yes: 'Lift the ban' });
    if (yes) await act(() => api('DELETE', `/admin/api/federation/groups/${g.id}/bans/${encodeURIComponent(b.id)}`), 'Ban lifted.');
  }

  async function saveBlock() {
    if (!open || !blocking) return;
    await api('POST', `/admin/api/federation/groups/${open.id}/blocks`, { member: blocking.member.id, invitees: blocking.invitees });
    toast(`${blocking.member.name} is blocked. Nobody is told.`);
    memberID = '';
    await reload();
  }

  async function saveLeave() {
    if (!open || !leaving) return;
    await api('POST', `/admin/api/federation/groups/${open.id}/leave`, { ask: leaving.ask });
    toast(`You left "${open.name}".`);
    openID = '';
    await reload();
  }

  async function saveDissolve() {
    if (!open || !dissolving) return;
    await api('POST', `/admin/api/federation/groups/${open.id}/dissolve`, { keepPairings: dissolving.keep });
    toast(`"${open.name}" is dissolved.`);
    openID = '';
    await reload();
  }

  // A member's pairing with this server, in a few words.
  function linkText(m: Member): string {
    if (m.me) return 'This server';
    if (m.paired) return m.individual ? 'Paired, also individually' : 'Paired';
    if (m.peer) return 'Pairing';
    if (requested(m)) return 'Asks to pair: see Waiting for you';
    if (m.refused) return 'You refused it';
    if (m.asked) return 'Asked, waiting for its operator';
    return 'Not paired';
  }

  const others = (g: Group) => g.members.filter((m) => !m.me);
  const joiningOf = (g: Group) => joining.filter((p) => p.joiningGroup === g.id);
  const invitesOf = (g: Group) => invites.filter((c) => c.group === g.id);
  const waiting = (g: Group) => g.requests.length + g.joins.length + joiningOf(g).length;
  const requested = (m: Member) => !!open?.requests.some((r) => r.from === m.id);
</script>

<Card title="Groups" description="Groups of servers that share with each other as one.">
  {#snippet foot()}
    <span class="grow"></span>
    <button type="button" onclick={create}>Create a group</button>
  {/snippet}
  <div class="table-wrap">
    <table>
      <thead><tr><th>Group</th><th class="hide-narrow">Members</th><th><span class="visually-hidden">Actions</span></th></tr></thead>
      <tbody>
        {#each groups as g (g.id)}
          <tr>
            <td>
              <div class="name">{g.name || 'A group'}</div>
              <div class="hint">{roleName[g.role]}{g.dissolved ? ' · dissolved' : ''}{g.joining ? ' · joining: waiting for the list' : ''}</div>
              {#if waiting(g)}<div><Badge kind="warn">{plural(waiting(g), 'request')} waiting</Badge></div>{/if}
              {#if g.noWayIn}<div><Badge kind="err">No way in</Badge></div>{/if}
            </td>
            <td class="hide-narrow"><div>{plural(g.members.length, 'server')}</div><div class="hint">paired with {g.members.filter((m) => m.paired).length} of {others(g).length}</div></td>
            <td class="row-actions">
              {#if waiting(g)}<button type="button" class="small primary" onclick={() => openGroup(g, 'requests')}>Answer</button>{/if}
              {#if canInvite(g)}<button type="button" class="small" onclick={() => invite(g)}>Invite</button>{/if}
              <button type="button" class="small" onclick={() => openGroup(g)}>Open</button>
            </td>
          </tr>
        {:else}
          <tr><td colspan="3"><Empty>No group yet. Create one and invite servers to it, or use a group invite another server's owner sent you (Use a code, at the top).</Empty></td></tr>
        {/each}
      </tbody>
    </table>
  </div>
</Card>

{#if open}
  <Modal size="wide" title={open.name || 'A group'} description={`${roleName[open.role]} · ${plural(open.members.length, 'server')}`} close={() => (openID = '')}>
    {#if open.dissolved}<Notice warn>The owner dissolved this group. It goes from this server at once.</Notice>{/if}
    {#if open.noWayIn}<Notice warn>No way in: this server has no pairing through this group any more. Nothing from the other members reaches it, and nothing it does reaches them, so what you see here may be out of date. To get back in, ask a member for a new code for this group, and use it under Federation, "Use a code".</Notice>{/if}
    {#if open.joining}<Notice>Joining: the group's list comes from the server that invited you, once its owner accepts. Without an answer within 30 days, this server gives up.</Notice>{/if}
    <div class="facts" role="group" aria-label="Your settings for this group">
      <button type="button" class="fact" onclick={() => open && settings(open)} title="Settings">{#if open.offers}<Badge kind="plain">Shares {plural(open.offers, 'library', 'libraries')}</Badge>{:else}<Badge kind="warn">Shares nothing</Badge>{/if}</button>
      <button type="button" class="fact" onclick={() => open && settings(open)} title="Settings"><Badge kind="plain">{plural(open.pool.streams, 'stream')}</Badge></button>
      <button type="button" class="fact" onclick={() => open && settings(open)} title="Settings"><Badge kind="plain">{open.accept ? 'Accepts new members automatically' : 'Confirms new members'}</Badge></button>
    </div>
    <Tabs label="The group" bind:current={tab} tabs={[{ id: 'members', label: 'Members' }, { id: 'requests', label: `Waiting for you${waiting(open) ? ` (${waiting(open)})` : ''}` }, { id: 'invites', label: `Invites${invitesOf(open).length ? ` (${invitesOf(open).length})` : ''}` }, { id: 'banned', label: `Banned${open.banned.length ? ` (${open.banned.length})` : ''}` }]}>
      {#snippet children(at)}
        {#if at === 'members'}
          <div class="table-wrap">
            <table>
              <thead><tr><th>Server</th><th class="hide-narrow">Pairing</th><th class="hide-narrow"><span class="visually-hidden">Actions</span></th></tr></thead>
              <tbody>
                {#each open.members as m (m.id)}
                  <tr>
                    <td>
                      <div class="name">{m.name || 'A server'} {#if m.role !== 'member'}<Badge kind="info">{roleName[m.role]}</Badge>{/if}{#if open.heir === m.id} <Badge>Heir</Badge>{/if}</div>
                      <div class="hint">{m.invitedBy ? `invited by ${m.invitedBy}, ` : ''}joined {day(m.joinedAt)}</div>
                      <div><code class="fingerprint">{m.fingerprint}</code></div>
                      {#if readingText(m.readFailed)}<div class="hint" lang={readingLang}>{readingText(m.readFailed)}</div>{/if}
                      <div class="hint only-narrow">{linkText(m)}</div>
                      <div class="only-narrow narrow-actions">{@render memberActions(m)}</div>
                    </td>
                    <td class="hide-narrow">{linkText(m)}</td>
                    <td class="row-actions hide-narrow">{@render memberActions(m)}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {:else if at === 'requests'}
          {#if waiting(open)}
            <div class="table-wrap">
              <table class="stack-rows">
                <thead><tr><th>Server</th><th><span class="visually-hidden">Actions</span></th></tr></thead>
                <tbody>
                  {#each joiningOf(open) as p (p.id)}
                    <tr>
                      <td><div class="name">{p.name || 'A server'}</div><div class="hint">used your invite to join the group · {p.address}</div><div><code class="fingerprint">{p.fingerprint}</code></div></td>
                      <td class="row-actions">
                        <button type="button" class="small primary" onclick={() => acceptJoining(p)}>Accept</button>
                        <button type="button" class="small danger" onclick={() => refuseJoining(p)}>Refuse</button>
                      </td>
                    </tr>
                  {/each}
                  {#each open.joins as j (j.peer)}
                    <tr>
                      <td><div class="name">{j.name || 'A server'}</div><div class="hint">already paired with you individually, used your invite to join the group, {ago(j.receivedAt)}</div><div><code class="fingerprint">{j.fingerprint}</code></div></td>
                      <td class="row-actions">
                        <button type="button" class="small primary" onclick={() => open && acceptJoin(open, j)}>Accept</button>
                        <button type="button" class="small danger" onclick={() => open && refuseJoin(open, j)}>Refuse</button>
                      </td>
                    </tr>
                  {/each}
                  {#each open.requests as r (r.mail)}
                    <tr>
                      <td><div class="name">{r.name || 'A server'}</div><div class="hint">new in the group, asks to pair with you, {ago(r.receivedAt)}</div><div><code class="fingerprint">{r.fingerprint}</code></div></td>
                      <td class="row-actions">
                        <button type="button" class="small primary" onclick={() => open && acceptRequest(open, r)}>Pair</button>
                        <button type="button" class="small danger" onclick={() => open && refuseRequest(open, r)}>Refuse</button>
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          {:else}
            <Empty>Nothing waiting. A server that joins asks every member to pair; {open.accept ? 'this server accepts new members automatically.' : 'its request then waits here for you.'}</Empty>
          {/if}
        {:else if at === 'invites'}
          {#if invitesOf(open).length}
            <div class="table-wrap">
              <table class="stack-rows">
                <thead><tr><th>Invite</th><th><span class="visually-hidden">Actions</span></th></tr></thead>
                <tbody>
                  {#each invitesOf(open) as c (c.id)}
                    <tr>
                      <td><div class="name">{c.note || 'No note'}</div><div class="hint">{c.uses > 1 ? `${c.uses} uses left · ` : ''}works until {when(c.expiresAt)}</div></td>
                      <td class="row-actions">
                        <button type="button" class="small" onclick={() => copyInvite(c)}>Copy</button>
                        {#if sendInvite}<button type="button" class="small" onclick={() => sendInvite?.(c)}>Send by email</button>{/if}
                        <button type="button" class="small danger" onclick={() => withdraw(c)}>Withdraw</button>
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          {:else}
            <Empty>No invite waiting to be used.{#if canInvite(open)} Invite a server with the Invite button below.{/if}</Empty>
          {/if}
        {:else}
          {#if open.banned.length}
            <div class="table-wrap">
              <table class="stack-rows">
                <thead><tr><th>Server</th><th><span class="visually-hidden">Actions</span></th></tr></thead>
                <tbody>
                  {#each open.banned as b (b.id)}
                    <tr>
                      <td><div class="name">{b.name || 'A server'}</div>{#if b.reason}<div class="hint">{b.reason}</div>{/if}<div><code class="fingerprint">{b.fingerprint}</code></div></td>
                      <td class="row-actions">{#if isOwner(open)}<button type="button" class="small" onclick={() => open && unban(open, b)}>Lift the ban</button>{/if}</td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          {:else}
            <Empty>Nobody is banned. A ban puts a server out for every member, and no invitation brings it back until the owner lifts it.</Empty>
          {/if}
        {/if}
      {/snippet}
    </Tabs>
    {#snippet foot()}
      {#if isOwner(open)}
        <button type="button" class="danger" onclick={() => (dissolving = { keep: true })}>Dissolve</button>
        <button type="button" onclick={() => (giving = { to: '' })} disabled={!others(open).length}>Give the group</button>
      {:else}
        {#if open.joining}
          <button type="button" class="danger" onclick={() => open && giveUp(open)}>Give up</button>
        {:else}
          <button type="button" class="danger" onclick={() => (leaving = { ask: [] })}>Leave</button>
        {/if}
      {/if}
      <span class="grow"></span>
      {#if canInvite(open)}<button type="button" onclick={() => open && invite(open)}>Invite</button>{/if}
      <button type="button" onclick={() => open && settings(open)}>Settings</button>
      <button type="button" class="primary" onclick={() => (openID = '')}>Close</button>
    {/snippet}
  </Modal>
{/if}

{#snippet memberActions(m: Member)}
  {#if !m.me && !m.peer && !m.refused && !m.asked && !requested(m)}<button type="button" class="small" onclick={() => open && askMember(open, m)}>Ask to pair</button>{/if}
  {#if m.refused}<button type="button" class="small" onclick={() => open && liftRefusal(open, m)}>Hear it again</button>{/if}
  {#if !m.me}<button type="button" class="small" onclick={() => (memberID = m.id)}>More</button>{/if}
{/snippet}

{#if open && member}
  <Modal size="small" title={member.name || 'A server'} description={`${roleName[member.role]} of "${open.name}"`} close={() => (memberID = '')}>
    <div class="actions">
      {#if isOwner(open) && member.role === 'member'}<button type="button" onclick={() => open && member && setAdmin(open, member, true)}>Make admin</button>{/if}
      {#if isOwner(open) && member.role === 'admin'}<button type="button" onclick={() => open && member && setAdmin(open, member, false)}>Remove the admin role</button>{/if}
      {#if isOwner(open) && open.heir !== member.id}<button type="button" onclick={() => open && member && setHeir(open, member)}>Name heir</button>{/if}
      {#if isOwner(open) && open.heir === member.id}<button type="button" onclick={() => open && setHeir(open, null)}>No heir</button>{/if}
      {#if isOwner(open)}<button type="button" onclick={() => member && (giving = { to: member.id })}>Give the group to it</button>{/if}
      {#if member.paired && member.peer && !member.individual}<button type="button" onclick={() => member && pairDirectly(member.peer ?? '')}>Invite individually</button>{/if}
      <button type="button" class="danger" onclick={() => member && (blocking = { member, invitees: false })}>Block</button>
      {#if canBan(open, member)}<button type="button" class="danger" onclick={() => member && (banning = { member, reason: '' })}>Ban</button>{/if}
    </div>
    <div class="hint">Block is yours alone, and nobody is told: this server ends its pairing with it, whatever made it, and ignores it in all your groups. Ban puts it out of the group, for everyone, and tells it why.</div>
    {#snippet foot()}
      <span class="grow"></span>
      <button type="button" onclick={() => (memberID = '')}>Close</button>
    {/snippet}
  </Modal>
{/if}

{#if editing}
  <FormModal title={editing.group ? `Settings of "${editing.group.name}"` : 'Create a group'} description={editing.group ? '' : 'You are its owner. Invite servers to it; each one asks the members to pair with it.'} saveLabel={editing.group ? 'Save' : 'Create'} save={saveEditing} close={() => (editing = null)}>
    {#if !editing.group || isOwner(editing.group)}
      <Field label="Name" wide hint="What members see: the same name for everyone."><input type="text" bind:value={editing.name} maxlength="80" required /></Field>
    {/if}
    {#if !editing.group || !editing.group.joining}
      <div class="setting stack">
        <div><div class="label">You share with it</div><div class="hint">Your libraries, for all the group's members.</div></div>
        <div class="checks all">
          {#each editing.ours as l (l.id)}<label class="inline"><input type="checkbox" value={l.id} bind:group={editing.offer} /> {l.name}</label>
          {:else}<span class="hint">This server has no library yet.</span>{/each}
        </div>
      </div>
      <Setting label="Streams at once" hint="For all the group's members together.">
        {#snippet children(ids)}<input type="number" min="0" max="50" bind:value={editing!.limits.streams} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
      <Setting label="Transcodes among them" hint="Re-encoded video, which is what costs.">
        {#snippet children(ids)}<input type="number" min="0" max="50" bind:value={editing!.limits.transcodes} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
      <Setting label="Bitrate cap (kbps)" hint="0: no cap.">
        {#snippet children(ids)}<input type="number" min="0" bind:value={editing!.limits.bitrateKbps} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
      <Setting label="Downloads at once" hint="Copies the group's members may save to watch offline, at the same time. 0: no downloads.">
        {#snippet children(ids)}<input type="number" min="0" max="1000" bind:value={editing!.limits.downloads} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
      <Setting label="Downloads a day" hint="0: no daily cap besides the one above.">
        {#snippet children(ids)}<input type="number" min="0" max="10000" bind:value={editing!.limits.downloadsPerDay} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
      <Setting label="Download speed cap (kbps)" hint="For each download. 0: no cap.">
        {#snippet children(ids)}<input type="number" min="0" max="1000000" bind:value={editing!.limits.downloadKbps} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
      <Setting label="Scans a day" hint="Reads of a file's start, so that a member's media server can list it (never to watch it). 0: no scan access.">
        {#snippet children(ids)}<input type="number" min="0" max="1000000" bind:value={editing!.limits.scansPerDay} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
    {/if}
    {#if editing.group && !editing.group.joining}
      <div class="setting stack">
        <div><div class="label">You show from it</div><div class="hint">Everything its members share with the group shows on your site, now and later. Untick what you don't want.</div></div>
        {#each editing.shows as m (m.member)}
          <div class="hint member-name">{m.name || 'A server'}</div>
          <div class="checks all">
            {#each m.libraries as l (l.id)}<label class="inline"><input type="checkbox" checked={!editing.hidden.includes(showKey(m.member, l.id))} onchange={(e) => toggleShown(m.member, l.id, e.currentTarget.checked)} /> {l.name}</label>
            {:else}<span class="hint">It shares nothing with the group yet, or you aren't paired with it yet.</span>{/each}
          </div>
        {:else}<span class="hint">No other member yet.</span>{/each}
      </div>
    {/if}
    <Setting label="Automatically accept new members" hint="Off: each new member waits for you to confirm it. On: this server pairs with it at once. Each member chooses for itself.">
      {#snippet children(ids)}<input type="checkbox" bind:checked={editing!.accept} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
    </Setting>
    {#if !editing.group || isOwner(editing.group)}
      {#if editing.more}
        <Setting label="Invites a day, for each inviter" hint="How many servers each admin may bring in a day, so an invite that leaks can't flood the group. 1 to 100.">
          {#snippet children(ids)}<input type="number" min="1" max="100" bind:value={editing!.capPerDay} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
        </Setting>
      {:else}
        <div><button type="button" class="small" onclick={() => editing && (editing.more = true)}>More</button></div>
      {/if}
    {/if}
  </FormModal>
{/if}

{#if open && banning}
  <FormModal danger title={`Ban ${banning.member.name}`} description="It's out of the group for every member, and no invitation brings it back until the owner lifts the ban. Its pairings made only through this group end, on every member." saveLabel="Ban" save={saveBan} close={() => (banning = null)}>
    <Field label="Reason" wide hint="Optional. Shown to the members, and to the banned server's operator."><input type="text" bind:value={banning.reason} maxlength="280" /></Field>
  </FormModal>
{/if}

{#if open && blocking}
  <FormModal danger title={`Block ${blocking.member.name}`} description="This server ends its pairing with it, whatever made it, and ignores it in all your groups. It stays in the groups for the others; nobody is told." saveLabel="Block" save={saveBlock} close={() => (blocking = null)}>
    <label class="inline"><input type="checkbox" bind:checked={blocking.invitees} /> Also block the servers it invited, and the ones they invited</label>
  </FormModal>
{/if}

{#if open && leaving}
  <FormModal danger title={`Leave "${open.name}"`} description={open.links.some((l) => l.only) ? 'You are paired with these servers only through the group: leaving unpairs you from them. Tick those you want to stay paired with; their operators are asked first.' : 'The servers you are also paired with directly, or through another group, stay paired.'} saveLabel="Leave" save={saveLeave} close={() => (leaving = null)}>
    {#if open.links.some((l) => l.only)}
      <div class="setting stack">
        <div><div class="label">Stay paired with</div></div>
        <div class="checks all">
          {#each open.links.filter((l) => l.only) as l (l.peer)}<label class="inline"><input type="checkbox" value={l.peer} bind:group={leaving.ask} /> <span>{l.name}</span></label>{/each}
        </div>
        <div><button type="button" class="small" onclick={() => leaving && open && (leaving.ask = open.links.filter((l) => l.only).map((l) => l.peer))}>Tick all</button></div>
      </div>
    {/if}
  </FormModal>
{/if}

{#if open && dissolving}
  <FormModal danger title={`Dissolve "${open.name}"`} description="Every member removes the group. It can't be undone: make a new group to start again." saveLabel="Dissolve" save={saveDissolve} close={() => (dissolving = null)}>
    <label class="inline"><input type="checkbox" bind:checked={dissolving.keep} /> Let members choose to stay paired</label>
    <div class="hint">On: each member chooses, for each server it knew only through the group, to stay paired or not. Off: those pairings end, on every member (for a group that was misused, say).</div>
  </FormModal>
{/if}

{#if open && giving}
  <FormModal title={`Give "${open.name}"`} description="The new owner names admins and the heir, changes the settings, lifts bans and may dissolve the group. You stay in it as a member: the new owner can make you an admin." saveLabel="Give" save={saveGiving} close={() => (giving = null)}>
    <Field label="To" wide>
      <select bind:value={giving.to}>
        <option value="">Who takes it by default: the heir, else the oldest admin, else the oldest member</option>
        {#each others(open) as m (m.id)}<option value={m.id}>{m.name}</option>{/each}
      </select>
    </Field>
  </FormModal>
{/if}

<style>
  .facts {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    margin-bottom: 1rem;
  }
  .fact {
    padding: 0;
    border: 0;
    background: none;
    min-height: 0;
    border-radius: 999px;
    cursor: pointer;
  }
  .fact:hover :global(.badge) {
    filter: brightness(1.15);
  }
  .member-name {
    font-weight: 600;
    margin-top: 0.4rem;
  }
  .fingerprint {
    white-space: nowrap;
    letter-spacing: 0.02em;
  }
  .actions {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    margin-bottom: 1rem;
  }
  .only-narrow {
    display: none;
  }
  .narrow-actions {
    margin-top: 0.4rem;
  }
  .narrow-actions :global(button) {
    margin-right: 0.4rem;
  }
  @media (max-width: 860px) {
    .only-narrow {
      display: block;
    }
    /* Requests and bans: the buttons under the server, so a phone shows
       them whole. */
    .stack-rows tr {
      display: flex;
      flex-direction: column;
    }
    .stack-rows thead th:last-child {
      display: none;
    }
    .stack-rows td.row-actions {
      border-top: 0;
      padding-top: 0;
      justify-content: flex-start;
      text-align: left;
    }
  }
</style>
