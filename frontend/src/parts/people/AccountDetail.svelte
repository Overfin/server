<!-- The selected account: who, how they sign in, access, profiles,
     devices, and the destructive actions, set apart. -->
<script lang="ts">
  import { Avatar, Badge, Busy, ask, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { ago, when } from '../../lib/format';
  import type { Template } from '../../lib/types';
  import type { Account, Person } from '../../lib/types-people';
  import { lastActive, nameOf, statusKind, statusText, subline } from './names';
  import PersonControls from './PersonControls.svelte';
  import AccessForm from './AccessForm.svelte';

  let { account: a, templates, people, reload }: {
    account: Account;
    templates: Template[];
    people: Person[] | null;
    reload: (id?: string) => void;
  } = $props();

  let editing = $state(false);
  const signsIn = $derived(people?.find((x) => x.accountId === a.id));

  async function patch(change: Record<string, unknown>, done: string) {
    await api('PATCH', `/admin/api/accounts/${a.id}`, change);
    toast(done);
    reload(a.id);
  }

  async function unlock(p: Account['profiles'][number]) {
    await api('POST', `/admin/api/profiles/${p.id}/unlock-pin`, {});
    toast(`${p.name}'s PIN is unlocked.`);
    reload(a.id);
  }

  async function signOut(s: Account['sessions'][number]) {
    await api('DELETE', `/admin/api/accounts/${a.id}/sessions/${s.id}`);
    toast('Signed out.');
    reload(a.id);
  }

  async function remove() {
    if (!(await ask(`Delete ${nameOf(a)}'s account, its profiles and what they hold? This can't be undone.`, { yes: 'Delete', danger: true }))) return;
    await api('DELETE', `/admin/api/accounts/${a.id}`);
    toast('Account deleted.');
    reload('');
  }

  // historyCopy: the account brought a history from another server; it may
  // bring one again, if the wrong one came, or the wrong person took
  // someone's.
  async function allowAgain() {
    if (!(await ask('Let this account bring a history again? What was copied stays; the account it came from can be copied again, by anyone with its login.', { yes: 'Allow' }))) return;
    await api('DELETE', `/admin/api/accounts/${a.id}/history-copy`);
    toast('This account may bring a history again.');
    reload(a.id);
  }

  const profileHint = (p: Account['profiles'][number]) => [
    p.kind === 'kids' && (p.maxAge !== undefined ? `Up to ${p.maxAge} years` : 'Age not chosen yet'),
    p.hasPin ? (p.pinLocked ? 'PIN locked' : 'PIN set') : 'No PIN',
  ].filter(Boolean).join(' · ');
</script>

<div class="detail-head">
  <Avatar name={nameOf(a)} big />
  <div>
    <h2>{nameOf(a)}</h2>
    <div class="hint">{subline(a) || a.person?.email || ''}</div>
  </div>
</div>
<div>
  <Badge kind={statusKind[a.status] ?? ''}>{statusText[a.status] || a.status}</Badge>
  {#if a.person && !a.person.active}{' '}<Badge kind="err">Off at the identity provider</Badge>{/if}
</div>

<h3>About</h3>
<dl class="facts">
  <dt>Since</dt><dd>{when(a.createdAt)}</dd>
  <dt>Last active</dt><dd>{ago(lastActive(a)) || 'never'}</dd>
  <dt>Sign-in ID</dt><dd class="hint">{a.subject}</dd>
</dl>

{#if signsIn}
  <h3>Signing in</h3>
  <div class="box"><PersonControls person={signsIn} reload={() => reload(a.id)} /></div>
{/if}

<h3>Access</h3>
<dl class="facts">
  {#if !a.person}<dt>Label</dt><dd>{a.label || '—'}</dd>{/if}
  <dt>Template</dt><dd>{a.template.name}</dd>
  <dt>Access ends</dt><dd>{a.expiresAt ? when(a.expiresAt) : 'Never'}</dd>
</dl>
<div class="actions-row"><button type="button" class="small" onclick={() => (editing = true)}>Edit access</button></div>

<h3>Profiles ({a.profiles.length})</h3>
<ul class="list">
  {#each a.profiles as p (p.id)}
    <li>
      <Avatar name={p.name} />
      <div>
        <div class="name">
          {p.name}
          {#if p.owner}{' '}<Badge kind="plain">Owner</Badge>{/if}
          {#if p.kind === 'kids'}{' '}<Badge kind="info">Kids</Badge>{/if}
        </div>
        <div class="hint">{profileHint(p)}</div>
      </div>
      {#if p.pinLocked}<Busy small action={() => unlock(p)}>Unlock PIN</Busy>{/if}
    </li>
  {/each}
</ul>

{#if a.historyCopiedAt}
  <h3>History brought over</h3>
  <div class="box">
    <div>From the other server, {when(a.historyCopiedAt)}.</div>
    <div class="actions-row"><Busy small action={allowAgain}>Allow it again</Busy></div>
  </div>
{/if}

<h3>Signed-in devices ({a.sessions.length})</h3>
{#if a.sessions.length}
  <ul class="list">
    {#each a.sessions as s (s.id)}
      <li>
        <div>
          <div>{s.device || 'Unknown device'}</div>
          <div class="hint">{`${s.profile} · ${ago(s.lastActiveAt)}`}</div>
        </div>
        <Busy small action={() => signOut(s)}>Sign out</Busy>
      </li>
    {/each}
  </ul>
{:else}
  <p class="hint">None.</p>
{/if}

<div class="danger-zone">
  <Busy action={() => patch({ disabled: a.status !== 'disabled' }, a.status === 'disabled' ? 'Account enabled.' : 'Account disabled.')}>
    {a.status === 'disabled' ? 'Enable account' : 'Disable account'}
  </Busy>
  <Busy kind="danger" action={remove}>Delete account</Busy>
</div>

{#if editing}
  <AccessForm account={a} {templates} saved={() => reload(a.id)} close={() => (editing = false)} />
{/if}
