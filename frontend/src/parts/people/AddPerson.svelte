<!-- The form to make someone's account here, in a window: their email.
     Nobody types a password: they choose theirs from a setup link, which
     opens next. -->
<script lang="ts">
  import { FormModal, Field, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import type { Person } from '../../lib/types-people';

  let { saved, close }: { saved: (p: Person) => void; close: () => void } = $props();

  let email = $state('');
  let admin = $state(false);

  async function save() {
    const p = await api<Person>('POST', '/admin/api/people', { email, admin });
    toast(`${email} added: they choose their password from a setup link.`);
    saved(p);
  }
</script>

<FormModal title="Add a person" description="Next, send them a setup link: they choose their own password with it." saveLabel="Add" {save} {close}>
  <div class="fields">
    <Field label="Email" hint="Check it: it's how they sign in, and where emails go." wide><input type="email" bind:value={email} placeholder="name@example.org" autocomplete="off" required /></Field>
    <label class="inline full"><input type="checkbox" bind:checked={admin} /> Admin</label>
  </div>
</FormModal>
