<!-- A template's form, in a window: a new one when template is null. -->
<script lang="ts">
  import { FormModal, Field, Setting, ask, toast } from '@overfin/ui';
  import { api } from '../lib/api';
  import { numberIn, plural } from '../lib/format';
  import type { Categories, Template } from '../lib/types';

  let { template: t, cats, saved, close }: { template: Template | null; cats: Categories; saved: () => void; close: () => void } = $props();

  const catName = (c: Categories['items'][number]) => c.names[cats.defaultLanguage] || Object.values(c.names)[0] || 'Unnamed';
  // The form starts from the template; it's made again for each one.
  // svelte-ignore state_referenced_locally
  let name = $state(t?.name || '');
  // svelte-ignore state_referenced_locally
  let streams = $state<number | string>(t?.maxStreams ?? 1);
  // svelte-ignore state_referenced_locally
  let profiles = $state<number | string>(t?.maxProfiles ?? 1);
  // svelte-ignore state_referenced_locally
  let reserve = $state(!!t?.reserveStreams);
  // Only some categories: none ticked is no limit.
  // svelte-ignore state_referenced_locally
  let picked = $state<string[]>([...(t?.categories || [])]);

  async function save() {
    const body = { name, maxStreams: numberIn(streams, 'Streams'), maxProfiles: numberIn(profiles, 'Profiles'),
      reserveStreams: reserve, categories: picked.length ? picked : null };
    // A template kept to categories (deleted since, maybe: then it sees
    // nothing) loses its limit only when the admin says so.
    if (t?.categories && !picked.length
      && !(await ask(`No category is ticked: every account on "${t.name}" will see every title. Save anyway?`, { yes: 'Save' }))) {
      throw new Error('Not saved: tick the categories it keeps to, or save it without any.');
    }
    await (t ? api('PUT', `/admin/api/templates/${t.id}`, body) : api('POST', '/admin/api/templates', body));
    toast(t ? 'Template saved.' : 'Template created.');
    saved();
  }
</script>

<FormModal title={t ? `Edit ${t.name}` : 'New template'} saveLabel={t ? 'Save' : 'Create'} {save} {close}
  description={t ? `${plural(t.accounts, 'account')} on it${t.accounts ? '; changes apply to them at once' : ''}.` : 'For a group of accounts that may do more, or less.'}>
  <div class="fields">
    <Field label="Name" wide><input bind:value={name} maxlength="40" placeholder="e.g. Family, Friends" required /></Field>
    <Field label="Streams at once"><input type="number" min="1" max="100" bind:value={streams} /></Field>
    <Field label="Profiles" hint="The owner's included."><input type="number" min="1" max="20" bind:value={profiles} /></Field>
  </div>
  <Setting label="A stream for each profile" hint="Each profile keeps one of the streams for itself; the rest are shared. Nobody is cut off to make room. No more profiles than streams.">
    {#snippet children(ids)}<input type="checkbox" bind:checked={reserve} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
  </Setting>
  {#if cats.items.length}
    <div class="setting stack">
      <div>
        <div class="label">Only these categories</div>
        <div class="hint">Every profile of its accounts, the owner's too, sees only their titles (and none that no library has). None ticked: no limit.</div>
      </div>
      <div class="checks">
        {#each cats.items as c (c.id)}
          <label class="inline"><input type="checkbox" value={c.id} bind:group={picked} aria-label={`Only: ${catName(c)}`} /> {catName(c)}</label>
        {/each}
      </div>
    </div>
  {/if}
</FormModal>
