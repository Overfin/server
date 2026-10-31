<!-- A new password for someone who signs in here (they forgot theirs), in
     a window. Their logins end. -->
<script lang="ts">
  import { FormModal, Field, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import type { Person } from '../../lib/types-people';

  let { person: x, saved, close }: { person: Person; saved: () => void; close: () => void } = $props();

  let password = $state('');
  let mustChange = $state(true);

  async function save() {
    await api('PUT', `/admin/api/people/${x.id}/password`, { password, mustChange });
    toast(`${x.email} has a new password; their logins ended.`);
    saved();
  }
</script>

<FormModal title={`A new password for ${x.email}`} description="Hand it over privately. Their logins end." saveLabel="Set it" {save} {close}>
  <div class="fields">
    <Field label="New password" wide><input type="password" bind:value={password} autocomplete="new-password" required /></Field>
    <label class="inline full"><input type="checkbox" bind:checked={mustChange} /> Ask for a new password at the next sign-in</label>
  </div>
</FormModal>
