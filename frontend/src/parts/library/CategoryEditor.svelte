<!-- Edits a category (category), or makes one (null), in a window, with a
     live count of the titles it would have. -->
<script lang="ts">
  import { FormModal, Field, Setting, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { message, plural } from '../../lib/format';
  import type { Categories, Category } from '../../lib/types';

  let { data, category: c, langName, nameOf, saved, close }: {
    data: Categories; category: Category | null; langName: (code: string) => string; nameOf: (c: Category) => string;
    saved: () => void; close: () => void;
  } = $props();

  // The form starts from the category; it's made again for each one.
  // svelte-ignore state_referenced_locally
  const r = c?.rules || {};
  // The languages offered, and any other it has a name in (offered before):
  // saving keeps that name instead of dropping it.
  // svelte-ignore state_referenced_locally
  const langs = [...new Set([...data.languages, ...Object.keys(c?.names || {})])];
  // svelte-ignore state_referenced_locally
  let names = $state<Record<string, string>>(Object.fromEntries(langs.map((l) => [l, c?.names[l] || ''])));
  let kinds = $state<string[]>([...(r.kinds || [])]);
  let genres = $state<string[]>((r.genres || []).map(String));
  let languages = $state<string[]>([...(r.languages || [])]);
  let libraries = $state<string[]>([...(r.libraries || [])]);
  let tags = $state<string[]>([...(r.tags || [])]);
  let except = $state<string[]>([...(r.except || [])]);
  // For ages up to: the ages kids profiles choose among.
  let maxAge = $state(r.maxAge !== undefined && r.maxAge !== null ? String(r.maxAge) : '');
  // svelte-ignore state_referenced_locally
  let home = $state(Boolean(c?.home));
  let tagSearch = $state('');
  let preview = $state('');

  // Tags can be many (TMDB's keywords, with Jellyfin's default metadata):
  // the ticked first, and a search.
  // svelte-ignore state_referenced_locally
  const tagList = [...data.tags].sort((a, b) => Number((r.tags || []).includes(b)) - Number((r.tags || []).includes(a)));
  const tagShown = (t: string) => {
    const q = tagSearch.trim().toLowerCase();
    return !q || t.toLowerCase().includes(q) || tags.includes(t);
  };
  // svelte-ignore state_referenced_locally
  const others = data.items.filter((x) => x.id !== c?.id);
  const ages = [3, 6, 8, 10, 12, 14, 16];

  const rules = () => ({ kinds, genres: genres.map(Number), languages, libraries,
    tags, maxAge: maxAge === '' ? undefined : Number(maxAge), except });

  // What it would hold, asked again a moment after a change.
  $effect(() => {
    const body = { id: c?.id || '', rules: rules() };
    const timer = setTimeout(async () => {
      try {
        const p = await api<{ count: number; sample: string[] }>('POST', '/admin/api/categories/preview', body);
        preview = `${plural(p.count, 'title')}${p.sample.length ? `: ${p.sample.join(', ')}${p.count > p.sample.length ? '…' : ''}` : ''}`;
      } catch (err) {
        preview = message(err);
      }
    }, 300);
    return () => clearTimeout(timer);
  });

  async function save() {
    const body = { names: Object.fromEntries(Object.entries(names).map(([l, v]) => [l, v.trim()])), rules: rules(), home };
    await (c ? api('PUT', `/admin/api/categories/${c.id}`, body) : api('POST', '/admin/api/categories', body));
    toast(c ? 'Category saved.' : 'Category created.');
    saved();
  }
</script>

{#snippet group(label: string, hint: string, values: [string, string][], picked: string[], set: (v: string[]) => void, aria: string, shown?: (v: string) => boolean)}
  <div class="setting stack">
    <div><div class="label">{label}</div>{#if hint}<div class="hint">{hint}</div>{/if}</div>
    <div class="checks">
      {#each values as [v, text] (v)}
        <label class="inline" hidden={shown ? !shown(v) : false}>
          <input type="checkbox" checked={picked.includes(v)} aria-label={`${aria}: ${text}`}
            onchange={(e) => set(e.currentTarget.checked ? [...picked, v] : picked.filter((x) => x !== v))} /> {text}
        </label>
      {/each}
    </div>
  </div>
{/snippet}

<FormModal title={c ? `Edit ${nameOf(c)}` : 'New category'} wide saveLabel={c ? 'Save' : 'Create'} {save} {close}
  description="A title is in it when it fits every rule you set; within a rule, any of what you tick. Nothing ticked: no limit.">
  <div class="modal-fields">
    {#each langs as l (l)}
      <Field label={`Name in ${langName(l)}`}><input bind:value={names[l]} maxlength="40" aria-label={`Name in ${langName(l)}`} /></Field>
    {/each}
    {@render group('Films or series', '', [['movie', 'Films'], ['series', 'Series']], kinds, (v) => (kinds = v), 'Kind')}
    {@render group('Genres', 'TMDB\'s.', data.genres.map((g) => [String(g.id), g.name]), genres, (v) => (genres = v), 'Genre')}
    {@render group('Original language', '', data.originalLanguages.map((l) => [l, langName(l)]), languages, (v) => (languages = v), 'Language')}
    {#if data.libraries.length > 1}
      {@render group('Libraries', '', data.libraries.map((l) => [l.id, l.name]), libraries, (v) => (libraries = v), 'Library')}
    {/if}
    {#if data.tags.length > 0}
      <div class="setting stack">
        <div><div class="label">Tags</div><div class="hint">Jellyfin's: often TMDB's keywords (christmas, anime, based on novel…).</div></div>
        <div>
          {#if data.tags.length > 12}<input type="search" placeholder="Find a tag" aria-label="Find a tag" bind:value={tagSearch} />{/if}
          <div class="checks">
            {#each tagList as t (t)}
              <label class="inline" hidden={!tagShown(t)}>
                <input type="checkbox" value={t} bind:group={tags} aria-label={`Tag: ${t}`} /> {t}
              </label>
            {/each}
          </div>
        </div>
      </div>
    {/if}
    <div class="setting stack">
      <div><div class="label">For ages up to</div><div class="hint">From the titles' ratings in any country (this server's country first). A title without any is left out.</div></div>
      <select bind:value={maxAge} aria-label="For ages up to">
        <option value="">Any age</option>
        {#each ages as a (a)}<option value={String(a)}>{a} years</option>{/each}
      </select>
    </div>
    {#if others.length}
      {@render group('Leave out', 'Titles of these categories aren\'t in this one: Series except Anime, say.', others.map((x) => [x.id, nameOf(x)]), except, (v) => (except = v), 'Leave out')}
    {/if}
    <Setting label="A row on the home page">
      {#snippet children(ids)}<input type="checkbox" bind:checked={home} aria-labelledby={ids.label} />{/snippet}
    </Setting>
    <div class="setting"><div><div class="label">What it holds</div><div class="hint">{preview}</div></div></div>
  </div>
</FormModal>
