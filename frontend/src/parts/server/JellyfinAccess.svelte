<!-- Access to Jellyfin itself, opened or closed: closed, nobody can sign in
     there; opened, Overfin's administrator account gets a new password,
     shown here once. -->
<script lang="ts">
  import { Busy, Secret, ask, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { when } from '../../lib/format';
  import type { JellyfinAccess } from '../../lib/types-server';

  let { access }: { access?: JellyfinAccess } = $props();

  // svelte-ignore state_referenced_locally
  let open = $state(!!access?.open);
  // svelte-ignore state_referenced_locally
  let since = $state(access?.since);
  let shown = $state<{ username: string; password: string } | null>(null);

  async function change() {
    if (!open && !(await ask('Open access to Jellyfin itself? Changes made there may break Overfin, and Overfin sets the settings it needs back when it starts.', { yes: 'Open access', danger: true }))) return;
    const out = await api<{ username: string; password: string }>('PUT', '/admin/api/jellyfin/access', { open: !open });
    if (open) {
      toast('Access to Jellyfin closed.');
      open = false;
      shown = null;
      return;
    }
    open = true;
    since = new Date().toISOString();
    shown = { username: out.username, password: out.password };
  }
</script>

<div>
  <h3>Access to Jellyfin itself</h3>
  {#if open}
    <p class="notice warn">Open since {when(since)}. Changes made in Jellyfin may break Overfin, and the settings it needs are set back when it starts.</p>
  {:else}
    <p class="hint">Closed: nobody can sign in to Jellyfin itself, and Overfin doesn't need anyone to. Open it only to look inside, for a while.</p>
  {/if}
  <div class="actions-row">
    <Busy small kind={open ? '' : 'danger'} action={change}>{open ? 'Close access' : 'Open access'}</Busy>
  </div>
  {#if shown}
    <Secret value={shown.password} after="Shown once, never kept. Close access when you're done: that ends every sign-in there.">Sign in to Jellyfin as {shown.username} with this password:</Secret>
  {/if}
</div>
