<!-- An account's access, in a window: its label (without the identity
     provider's name), its template, when it ends. -->
<script lang="ts">
  import { FormModal, Field, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { plural, toLocalInput } from '../../lib/format';
  import type { Template } from '../../lib/types';
  import type { Account } from '../../lib/types-people';
  import { nameOf } from './names';

  let { account: a, templates, saved, close }: { account: Account; templates: Template[]; saved: () => void; close: () => void } = $props();

  // The form starts from the account; it's made again for each one.
  // svelte-ignore state_referenced_locally
  let label = $state(a.label || '');
  // svelte-ignore state_referenced_locally
  let expires = $state(toLocalInput(a.expiresAt));
  // svelte-ignore state_referenced_locally
  let template = $state(a.template.id);

  async function save() {
    await api('PATCH', `/admin/api/accounts/${a.id}`, {
      ...(a.person ? {} : { label }),
      expiresAt: expires ? new Date(expires).toISOString() : '',
      templateId: template,
    });
    toast('Access saved.');
    saved();
  }
</script>

<FormModal title={`${nameOf(a)}'s access`} {save} {close}>
  <div class="fields">
    {#if !a.person}
      <Field label="Label" hint="A name for it, until the identity provider gives one." wide><input bind:value={label} maxlength="64" placeholder="e.g. Bob" /></Field>
    {/if}
    <Field label="Template" hint="What the account may do." wide>
      <select bind:value={template}>
        {#each templates as t (t.id)}
          <option value={t.id}>{`${t.name} · ${plural(t.maxStreams, 'stream')}, ${plural(t.maxProfiles, 'profile')}${t.reserveStreams ? ', one each' : ''}`}</option>
        {/each}
      </select>
    </Field>
    <Field label="Access ends" hint="Empty: never." wide><input type="datetime-local" bind:value={expires} /></Field>
  </div>
</FormModal>
