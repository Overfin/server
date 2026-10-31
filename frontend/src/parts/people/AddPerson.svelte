<!-- The form to make someone's account here, in a window: their email and a
     first password, which they choose again at their first sign-in unless
     told otherwise. -->
<script lang="ts">
  import { FormModal, Field, toast } from '@overfin/ui';
  import { api } from '../../lib/api';

  let { saved, close }: { saved: () => void; close: () => void } = $props();

  let email = $state('');
  let password = $state('');
  let mustChange = $state(true);
  let admin = $state(false);

  async function save() {
    await api('POST', '/admin/api/people', { email, password, mustChange, admin });
    toast(`${email} can sign in now.`);
    saved();
  }
</script>

<FormModal title="Add a person" description="Their account is ready at once: hand them their email and this password, privately." saveLabel="Add" {save} {close}>
  <div class="fields">
    <Field label="Email" wide><input type="email" bind:value={email} placeholder="name@example.org" autocomplete="off" required /></Field>
    <Field label="Password" hint="The rules are on People → Registration." wide><input type="password" bind:value={password} autocomplete="new-password" required /></Field>
    <label class="inline full"><input type="checkbox" bind:checked={mustChange} /> Ask for a new password at the first sign-in</label>
    <label class="inline full"><input type="checkbox" bind:checked={admin} /> Admin</label>
  </div>
</FormModal>
