<!-- What an admin can do about someone who signs in here: a new password
     (they forgot theirs), the admin mark, and, before they have an
     account, removing them. -->
<script lang="ts">
  import { Badge, Busy, ask, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import type { Person } from '../../lib/types-people';
  import NewPassword from './NewPassword.svelte';

  let { person: x, reload }: { person: Person; reload: () => void } = $props();

  let passwordOpen = $state(false);

  const standing = $derived(!x.confirmed ? 'Registered: waiting for their address\'s confirmation'
    : x.mustChange ? 'Chooses a new password at the next sign-in' : '');

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
<div class="actions-row">
  <button type="button" class="small" onclick={() => (passwordOpen = true)}>Set a new password</button>
  {#if !x.you}<Busy small action={toggleAdmin}>{x.admin ? 'Remove admin' : 'Make admin'}</Busy>{/if}
  {#if !x.accountId && !x.you}<Busy small kind="danger" action={remove}>Remove</Busy>{/if}
</div>

{#if passwordOpen}
  <NewPassword person={x} saved={reload} close={() => (passwordOpen = false)} />
{/if}
