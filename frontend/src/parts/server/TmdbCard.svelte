<!-- TMDB: the operator's token, the ratings country kids profiles are held
     to, and which titles not in the libraries are shown. -->
<script lang="ts">
  import { Card, Badge, Setting, Status, Busy, ask, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { message } from '../../lib/format';
  import type { Line, Ratings } from '../../lib/types-server';

  let { rt, reload }: { rt: Ratings; reload: () => void } = $props();

  // The token: tested by TMDB before it's kept; never shown again.
  let key = $state('');
  let keySt = $state<Line>({ text: '', kind: '' });
  const keyFail = (err: Error) => (keySt = { text: message(err), kind: 'error' });

  async function setKey(value: string) {
    const out = await api<{ summary: string }>('PUT', '/admin/api/tmdb-token', { token: value });
    key = '';
    toast(out.summary);
    reload();
  }

  async function testKey() {
    if (!key.trim()) {
      keySt = { text: 'Paste the token first.', kind: 'error' };
      return;
    }
    await setKey(key);
  }

  async function removeKey() {
    if (!(await ask('Without a token, the site can\'t be used for now: titles lose their names and pictures, and TMDB\'s data kept so far is removed unless Seerr gives it. Remove the token?', { yes: 'Remove', danger: true }))) return;
    await setKey('');
  }

  let names: Intl.DisplayNames | null = null;
  try { names = new Intl.DisplayNames([navigator.language, 'en'], { type: 'region' }); } catch { names = null; }
  // svelte-ignore state_referenced_locally
  const countries = (rt.countries.length ? rt.countries : [rt.country])
    .map((cc) => [cc, `${(names && names.of(cc)) || cc} (${cc})`])
    .sort((a, b) => a[1].localeCompare(b[1]));
  // svelte-ignore state_referenced_locally
  let saved = $state({ country: rt.country, shown: rt.shown });
  // svelte-ignore state_referenced_locally
  let country = $state(rt.country);
  // svelte-ignore state_referenced_locally
  let shown = $state(rt.shown);

  const describe = (r: Ratings): Line => {
    if (!r.source && !r.token.set) return { text: 'Without TMDB, no title can be rated, so kids profiles see nothing.', kind: 'error' };
    if (!r.source) return { text: 'TMDB can\'t be reached: no title can be rated, so kids profiles see nothing.', kind: 'error' };
    if (!r.countries.length) return { text: 'Fetching the rating scales from TMDB… Kids profiles see nothing until titles are rated.', kind: '' };
    return { text: `${r.fetched.toLocaleString()} of ${r.total.toLocaleString()} films and series rated.`, kind: '' };
  };
  // svelte-ignore state_referenced_locally
  let st = $state<Line>(describe(rt));

  async function save() {
    if (country === saved.country && shown === saved.shown) return;
    if (country !== saved.country &&
      !(await ask('Every kids profile will need new limits from its account\'s owner, and sees nothing until then. Change the country?', { yes: 'Change' }))) {
      country = saved.country;
      return;
    }
    const out = await api<Ratings>('PUT', '/admin/api/ratings', { country, shown });
    saved = { country: out.country, shown: out.shown };
    st = describe(out);
    toast('TMDB settings saved.');
  }
</script>

<Card title="TMDB" description="Where titles, artwork and ratings come from. For now, the site can't be used without it.">
  {#snippet badge()}
    {#if rt.source}<Badge kind="ok">Reachable</Badge>{:else}<Badge kind="err">{rt.token.set ? 'Unreachable' : 'No token'}</Badge>{/if}
  {/snippet}
  <Setting label="Token" hint={rt.token.inEnv ? 'Set in Overfin\'s environment (OVERFIN_TMDB_TOKEN): change it there.' : 'TMDB\'s API Read Access Token.'}>
    {#snippet children(ids)}
      <div class="row">
        <input type="password" bind:value={key} autocomplete="off" placeholder={rt.token.set ? 'Unchanged' : 'API Read Access Token'} disabled={rt.token.inEnv} aria-labelledby={ids.label} aria-describedby={ids.hint} />
        <Busy disabled={rt.token.inEnv} action={testKey} fail={keyFail}>Test and save</Busy>
        {#if rt.token.set && !rt.token.inEnv}<Busy kind="danger" action={removeKey} fail={keyFail}>Remove</Busy>{/if}
      </div>
    {/snippet}
  </Setting>
  {#if !rt.token.inEnv && !rt.token.set}
    <p class="hint">Free, once: sign up at <a href="https://www.themoviedb.org/signup" target="_blank" rel="noopener">themoviedb.org</a>, then in your account's <a href="https://www.themoviedb.org/settings/api" target="_blank" rel="noopener">Settings → API</a>, ask for API access (personal use is fine), then copy the API Read Access Token: the long one, not the API key.</p>
  {/if}
  <Status text={keySt.text} kind={keySt.kind} />
  <Setting label="Ratings country" hint="Titles' ages, for kids profiles and categories, go by this country's ratings first; a title with none there gets the middle of the other countries'.">
    {#snippet children(ids)}
      <select bind:value={country} aria-labelledby={ids.label} aria-describedby={ids.hint}>
        {#each countries as [cc, label] (cc)}<option value={cc}>{label}</option>{/each}
      </select>
    {/snippet}
  </Setting>
  <Setting label="Titles shown" hint="Of those no library has, in discovery and requests. A title a library has is always shown.">
    {#snippet children(ids)}
      <select bind:value={shown} aria-labelledby={ids.label} aria-describedby={ids.hint}>
        <option value="all">All, announced ones too</option>
        <option value="released">Released, in theatres too</option>
        <option value="home">Watchable at home</option>
      </select>
    {/snippet}
  </Setting>
  {#snippet foot()}
    <Status text={st.text} kind={st.kind} />
    <Busy kind="primary" disabled={!rt.countries.length} action={save} fail={(err) => (st = { text: message(err), kind: 'error' })}>Save</Busy>
  {/snippet}
</Card>
