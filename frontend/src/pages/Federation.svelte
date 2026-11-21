<!-- Federation: pairing this server with other Overfin servers, by a
     one-time code one operator sends the other, then sharing library by
     library (ROADMAP.md milestone 6); and the servers' keys: this one's
     next key, changing it, revoking it when stolen, and the peers'; and
     whether this server can be called (a server that can't be watches,
     and shares nothing). -->
<script lang="ts">
  import { onDestroy } from 'svelte';
  import { PageHead, Card, Field, FormModal, Modal, Secret, Setting, Badge, Busy, Empty, Loading, Notice, ask, toast } from '@overfin/ui';
  import { api } from '../lib/api';
  import { ago, day, when, message, plural } from '../lib/format';
  import FederationGroups, { type Group, type JoinWaiting } from './FederationGroups.svelte';

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
    otherVersion?: boolean;
    wantsGone?: number;
    // Its keys: where its next key's commitment came from ('' for none);
    // a change of its key waiting for you; when it revoked its key; a
    // change refused. What it knows of ours: 'old' not moved yet, 'lost'
    // didn't move in time.
    commitment: '' | 'pair' | 'rotation';
    pending?: { fingerprint: string; date: string };
    suspendedAt?: string;
    refused?: { fingerprint: string; at: string };
    ourKey?: 'old' | 'lost';
    rotationAnswer?: 'pending' | 'refused';
    // Addresses: still calling this server's former address (late: for
    // some days); an address it says it moved to, being checked.
    notMoved?: boolean;
    moveLate?: boolean;
    moving?: { address: string; failing?: string; warn?: boolean };
    // It can't be called ('no'), or says it can again and its address is
    // being checked ('confirming'). callsFailed: its last answer said
    // this server's last calls to it couldn't connect.
    inbound?: 'no' | 'confirming';
    callsFailed?: boolean;
    // Relations (MSFP core section 7.7): the groups this pairing was made
    // or kept through; individual, also paired directly; asked, this server
    // asked to pair directly; asks, the other server asks; keepChoice, its
    // group was dissolved and you choose whether to stay paired.
    groups?: string[];
    individual: boolean;
    asked?: boolean;
    asks?: boolean;
    keepChoice?: boolean;
    // joiningGroup: it used an invite to that group, waiting for you.
    joiningGroup?: string;
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
    notSupported?: boolean;
    uncallable?: boolean;
    limits: Limits;
    theirLimits?: Limits;
  }
  interface Code {
    id: string;
    note: string;
    createdAt: string;
    expiresAt: string;
    // How many more servers may use it; the group it invites to.
    uses: number;
    group?: string;
  }
  interface State {
    available: boolean;
    // This server can't be called (its operator's setting), and whether
    // the panel can change it.
    uncallable?: boolean;
    canSet?: boolean;
    address?: string;
    testPlainHTTP?: boolean;
    fingerprint?: string;
    codes?: Code[];
    peers?: Peer[];
    groups?: Group[];
    // Codes can be emailed (the SMTP server is set).
    canMail?: boolean;
    nextKey?: { fingerprint: string; onServer: boolean };
    rotation?: { since: string; until: string };
    move?: { former: string; since: string; until: string; moved: number; of: number; waiting: boolean };
  }
  interface Preview {
    address: string;
    fingerprint: string;
    expiresAt: string;
    expired: boolean;
    // A group's code: the group it invites to.
    groupName?: string;
  }

  let s = $state<State | null>(null);
  let problem = $state('');
  // Making a code: its note, then the code itself, shown once.
  let making = $state(false);
  let note = $state('');
  // A group's code: the group's ID; how many servers may use it, and for
  // how many hours it works.
  let codeGroup = $state('');
  let uses = $state(1);
  let hours = $state(24);
  // Where the code is emailed too ("" for nowhere).
  let made = $state<{ id: string; note: string; code: string; uses: number; group?: string; expiresAt: string } | null>(null);
  // An invite shown again, to copy by hand; one being sent by email.
  let shown = $state<{ code: string; note: string } | null>(null);
  let sending = $state<{ invite: Code; email: string } | null>(null);
  // Using another server's code: pasted, then what it says, to confirm.
  let using = $state(false);
  let pasted = $state('');
  let preview = $state<Preview | null>(null);
  // Joining a group: what this server shares with it, and its limits.
  let joinOffer = $state<string[]>([]);
  let joinLibs = $state<Lib[]>([]);
  let joinLimits = $state<Limits>({ streams: 2, transcodes: 1, bitrateKbps: 0 });
  let pairing = $state(false);
  // Sharing with a paired server: what we offer, what we want.
  let sharingWith = $state<Peer | null>(null);
  let sharing = $state<Sharing | null>(null);
  let offer = $state<string[]>([]);
  let want = $state<string[]>([]);
  // Your names for the wanted libraries' entries on the site, by ID.
  let labels = $state<Record<string, string>>({});
  let limits = $state<Limits>({ streams: 2, transcodes: 1, bitrateKbps: 0 });
  // This server's keys: the next key shown once exported; changing the
  // key, or revoking it, in a window, with the next key pasted back when
  // it isn't on the server.
  let exported = $state<{ text: string; fingerprint: string } | null>(null);
  let changing = $state<'rotate' | 'revoke' | null>(null);
  let nextKeyText = $state('');
  let withoutNext = $state(false);

  // One load at a time: a slow answer doesn't pile up the next ones. Asked
  // during one (an action just done while the timer's was on its way), it
  // reads once more after it, so what the action changed shows, and the
  // action waits for that.
  let running: Promise<void> | null = null;
  let again = false;
  async function load() {
    if (running) {
      again = true;
      return running;
    }
    running = (async () => {
      do {
        again = false;
        try {
          s = await api<State>('GET', '/admin/api/federation');
          problem = '';
        } catch (err) {
          problem = message(err);
        }
      } while (again);
    })();
    try {
      await running;
    } finally {
      running = null;
    }
  }

  // The page reloads its lists as soon as anything changes in federation:
  // another server using an invite, an owner accepting, a request, a
  // group changed, titles read. It waits on the server for the next change
  // (up to 25 seconds, then asks again). Open windows keep what's typed in
  // them: only the lists change.
  let alive = true;
  let seen = 0;
  onDestroy(() => (alive = false));
  (async () => {
    while (alive) {
      try {
        const r = await api<{ seq: number }>('GET', `/admin/api/federation/changes?after=${seen}`);
        if (r.seq !== seen) {
          const first = seen === 0;
          seen = r.seq;
          if (!first) await load();
        }
      } catch {
        await new Promise((ok) => setTimeout(ok, 5000));
      }
    }
  })();

  function newCode(g?: Group) {
    note = '';
    codeGroup = g?.id ?? '';
    uses = 1;
    hours = 24;
    making = true;
  }

  async function make() {
    const c = await api<{ id: string; note: string; code: string; uses: number; group?: string; expiresAt: string }>('POST', '/admin/api/federation/codes', {
      note,
      group: codeGroup,
      uses: Number(uses) || 1,
      hours: Number(hours) || 24,
    });
    await load();
    setTimeout(() => (made = c), 0);
  }

  // Copies text: the clipboard where the browser allows it (https or
  // localhost), else the older way, which works on plain http too.
  async function copyText(text: string): Promise<boolean> {
    try {
      if (window.isSecureContext && navigator.clipboard) {
        await navigator.clipboard.writeText(text);
        return true;
      }
    } catch {
      // the older way below
    }
    const t = document.createElement('textarea');
    t.value = text;
    t.setAttribute('readonly', '');
    t.style.position = 'fixed';
    t.style.opacity = '0';
    document.body.appendChild(t);
    t.select();
    let ok = false;
    try {
      ok = document.execCommand('copy');
    } catch {
      ok = false;
    }
    t.remove();
    return ok;
  }

  // Copies an invite: its code is asked for again; when the browser won't
  // copy, it's shown, selected, to copy by hand.
  async function copyInvite(c: Code) {
    try {
      const r = await api<{ code: string }>('GET', `/admin/api/federation/codes/${c.id}/text`);
      if (await copyText(r.code)) toast('Invite copied.');
      else shown = { code: r.code, note: c.note };
    } catch (err) {
      toast(message(err), true);
    }
  }

  function sendInvite(c: Code) {
    sending = { invite: c, email: '' };
  }

  async function saveSending() {
    if (!sending) return;
    await api('POST', `/admin/api/federation/codes/${sending.invite.id}/email`, { email: sending.email.trim() });
    toast(`Sent to ${sending.email.trim()}.`);
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
    joinOffer = [];
    joinLimits = { streams: 2, transcodes: 1, bitrateKbps: 0 };
    joinLibs = p.groupName ? await api<Lib[]>('GET', '/admin/api/federation/libraries').catch(() => []) : [];
    setTimeout(() => (preview = p), 0);
  }

  async function pair() {
    pairing = true;
    try {
      const n = (v: unknown) => Number(v) || 0;
      await api('POST', '/admin/api/federation/peers', preview?.groupName
        ? { code: pasted, offer: joinOffer, limits: { streams: n(joinLimits.streams), transcodes: n(joinLimits.transcodes), bitrateKbps: n(joinLimits.bitrateKbps) } }
        : { code: pasted });
      toast(preview?.groupName ? `Sent. Once its owner accepts, this server joins "${preview.groupName}".` : "Sent. Now waiting for the other server's owner to accept.");
      preview = null;
    } catch (err) {
      toast(message(err), true);
    } finally {
      pairing = false;
    }
    await load();
  }

  async function accept(p: Peer) {
    const yes = await ask(
      `Accept ${p.name} (${p.address})? Compare its fingerprint with its operator first, by phone or a message you trust: ${p.fingerprint}. If they differ, refuse it.` +
        (p.address.startsWith('http://') ? ' Its address is plain http, accepted only because of the test setting test_plain_http: never on a server in use.' : ''),
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
    let what = p.state === 'ours' ? `Refuse ${p.name}?` : p.state === 'paired' ? `End the pairing with ${p.name}? It ends at once, on both servers.` : `Remove ${p.name}?`;
    let yes = p.state === 'ours' ? 'Refuse' : p.state === 'paired' ? 'End' : 'Remove';
    if (p.state === 'paired') {
      // Its last pairing through a group: that group can't reach this
      // server afterwards (MSFP groups section 6, "No way in").
      const cuts = await api<{ id: string; name: string }[]>('GET', `/admin/api/federation/peers/${p.id}/cuts`).catch(() => []);
      if (cuts.length) {
        const names = cuts.map((g) => `"${g.name}"`).join(', ');
        const one = cuts.length === 1;
        what = `${p.name} is this server's only pairing through ${one ? 'the group' : 'the groups'} ${names}. Once it ends, nothing from ${one ? 'that group' : 'those groups'} reaches this server, and nothing from this server reaches ${one ? 'it' : 'them'}. To stop being part of ${one ? 'it' : 'them'}, leave instead: open the group under Groups and choose Leave.`;
        yes = 'End anyway';
      }
    }
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

  async function exportNextKey() {
    const e = await api<{ text: string; fingerprint: string }>('POST', '/admin/api/federation/next-key/export', {});
    exported = e;
  }

  async function deleteNextKey() {
    const yes = await ask(
      "Delete the next key from this server? Keep the copy you made somewhere safe, away from the server: changing this server's key, or recovering from a stolen one, will need it. Someone who copies the server's storage then can't change its key.",
      { yes: 'Delete it from the server', danger: true },
    );
    if (!yes) return;
    try {
      await api('DELETE', '/admin/api/federation/next-key');
      exported = null;
      toast('The next key is no longer on this server.');
    } catch (err) {
      toast(message(err), true);
    }
    await load();
  }

  function change(what: 'rotate' | 'revoke') {
    nextKeyText = '';
    withoutNext = false;
    changing = what;
  }

  async function saveChange() {
    if (changing === 'rotate') {
      await api('POST', '/admin/api/federation/key/rotate', { nextKey: nextKeyText.trim() });
      toast("This server's key changed. Paired servers are told.");
    } else {
      const r = await api<{ rotated: boolean }>('POST', '/admin/api/federation/key/revoke', { nextKey: nextKeyText.trim(), withoutNextKey: withoutNext });
      toast(r.rotated ? "The stolen key is revoked, and this server moved to its next key." : 'The stolen key is revoked. Every pairing ended: pair again with new codes.');
    }
    await load();
  }

  async function setUncallable(e: Event) {
    const box = e.currentTarget as HTMLInputElement;
    const on = box.checked;
    const yes = await ask(
      on
        ? "Turn on \"This server can't be called\"? Paired servers then never connect to this one: it can watch what they share with it, and shares nothing with them. It asks each of them every few minutes instead. It makes no codes: ask the other server's operator for one."
        : "Turn off \"This server can't be called\"? Paired servers are told this server's address, check that it answers there, then call it again: only if they can reach it.",
      { yes: on ? 'Turn on' : 'Turn off' },
    );
    if (!yes) {
      box.checked = !on;
      return;
    }
    try {
      await api('PUT', '/admin/api/federation/uncallable', { on });
      toast(on ? "This server can't be called now. Paired servers are told at its next message to each." : 'This server can be called again. Paired servers are told now.');
    } catch (err) {
      box.checked = !on;
      toast(message(err), true);
    }
    await load();
  }

  async function confirmKey(p: Peer) {
    if (!p.pending) return;
    const yes = await ask(
      `${p.name} changed its key. Confirm the new key's fingerprint with its operator first, by phone or a message you trust: ${p.pending.fingerprint}. If they differ, don't confirm: end the pairing instead.`,
      { yes: 'Confirm the new key' },
    );
    if (!yes) return;
    try {
      await api('POST', `/admin/api/federation/peers/${p.id}/confirm-key`, {});
      toast(`${p.name}'s new key confirmed.`);
    } catch (err) {
      toast(message(err), true);
    }
    await load();
  }

  async function dismiss(p: Peer) {
    await api('DELETE', `/admin/api/federation/peers/${p.id}/refused`);
    await load();
  }

  // A peer's key, in a word: what the Key column shows.
  function keyState(p: Peer): { text: string; kind: '' | 'ok' | 'warn' | 'err' | 'info' } {
    if (p.suspendedAt) return { text: 'Suspended', kind: 'err' };
    if (p.pending) return { text: 'To confirm', kind: 'warn' };
    if (p.ourKey === 'lost') return { text: 'Pair again', kind: 'err' };
    if (p.ourKey === 'old') return { text: 'Changing', kind: 'info' };
    return { text: 'Current', kind: 'ok' };
  }

  // What its keys need said: a warning first.
  function keyText(p: Peer): string {
    if (p.suspendedAt && p.state === 'gone')
      return `Its key was revoked ${day(p.suspendedAt)}, and no new key followed within 30 days. Remove it, and pair again if you wish.`;
    if (p.suspendedAt)
      return `Its key was revoked ${day(p.suspendedAt)}, perhaps stolen: nothing is shared with it until it moves to the next key it committed to, or within 30 days it's gone.`;
    if (p.pending) return `It changed its key: confirm the new fingerprint with its operator (${p.pending.fingerprint}).`;
    if (p.ourKey === 'lost') return "It didn't move to this server's new key within 30 days: remove it, and pair again.";
    if (p.ourKey === 'old' && p.rotationAnswer === 'pending') return "Its operator has to confirm this server's new key.";
    if (p.ourKey === 'old' && p.rotationAnswer === 'refused') return "It refused this server's new key: pair again if it doesn't move.";
    if (p.ourKey === 'old') return "Not moved to this server's new key yet: it is told at each contact.";
    if (!p.commitment && p.state === 'paired') return 'Its next key is unknown: a change of its key will wait for you to confirm it.';
    return '';
  }

  // What its address needs said, "" for nothing.
  function addressText(p: Peer): string {
    if (p.inbound === 'no') return "This server can't be called: it can watch what you share with it, and can share nothing with you.";
    if (p.inbound === 'confirming') return 'It says it can be called again: checking it answers at its address first.';
    if (p.callsFailed && !s?.uncallable)
      return "It says this server's last calls to it couldn't connect. If other servers can't reach yours (at home behind a router, say), turn on \"This server can't be called\" above.";
    if (p.moving?.failing)
      return `It says it moved to ${p.moving.address}, which hasn't answered as it since ${day(p.moving.failing)}: it's still reached at its old address, and tried again every hour.`;
    if (p.moving) return `It says it moved to ${p.moving.address}: checking it answers there.`;
    if (p.moveLate) return `Still reaches this server at its former address, ${s?.move?.former ?? ''}: ask its operator whether it can reach the new one.`;
    if (p.notMoved) return "Still reaches this server at its former address: it's told the new one at each contact.";
    return '';
  }

  const attention = $derived((s?.peers ?? []).filter((p) => p.suspendedAt || p.pending || p.refused || p.ourKey === 'lost' || p.moving?.warn || p.moveLate || (p.callsFailed && !s?.uncallable)));

  const kinds: Record<string, string> = { movies: 'Films', tvshows: 'Series', music: 'Music', musicvideos: 'Music videos', homevideos: 'Home videos', boxsets: 'Collections', books: 'Books', photos: 'Photos', mixed: 'Mixed' };

  const limitsText = (l: Limits) =>
    `${plural(l.streams, 'stream')} at once, ${plural(l.transcodes, 'transcode')} among them, ${l.bitrateKbps ? `${l.bitrateKbps} kbps at most` : 'no bitrate cap'}`;

  const shareText = (p: Peer) =>
    `Offers ${plural(p.offers, 'library', 'libraries')} · wants ${p.wants}${p.wants ? ` (${plural(p.titles, 'title')})` : ''}` +
    (p.wantsGone ? ` · ${p.wantsGone} no longer offered: choose again in Sharing` : '');
  // The kind, unless the name already says it ("Films").
  const kindOf = (l: Lib) => (kinds[l.kind] && kinds[l.kind].toLowerCase() !== l.name.toLowerCase() ? kinds[l.kind] : '');

  function stateText(p: Peer): string {
    if (p.otherVersion) return "It speaks another version of the federation protocol: the two servers can't share until one of them is updated";
    switch (p.state) {
      case 'ours':
        return 'Waiting for you to accept';
      case 'theirs':
        return 'Waiting for its operator to accept';
      case 'paired':
        if (p.keepChoice) return 'Its group was closed: stay paired with it?';
        if (p.asks) return `Asks to pair with you directly; you know it through ${groupNames(p)}`;
        if (p.asked) return 'You asked to pair directly: waiting for its operator';
        return `Paired ${p.pairedAt ? day(p.pairedAt) : ''}`.trim();
      case 'gone':
        return 'It no longer knows this server';
    }
  }

  // Servers paired directly, asked, or asking: the Servers list. Those
  // known only through a group are under that group.
  const direct = $derived((s?.peers ?? []).filter((p) => !p.joiningGroup && (p.state !== 'paired' || p.individual || p.asked || p.asks || p.keepChoice)));
  // Servers that used an invite to one of your groups: shown in that group.
  const joiningPeers = $derived((s?.peers ?? []).filter((p) => p.state === 'ours' && !!p.joiningGroup));
  const personalInvites = $derived((s?.codes ?? []).filter((c) => !c.group));

  async function relation(p: Peer, path: 'individual' | 'unpair' | 'refuse' | 'block', done: string) {
    try {
      await api('POST', `/admin/api/federation/peers/${p.id}/${path}`, {});
      toast(done);
    } catch (err) {
      toast(message(err), true);
    }
    await load();
  }

  async function pairDirectly(p: Peer) {
    const yes = await ask(`${p.asks ? `Pair directly with ${p.name}?` : `Ask ${p.name} to pair directly? Its operator answers on their side.`} Its fingerprint, to compare with its operator if you like: ${p.fingerprint}`, { yes: p.asks ? 'Pair' : 'Ask' });
    if (yes) await relation(p, 'individual', p.asks ? `Paired directly with ${p.name}.` : `${p.name} was asked.`);
  }

  async function unpair(p: Peer) {
    const what = p.asked && !p.individual ? `Withdraw your ask to pair directly with ${p.name}?`
      : p.groups?.length ? `Unpair from ${p.name}? You stay paired through ${groupNames(p)}, and it keeps what you share with the group.`
      : `Unpair from ${p.name}? It ends on both servers; a new code pairs you again.`;
    if (await ask(what, { yes: p.asked && !p.individual ? 'Withdraw' : 'Unpair', danger: true })) await relation(p, 'unpair', p.asked && !p.individual ? 'Ask withdrawn.' : 'Unpaired.');
  }

  async function block(p: Peer) {
    const groups = p.groups?.length ? ` It is also cut off from you in ${groupNames(p)}.` : '';
    if (await ask(`Block ${p.name}? Everything with it ends, and it is kept out of all your groups. Only a new code from you lets it back.${groups}`, { yes: 'Block', danger: true })) await relation(p, 'block', `${p.name} is blocked.`);
  }

  function manageById(id: string) {
    const p = (s?.peers ?? []).find((x) => x.id === id);
    if (p) openSharing(p);
  }

  // The groups you may invite to: as owner or admin.
  const invitable = $derived((s?.groups ?? []).filter((g) => (g.role === 'owner' || g.role === 'admin') && !g.dissolved));
  const groupName = (id: string) => (s?.groups ?? []).find((g) => g.id === id)?.name ?? 'a group';
  const groupNames = (p: Peer) => (p.groups ?? []).map((id) => `"${groupName(id)}"`).join(', ');

  load();
</script>

<PageHead title="Federation" description="Share with other servers: one by one, or in groups that share with each other as one. Invite a server or create a group, and send the invite to the other server's owner; a server that uses it waits for you to accept it, after you both compared fingerprints. What they share then shows on your site beside your titles, and plays straight from them.">
  {#snippet actions()}
    {#if s?.available}
      <button type="button" class={s.uncallable ? 'primary' : ''} onclick={useCode}>Use a code</button>
    {/if}
  {/snippet}
</PageHead>

{#if problem}
  <Notice warn>{problem}</Notice>
{:else if !s}
  <Loading />
{:else if !s.available}
  <Notice warn>Federation needs the site's https address, which codes give to other servers: set <code>site.url</code> in the configuration (or an HTTPS domain), as https://host with nothing after it. Servers pair over HTTPS only. A server other servers can't reach needs none: turn on "This server can't be called".</Notice>
  {#if s.canSet}
    <Card title="This server">
      {@render uncallableSetting()}
    </Card>
  {/if}
{:else}
  <Card title="This server">
    {#if s.canSet}{@render uncallableSetting()}{/if}
    <Setting label="Address" hint={s.uncallable ? "One that leads nowhere, made for this server: nothing connects to it. Other servers know this server by its key." : 'Where other servers reach this one, as your invites give it.'}>
      {#snippet children()}<code>{s?.address}</code>{/snippet}
    </Setting>
    <Setting stack label="Fingerprint" hint="What the other server's owner compares, to be sure the invite is really this server's.">
      {#snippet children()}<code class="fingerprint">{s?.fingerprint}</code>{/snippet}
    </Setting>
    {#if s.nextKey}
      <Setting stack label={s.nextKey.onServer ? 'Next key, on this server' : 'Next key, kept offline'} hint={s.nextKey.onServer ? "Made in advance, for changing this server's key; paired servers know it is the next one. Export it to keep offline, then delete it from the server: someone who copies the server's storage then can't change its key." : "Kept offline: it's no longer on this server. Changing the key, or recovering from a stolen one, needs the copy you exported."}>
        {#snippet children()}<code class="fingerprint">{s?.nextKey?.fingerprint}</code>{/snippet}
      </Setting>
    {/if}
    {#if s.move}
      {#if s.move.waiting}
        <Notice warn>This server's address changed {day(s.move.since)}, from <code>{s.move.former}</code>, waiting for the new address to answer: paired servers are told only once this server reaches itself at <code>{s.address}</code>, tried again and again. Until then they keep calling the former address.</Notice>
      {:else}
        <Notice>This server's address changed {day(s.move.since)}, from <code>{s.move.former}</code>. Paired servers are told, and move by themselves: {s.move.moved} of {s.move.of} {s.move.moved === 1 ? 'has' : 'have'}. The former address is still accepted for those that haven't, until {when(s.move.until)}, or until all have moved.</Notice>
      {/if}
    {/if}
    {#if s.rotation}
      <Notice>This server's key is changing since {day(s.rotation.since)}. Servers that haven't moved yet are told at each contact; the old key is kept for them until {when(s.rotation.until)}, then they must pair again.</Notice>
    {/if}
    {#if s.uncallable}
      <Notice>This server can't be called: it can watch what paired servers share with it, and shares nothing. It makes no codes: ask the other server's operator for one, and use it here.</Notice>
    {/if}
    {#if s.testPlainHTTP}
      <Notice warn>The address is plain http, accepted only because the test setting <code>federation.test_plain_http</code> is on: what servers say can be read on the way. Never on a server in use.</Notice>
    {/if}
    {#snippet foot()}
      <span class="grow"></span>
      {#if s?.nextKey?.onServer}<Busy action={exportNextKey}>Export the next key</Busy>{/if}
      <button type="button" onclick={() => change('rotate')}>Change this server's key</button>
      <button type="button" class="danger" onclick={() => change('revoke')}>This server's key was stolen</button>
    {/snippet}
  </Card>

  {#if attention.length}
    <Notice warn>
      {#each attention as p (p.id)}
        <div><strong>{p.name}</strong>: {p.refused && !p.suspendedAt && !p.pending ? `someone holding its key tried, ${ago(p.refused.at)}, to move the pairing to another key (${p.refused.fingerprint}). It was refused. Its operator may have lost control of its key: ask them.` : p.suspendedAt || p.pending || p.ourKey === 'lost' ? keyText(p) : addressText(p)}</div>
      {/each}
    </Notice>
  {/if}

  <Card title="Individual" description="Servers you share with one by one.">
    {#snippet foot()}
      <span class="grow"></span>
      {#if !s?.uncallable}<button type="button" class="primary" onclick={() => newCode()}>Invite a server</button>{/if}
    {/snippet}
    <div class="table-wrap">
      <table>
        <thead><tr><th>Server</th><th class="hide-narrow">Status</th><th class="hide-narrow">Key</th><th><span class="visually-hidden">Actions</span></th></tr></thead>
        <tbody>
          {#each direct as p (p.id)}
            <tr>
              <td><div class="name">{p.name}</div><div class="hint address">{p.address}{#if p.address.startsWith('http://')}{' · plain http (test setting)'}{/if}</div>{#if p.groups?.length}<div class="hint">{p.individual ? 'Also in ' : 'Through '}{groupNames(p)}</div>{/if}{#if addressText(p)}<div class="hint">{addressText(p)}</div>{/if}<div class="only-narrow">{stateText(p)}</div>{#if p.state === 'paired'}<div class="hint only-narrow">{shareText(p)}</div>{/if}</td>
              <td class="hide-narrow"><div>{stateText(p)}</div>{#if p.state === 'paired'}<div class="hint">{shareText(p)}</div>{/if}{#if p.seenAt}<div class="hint">heard from {ago(p.seenAt)}</div>{/if}</td>
              <td class="hide-narrow">{#if p.inbound === 'no'}<div><Badge kind="info">Can't be called</Badge></div>{/if}<div><Badge kind={keyState(p).kind}>{keyState(p).text}</Badge></div><div><code class="fingerprint">{p.fingerprint}</code></div>{#if keyText(p)}<div class="hint key-text">{keyText(p)}</div>{/if}</td>
              <td class="row-actions">
                {#if p.pending && !p.suspendedAt}
                  <button type="button" class="small primary" onclick={() => confirmKey(p)}>Confirm key</button>
                {/if}
                {#if p.refused}
                  <button type="button" class="small" onclick={() => dismiss(p)}>Dismiss</button>
                {/if}
                {#if p.state === 'ours'}
                  <button type="button" class="small primary" onclick={() => accept(p)}>Accept</button>
                  <button type="button" class="small danger" onclick={() => end(p)}>Refuse</button>
                {:else if p.state === 'paired' && (p.asks || p.keepChoice)}
                  <button type="button" class="small primary" onclick={() => (p.asks ? pairDirectly(p) : relation(p, 'individual', `Still paired with ${p.name}.`))}>{p.asks ? 'Accept' : 'Keep'}</button>
                  <button type="button" class="small danger" onclick={() => relation(p, 'refuse', p.asks ? 'Refused.' : `Unpaired from ${p.name}.`)}>{p.asks ? 'Refuse' : 'Let go'}</button>
                {:else if p.state === 'paired' && !p.suspendedAt && p.ourKey !== 'lost'}
                  <button type="button" class="small primary" onclick={() => openSharing(p)}>Sharing</button>
                  {#if !p.inbound}<button type="button" class="small" onclick={() => check(p)}>Check</button>{/if}
                  <button type="button" class="small danger" onclick={() => unpair(p)}>{p.asked && !p.individual ? 'Withdraw' : 'Unpair'}</button>
                  <button type="button" class="small danger" onclick={() => block(p)}>Block</button>
                {:else if p.suspendedAt || p.ourKey === 'lost'}
                  <button type="button" class="small danger" onclick={() => end(p)}>Remove</button>
                {:else}
                  {#if !p.inbound}<button type="button" class="small" onclick={() => check(p)}>Check</button>{/if}
                  <button type="button" class="small danger" onclick={() => end(p)}>Remove</button>
                {/if}
              </td>
            </tr>
          {:else}
            <tr><td colspan="4"><Empty>{s.uncallable ? "No server yet: use another server's invite (Use a code, at the top)." : "No server yet: invite one, or use another server's invite (Use a code, at the top)."}</Empty></td></tr>
          {/each}
        </tbody>
      </table>
    </div>
    {#if personalInvites.length}
      <div class="label invites-title">Invites not used yet</div>
      <div class="table-wrap">
        <table>
          <tbody>
            {#each personalInvites as c (c.id)}
              <tr>
                <td><div class="name">{c.note || 'No note'}</div><div class="hint">made {ago(c.createdAt)} · works until {when(c.expiresAt)}</div></td>
                <td class="row-actions">
                  <button type="button" class="small" onclick={() => copyInvite(c)}>Copy</button>
                  {#if s?.canMail}<button type="button" class="small" onclick={() => sendInvite(c)}>Send by email</button>{/if}
                  <button type="button" class="small danger" onclick={() => withdraw(c)}>Withdraw</button>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Card>

  <FederationGroups groups={s.groups ?? []} uncallable={s.uncallable} reload={load} invite={newCode}
    pairDirectly={(id) => { const p = (s?.peers ?? []).find((x) => x.id === id); if (p) pairDirectly(p); }}
    joining={joiningPeers} invites={s.codes ?? []}
    acceptJoining={(j: JoinWaiting) => { const p = (s?.peers ?? []).find((x) => x.id === j.id); if (p) accept(p); }}
    refuseJoining={(j: JoinWaiting) => { const p = (s?.peers ?? []).find((x) => x.id === j.id); if (p) end(p); }}
    withdraw={withdraw} copyInvite={copyInvite} sendInvite={s.canMail ? sendInvite : undefined} />

{/if}

{#if exported}
  <Modal title="Next key" close={() => (exported = null)}>
    <Secret value={exported.text} after="Keep it offline, somewhere safe: a password manager, or on paper. Then delete it from this server.">Copy it now: whoever holds it, with this server's current key, can change this server's key.</Secret>
    <Setting stack label="Its fingerprint" hint="The fingerprint of the key paired servers expect next: the same as on this page.">
      {#snippet children()}<code class="fingerprint">{exported?.fingerprint}</code>{/snippet}
    </Setting>
    {#snippet foot()}
      <button type="button" class="danger" onclick={deleteNextKey}>Delete it from this server</button>
      <span class="grow"></span>
      <button type="button" class="primary" onclick={() => (exported = null)}>Done</button>
    {/snippet}
  </Modal>
{/if}

{#if changing === 'rotate'}
  <FormModal title="Change this server's key" description={`This server moves to its next key (${s?.nextKey?.fingerprint ?? ''}), and makes a new next key. Paired servers that know it as the next one follow at once; the others' operators confirm its fingerprint. The old key is kept 30 days at most for servers that haven't moved yet.`} saveLabel="Change the key" save={saveChange} close={() => (changing = null)}>
    {#if s?.rotation}<Notice warn>A change of key is still under way: servers that haven't moved from the key before will have to pair again.</Notice>{/if}
    {#if !s?.nextKey?.onServer}
      <Field label="Next key" wide hint="The next key isn't on this server any more: paste the copy you exported, starting with msfp-next-key-1.."><textarea bind:value={nextKeyText} rows="2" spellcheck="false" autocomplete="off" required></textarea></Field>
    {/if}
  </FormModal>
{/if}

{#if changing === 'revoke'}
  <FormModal danger title="This server's key was stolen" description="Paired servers are told at once that this server's key is revoked: they stop hearing it. With the next key, this server then moves to it, and servers that know it as the next one follow by themselves. Without it, this server gets a new key, and every pairing ends: pair again with new codes, comparing fingerprints." saveLabel="Revoke the key" save={saveChange} close={() => (changing = null)}>
    {#if s?.nextKey?.onServer}
      <label class="inline"><input type="checkbox" bind:checked={withoutNext} /> Don't move to the next key</label>
      <div class="hint">The next key is on this server: if the server's storage was copied, the thief may hold it too. Then turn this on: every pairing ends, to make again.</div>
    {:else}
      <Field label="Next key, if you kept it" wide hint="The copy you exported, starting with msfp-next-key-1.. Leave it empty if you don't have it: every pairing then ends."><textarea bind:value={nextKeyText} rows="2" spellcheck="false" autocomplete="off"></textarea></Field>
    {/if}
  </FormModal>
{/if}

{#if making}
  <FormModal title={codeGroup ? `Invite to "${groupName(codeGroup)}"` : 'Invite a server'} description={codeGroup ? "The server's owner uses it on their server; you accept it, after comparing fingerprints, and it joins the group." : "The server's owner uses it on their server; you accept it, after comparing fingerprints, and you share with each other one by one."} saveLabel="Make the invite" save={make} close={() => (making = false)}>
    <Field label="For" wide hint="A note for you, to tell your invites apart."><input type="text" bind:value={note} placeholder="e.g. Marc's server" maxlength="80" /></Field>
    {#if codeGroup}
      <Setting label="Servers that may use it" hint="1 to 100. Each one still waits for you to accept it.">
        {#snippet children(ids)}<input type="number" min="1" max="100" bind:value={uses} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
    {/if}
    <Setting label="Works for" hint="Then it stops working, used or not.">
      {#snippet children(ids)}
        <select bind:value={hours} aria-labelledby={ids.label} aria-describedby={ids.hint}>
          <option value={1}>1 hour</option>
          <option value={24}>1 day</option>
          <option value={72}>3 days</option>
          <option value={168}>1 week</option>
          <option value={720}>30 days</option>
        </select>
      {/snippet}
    </Setting>

  </FormModal>
{/if}

{#if made}
  <Modal title={made.group ? `Invite to "${groupName(made.group)}"` : 'Invite'} close={() => (made = null)}>
    <div class="hint">Send it to the other server's owner however you like; they use it on their server (on Overfin: Federation, "Use a code"). Their server then waits for you {made.group ? `in "${groupName(made.group)}"` : 'under Individual'}, to accept once you've compared fingerprints. It works {made.uses > 1 ? `for ${made.uses} servers` : 'once'}, until {when(made.expiresAt)}. You find it under "Invites not used yet" until then.</div>
    <pre class="invite-code">{made.code}</pre>
    {#snippet foot()}
      <span class="grow"></span>
      {#if s?.canMail}<button type="button" onclick={() => { const c = (s?.codes ?? []).find((x) => x.id === made?.id); made = null; if (c) sendInvite(c); }}>Send by email</button>{/if}
      <button type="button" onclick={async () => { if (made && (await copyText(made.code))) toast('Invite copied.'); }}>Copy</button>
      <button type="button" class="primary" onclick={() => (made = null)}>Done</button>
    {/snippet}
  </Modal>
{/if}

{#if shown}
  <Modal title={shown.note || 'Invite'} close={() => (shown = null)}>
    <div class="hint">Your browser didn't let the page copy it: select it and copy it yourself.</div>
    <pre class="invite-code">{shown.code}</pre>
    {#snippet foot()}
      <span class="grow"></span>
      <button type="button" class="primary" onclick={() => (shown = null)}>Done</button>
    {/snippet}
  </Modal>
{/if}

{#if sending}
  <FormModal title="Send by email" description={`The invite "${sending.invite.note || 'No note'}", to the other server's owner. Compare fingerprints with them by phone or another way you trust, not by email.`} saveLabel="Send" save={saveSending} close={() => (sending = null)}>
    <Field label="To" wide><input type="email" bind:value={sending.email} placeholder="owner@example.org" maxlength="254" autocomplete="off" required /></Field>
  </FormModal>
{/if}

{#if using}
  <FormModal title="Use a code" description="The invite another server's owner sent you: to pair one by one, or to join a group." saveLabel="Read the code" save={read} close={() => (using = false)}>
    <Field label="Code" wide><textarea bind:value={pasted} rows="4" spellcheck="false" autocomplete="off" placeholder="msfp-pair-0.…" required></textarea></Field>
  </FormModal>
{/if}

{#if sharingWith && sharing}
  <FormModal title={`Sharing with ${sharingWith.name}`} description="Only what one server offers and the other wants is shared." saveLabel="Save" save={saveSharing} close={() => { sharingWith = null; sharing = null; }}>
    {#if s?.uncallable}<Notice>This server can't be called: it shares nothing. What you pick here is kept for when it can be called again.</Notice>{/if}
    {#if !sharingWith.individual}
      <Notice>{sharingWith.asked ? `You asked ${sharingWith.name} to pair directly; until its operator accepts, it gets` : `You know ${sharingWith.name} through ${groupNames(sharingWith)}: it gets`} what you share with the group, under the group's Sharing. Pair directly to share with it on its own.</Notice>
    {:else}
    <div class="setting stack">
      <div><div class="label">You offer</div><div class="hint">Your libraries {sharingWith.name} may take. You only ever offer your own.</div></div>
      <div class="checks all">
        {#each sharing.ours as l (l.id)}<label class="inline"><input type="checkbox" value={l.id} bind:group={offer} /> {l.name}{#if kindOf(l)} <span class="hint">({kindOf(l)})</span>{/if}</label>
        {:else}<span class="hint">This server has no library yet.</span>{/each}
      </div>
    </div>
    {/if}
    <div class="setting stack">
      <div><div class="label">You want</div><div class="hint">Out of what {sharingWith.name} offers you.</div></div>
      {#if sharing.uncallable}<Notice>{sharingWith.name} can't be called: it can watch what you share with it, and can share nothing with you.</Notice>
      {:else if sharing.notSupported}<Notice warn>{sharingWith.name} doesn't share libraries with this server: it speaks another version of the federation protocol, or hasn't said which yet (Check asks it). What you want can't be changed until it does.</Notice>
      {:else if !sharing.reachable}<Notice warn>{sharingWith.name} can't be reached now: what you want can't be changed until it can.</Notice>{/if}
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
    {#if sharingWith.individual}
    <Setting label="Streams at once" hint={`What ${sharingWith.name}'s people may play from this server at the same time.`}>
      {#snippet children(ids)}<input type="number" min="0" max="50" bind:value={limits.streams} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
    </Setting>
    <Setting label="Transcodes among them" hint="Re-encoded video, which is what costs. Your server's own reserve (Features & limits) applies too.">
      {#snippet children(ids)}<input type="number" min="0" max="50" bind:value={limits.transcodes} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
    </Setting>
    <Setting label="Bitrate cap (kbps)" hint="0: no cap.">
      {#snippet children(ids)}<input type="number" min="0" bind:value={limits.bitrateKbps} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
    </Setting>
    {/if}
  </FormModal>
{/if}

{#if preview}
  <Modal title={preview.groupName ? `Join the group "${preview.groupName}"?` : 'Pair with this server?'} close={() => (preview = null)}>
    {#if preview.groupName}
      <Notice>An invite to the group "{preview.groupName}", from the server below. Once its owner accepts, this server joins the group and asks every member to pair.</Notice>
    {/if}
    <Setting label="Address">
      {#snippet children()}<code>{preview?.address}</code>{/snippet}
    </Setting>
    {#if preview?.address?.startsWith('http://')}
      <Notice warn>That server's address is plain http, accepted only because the test setting <code>federation.test_plain_http</code> is on here. Never on a server in use.</Notice>
    {/if}
    <Setting stack label="Fingerprint" hint="Compare it with the other server's owner, by phone or a message you trust: if it differs, the invite was changed on the way.">
      {#snippet children()}<code class="fingerprint">{preview?.fingerprint}</code>{/snippet}
    </Setting>
    {#if preview.expired}
      <Notice warn>This invite stopped working {ago(preview.expiresAt)}: ask for a new one.</Notice>
    {/if}
    {#if preview.groupName && !preview.expired}
      <div class="setting stack">
        <div><div class="label">You share with "{preview.groupName}"</div><div class="hint">Your libraries, for all the group's members. You can change it later in the group.</div></div>
        <div class="checks all">
          {#each joinLibs as l (l.id)}<label class="inline"><input type="checkbox" value={l.id} bind:group={joinOffer} /> {l.name}</label>
          {:else}<span class="hint">This server has no library yet.</span>{/each}
        </div>
      </div>
      <Setting label="Streams at once" hint="For all the group's members together.">
        {#snippet children(ids)}<input type="number" min="0" max="50" bind:value={joinLimits.streams} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
      <Setting label="Transcodes among them" hint="Re-encoded video, which is what costs.">
        {#snippet children(ids)}<input type="number" min="0" max="50" bind:value={joinLimits.transcodes} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
    {/if}
    {#snippet foot()}
      <span class="grow"></span>
      <button type="button" onclick={() => (preview = null)}>Cancel</button>
      <button type="button" class="primary" disabled={!preview || preview.expired || pairing} onclick={pair}>{preview?.groupName ? 'Join' : 'Pair'}</button>
    {/snippet}
  </Modal>
{/if}

{#snippet uncallableSetting()}
  <Setting label="This server can't be called" hint="Turn on when other servers can't connect to this one: at home behind a router, without a forwarded port, say. This server can then watch what paired servers share with it, and share nothing: they never call it, and it asks each of them every few minutes instead.">
    {#snippet children(ids)}<input type="checkbox" checked={s?.uncallable ?? false} onchange={setUncallable} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
  </Setting>
{/snippet}

<style>
  .fingerprint {
    white-space: nowrap;
    letter-spacing: 0.02em;
  }
  .only-narrow {
    display: none;
  }
  .invite-code {
    white-space: pre-wrap;
    word-break: break-all;
    user-select: all;
    font-family: ui-monospace, "SF Mono", Menlo, monospace;
    font-size: 0.85rem;
    padding: 0.6rem 0.75rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-s);
    background: var(--panel-2);
    margin: 0.75rem 0 0;
  }
  .invites-title {
    font-weight: 600;
    margin: 1rem 0 0.4rem;
  }
  .key-text {
    max-width: 28em;
  }
  /* A server that can't be called has a long address in one piece: it
     wraps anywhere, so that a phone still shows the row's buttons. */
  .address {
    overflow-wrap: anywhere;
  }
  @media (max-width: 860px) {
    .only-narrow {
      display: block;
    }
  }
</style>
