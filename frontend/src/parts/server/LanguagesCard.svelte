<!-- The languages offered (each account picks one when there are several)
     and the default, for new accounts and before sign-in. -->
<script lang="ts">
  import { untrack } from 'svelte';
  import { Card, Setting, Status, Busy, ask, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { message } from '../../lib/format';
  import type { Languages, Line } from '../../lib/types-server';

  let { l }: { l: Languages } = $props();

  // svelte-ignore state_referenced_locally
  let saved = $state({ offered: [...l.offered], default: l.default });
  // svelte-ignore state_referenced_locally
  let offered = $state<string[]>([...l.offered]);
  // svelte-ignore state_referenced_locally
  let def = $state(l.default);
  let st = $state<Line>({ text: '', kind: '' });
  // The default is one of those ticked: unticked, the first ticked.
  const choices = $derived(l.choices.filter((c) => offered.includes(c.tag)));

  $effect(() => {
    const list = choices;
    untrack(() => {
      const keep = def || saved.default;
      def = list.some((c) => c.tag === keep) ? keep : (list[0]?.tag || '');
    });
  });

  async function save() {
    const changed = def !== saved.default;
    if (changed && !(await ask('Titles\' texts and artwork are fetched again in the new default language. Change it?', { yes: 'Change' }))) return;
    const out = await api<Languages>('PUT', '/admin/api/languages', { offered: l.choices.map((c) => c.tag).filter((t) => offered.includes(t)), default: def });
    saved = { offered: out.offered, default: out.default };
    st = { text: 'Saved.', kind: 'ok' };
    toast('Languages saved.');
  }
</script>

<Card title="Languages" description="Of the site, titles and emails. With several, each account picks its own.">
  <Setting label="Default language" hint="For new accounts, and before signing in.">
    {#snippet children(ids)}
      <select bind:value={def} aria-label="Default language" aria-describedby={ids.hint}>
        {#each choices as c (c.tag)}<option value={c.tag}>{c.name}</option>{/each}
      </select>
    {/snippet}
  </Setting>
  <div class="checks">
    {#each l.choices as c (c.tag)}
      <label class="inline"><input type="checkbox" value={c.tag} bind:group={offered} aria-label={c.name} /> {c.name}</label>
    {/each}
  </div>
  {#snippet foot()}
    <Status text={st.text} kind={st.kind} />
    <Busy kind="primary" action={save} fail={(err) => (st = { text: message(err), kind: 'error' })}>Save</Busy>
  {/snippet}
</Card>
