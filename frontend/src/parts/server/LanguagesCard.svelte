<!-- The server's country, its languages offered (each account picks one
     when there are several) and the default, for new accounts and before
     sign-in. Languages offered before that aren't the country's stay, under
     Other languages, until they're turned off and saved. -->
<script lang="ts">
  import { untrack } from 'svelte';
  import { Card, Setting, Status, Busy, ask, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { countryName, message } from '../../lib/format';
  import { unsaved } from '../../lib/router.svelte';
  import type { Languages, Line } from '../../lib/types-server';

  let { l, reload }: { l: Languages; reload: () => void } = $props();

  // svelte-ignore state_referenced_locally
  const countries = Object.keys(l.countries)
    .map((cc) => [cc, countryName(cc)])
    .sort((a, b) => a[1].localeCompare(b[1]));
  // Every language's name: the countries' and those offered.
  // svelte-ignore state_referenced_locally
  const named = new Map([...Object.values(l.countries).flat(), ...l.choices].map((c) => [c.tag, c.name]));

  // svelte-ignore state_referenced_locally
  let saved = $state({ country: l.country, offered: [...l.offered], default: l.default });
  // svelte-ignore state_referenced_locally
  let country = $state(l.country);
  // svelte-ignore state_referenced_locally
  let offered = $state<string[]>([...l.offered]);
  // svelte-ignore state_referenced_locally
  let def = $state(l.default);
  let st = $state<Line>({ text: '', kind: '' });
  $effect(() => unsaved(() => country !== saved.country || def !== saved.default ||
    [...offered].sort().join() !== [...saved.offered].sort().join()));

  const own = $derived((l.countries[country] || []).map((c) => c.tag));
  // Offered when last saved, not the country's.
  const others = $derived(saved.offered.filter((t) => !own.includes(t)));
  // The default is one of those on: off, the first on.
  const choices = $derived([...own, ...others].filter((t) => offered.includes(t)));

  $effect(() => {
    const list = choices;
    untrack(() => {
      const keep = def || saved.default;
      def = list.includes(keep) ? keep : (list[0] || '');
    });
  });

  // Another country: its languages to switch on; those of the one before
  // that were saved stay on, under Other languages.
  function moved() {
    offered = offered.filter((t) => own.includes(t) || saved.offered.includes(t));
  }

  async function save() {
    if (!country) {
      st = { text: 'Choose the server\'s country first.', kind: 'error' };
      return;
    }
    if (def !== saved.default && !(await ask('Titles\' texts and artwork are fetched again in the new default language. Change it?', { yes: 'Change' }))) return;
    const out = await api<Languages>('PUT', '/admin/api/languages', { country, offered: choices, default: def });
    const movedCountry = out.country !== saved.country;
    saved = { country: out.country, offered: out.offered, default: out.default };
    offered = [...out.offered];
    st = { text: 'Saved.', kind: 'ok' };
    toast('Languages saved.');
    // The TMDB card's ratings country may follow it.
    if (movedCountry) reload();
  }
</script>

<Card title="Languages" description="Of the site, titles and emails. With several, each account picks its own.">
  <Setting label="Country" hint={country ? 'The server\'s country: its languages are offered below. Ratings follow it too, unless the TMDB card chooses another country.' : 'Choose the server\'s country: its languages are offered below.'}>
    {#snippet children(ids)}
      <select bind:value={country} onchange={moved} aria-labelledby={ids.label} aria-describedby={ids.hint}>
        {#if !country}<option value="" disabled>Choose a country</option>{/if}
        {#each countries as [cc, label] (cc)}<option value={cc}>{label}</option>{/each}
      </select>
    {/snippet}
  </Setting>
  {#if own.length}
    <div class="checks">
      {#each own as tag (tag)}
        <label class="inline"><input type="checkbox" value={tag} bind:group={offered} aria-label={named.get(tag) || tag} /> {named.get(tag) || tag}</label>
      {/each}
    </div>
  {/if}
  {#if others.length}
    <h3>Other languages</h3>
    <p class="hint">Offered before, not the country's: once turned off and saved, they're gone from here.</p>
    <div class="checks">
      {#each others as tag (tag)}
        <label class="inline"><input type="checkbox" value={tag} bind:group={offered} aria-label={named.get(tag) || tag} /> {named.get(tag) || tag}</label>
      {/each}
    </div>
  {/if}
  <Setting label="Default language" hint="For new accounts, and before signing in.">
    {#snippet children(ids)}
      <select bind:value={def} aria-label="Default language" aria-describedby={ids.hint}>
        {#each choices as tag (tag)}<option value={tag}>{named.get(tag) || tag}</option>{/each}
      </select>
    {/snippet}
  </Setting>
  {#snippet foot()}
    <Status text={st.text} kind={st.kind} />
    <Busy kind="primary" action={save} fail={(err) => (st = { text: message(err), kind: 'error' })}>Save</Busy>
  {/snippet}
</Card>
