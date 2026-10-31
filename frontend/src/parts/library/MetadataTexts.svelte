<!-- A title's name and synopsis in each language offered, as the site
     shows them: changed in a form, given back to TMDB's. -->
<script lang="ts">
  import { Badge, FormModal, RowActions, ask, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { languageName } from '../../lib/format';
  import type { Metadata, MetadataText } from '../../lib/types-library';

  let { meta, changed }: { meta: Metadata; changed: () => Promise<void> } = $props();

  let editing = $state<MetadataText | null>(null);
  let name = $state('');
  let overview = $state('');

  const base = $derived(`/admin/api/metadata/${meta.kind}/${meta.tmdb}`);
  const edited = (x: MetadataText) => x.nameEdited || x.overviewEdited;

  function edit(x: MetadataText) {
    editing = x;
    name = x.name;
    overview = x.overview;
  }

  // A field as TMDB has it isn't kept as a change: it goes back to TMDB's.
  async function field(x: MetadataText, which: 'name' | 'overview', value: string, wasEdited: boolean, tmdb: string) {
    value = value.trim();
    if (value === tmdb && !x.untranslated) {
      if (wasEdited) await api('DELETE', `${base}/${which}/${x.language}`);
      return;
    }
    if (value === (which === 'name' ? x.name : x.overview) && (wasEdited || x.untranslated)) return;
    await api('PUT', base, { field: which, language: x.language, value });
  }

  async function save() {
    const x = editing!;
    if (!name.trim()) throw new Error('A name is needed.');
    await field(x, 'name', name, x.nameEdited, x.tmdbName);
    if (overview.trim()) await field(x, 'overview', overview, x.overviewEdited, x.tmdbOverview);
    else if (x.overviewEdited) await api('DELETE', `${base}/overview/${x.language}`);
    toast('Texts saved.');
    await changed();
  }

  async function restore(x: MetadataText) {
    if (!(await ask(`Give ${languageName(x.language)} back TMDB's name and synopsis?`, { yes: 'Give back' }))) return;
    if (x.nameEdited) await api('DELETE', `${base}/name/${x.language}`);
    if (x.overviewEdited) await api('DELETE', `${base}/overview/${x.language}`);
    toast('Back to TMDB\'s texts.');
    await changed();
  }
</script>

<p class="hint">The name and synopsis the site shows, in each language it offers. A change is kept by Overfin, over TMDB's: fetching TMDB's again doesn't undo it.</p>
<div class="table-wrap">
  <table>
    <thead><tr><th>Language</th><th>Name and synopsis</th><th><span class="visually-hidden">Actions</span></th></tr></thead>
    <tbody>
      {#each meta.texts as x (x.language)}
        <tr>
          <td class="nowrap">{languageName(x.language)}{#if x.default}<div class="hint">Default</div>{/if}</td>
          <td>
            <div class="name">{x.name}</div>
            <div class="hint clamp">{x.overview || 'No synopsis.'}</div>
            {#if edited(x)}<Badge kind="info">Edited here</Badge>{:else if x.untranslated}<Badge kind="plain">TMDB's in the default language</Badge>{/if}
          </td>
          <RowActions edit={() => edit(x)} name={languageName(x.language)}>
            {#if edited(x)}<button type="button" class="small" onclick={() => restore(x)}>Back to TMDB's</button>{/if}
          </RowActions>
        </tr>
      {/each}
    </tbody>
  </table>
</div>

{#if editing}
  <FormModal title={`Texts in ${languageName(editing.language)}`} description="What the site shows for this title in this language." wide {save} close={() => (editing = null)}>
    <label class="field">Name
      <input bind:value={name} maxlength="300" required />
      <span class="hint">TMDB's: {editing.untranslated ? 'none in this language yet' : editing.tmdbName}</span>
    </label>
    <label class="field">Synopsis
      <textarea bind:value={overview} rows="7" maxlength="5000"></textarea>
      <span class="hint">{editing.untranslated ? 'TMDB has none in this language yet.' : editing.tmdbOverview ? `TMDB's: ${editing.tmdbOverview}` : 'TMDB has none.'} Empty, it's TMDB's.</span>
    </label>
  </FormModal>
{/if}
