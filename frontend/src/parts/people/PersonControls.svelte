<!-- What an admin can do about someone who signs in here: a setup link
     (a new account, or they forgot their password), the admin mark, and,
     before they have an account, removing them. -->
<script lang="ts">
  import { Badge, Busy, ask, toast, day } from '@overfin/ui';
  import { api } from '../../lib/api';
  import type { Person } from '../../lib/types-people';
  import SetupLink from './SetupLink.svelte';

  let { person: x, reload }: { person: Person; reload: () => void } = $props();

  let linkOpen = $state(false);

  const standing = $derived(x.waitingSetup ? 'Waiting for setup: they haven\'t chosen their password yet'
    : !x.confirmed && x.registered ? 'Registered: waiting for their address\'s confirmation'
    : !x.confirmed ? 'Address not confirmed: they confirm it before signing in'
    : x.unverified ? 'Address not confirmed: they\'re asked to confirm it'
    : x.mustChange ? 'Chooses a new password at the next sign-in' : '');

  async function cancel() {
    await api('DELETE', `/admin/api/people/${x.id}/setup-link`);
    toast(`${x.email}'s setup link no longer works.`);
    reload();
  }

  async function toggleAdmin() {
    await api('PATCH', `/admin/api/people/${x.id}`, { admin: !x.admin });
    toast(x.admin ? `${x.email} is no longer an admin.` : `${x.email} is an admin now.`);
    reload();
  }

  async function remove() {
    if (!(await ask(`Remove ${x.email}? They can't sign in any more.`, { yes: 'Remove', danger: true }))) return;
    await api('DELETE', `/admin/api/people/${x.id}`);
    toast(`${x.email} removed.`);
    reload();
  }
</script>

<div class="name">
  {x.email}
  {#if x.admin}{' '}<Badge kind="info">Admin</Badge>{/if}
  {#if x.you}{' '}<Badge kind="plain">You</Badge>{/if}
  {#if x.disabled}{' '}<Badge kind="err">Disabled</Badge>{/if}
</div>
{#if standing}<div class="hint">{standing}</div>{/if}
{#if x.setupExpires}<div class="hint">{`A setup link works until ${day(x.setupExpires)}.`}</div>{/if}
<div class="actions-row">
  <button type="button" class="small" onclick={() => (linkOpen = true)}>{x.waitingSetup ? 'Send a setup link' : 'A new password: setup link'}</button>
  {#if x.setupExpires}<Busy small action={cancel}>Cancel the link</Busy>{/if}
  {#if !x.you}<Busy small action={toggleAdmin}>{x.admin ? 'Remove admin' : 'Make admin'}</Busy>{/if}
  {#if !x.accountId && !x.you}<Busy small kind="danger" action={remove}>Remove</Busy>{/if}
</div>

{#if linkOpen}
  <SetupLink person={x} done={reload} close={() => (linkOpen = false)} />
{/if}
