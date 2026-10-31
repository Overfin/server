<!-- A title's poster, backdrop and logo: one for every language, then each
     language's, as the site shows them. Another is chosen among TMDB's or
     uploaded; a choice is given back to TMDB's. -->
<script lang="ts">
  import { ask, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { languageName } from '../../lib/format';
  import ImagePicker from './ImagePicker.svelte';
  import type { ImageType, Metadata, MetadataArtwork } from '../../lib/types-library';

  let { meta, changed }: { meta: Metadata; changed: () => Promise<void> } = $props();

  const types: { type: ImageType; label: string }[] = [
    { type: 'poster', label: 'Poster' }, { type: 'backdrop', label: 'Backdrop' }, { type: 'logo', label: 'Logo' },
  ];
  let picking = $state<{ type: ImageType; language: string } | null>(null);

  // One language offered: its row alone, and what's chosen there is for
  // every language (so a language added later shows it too).
  const single = $derived(meta.artwork.length === 2);
  const rows = $derived(single ? meta.artwork.slice(1) : meta.artwork);
  const target = (a: MetadataArtwork) => (single ? '*' : a.language);

  const where = (a: MetadataArtwork) => (a.language === '*' ? 'All languages' : languageName(a.language));
  function source(a: MetadataArtwork, type: ImageType): string {
    const im = a.images[type];
    if (a.language === '*') return im.url ? 'Chosen for every language without its own' : 'None chosen: each language shows its own';
    if (single && im.edited) return 'Chosen here';
    if (im.edited === 'language') return 'Chosen for this language';
    if (im.edited === 'all') return 'The one for all languages';
    return im.url ? 'TMDB\'s' : 'None';
  }
  // A choice made for this row itself (not one it shows from "All languages").
  const own = (a: MetadataArtwork, type: ImageType) =>
    single ? a.images[type].edited !== '' : a.images[type].edited === (a.language === '*' ? 'all' : 'language');

  async function restore(a: MetadataArtwork, type: ImageType, label: string) {
    if (!(await ask(`${single ? 'Give' : `${where(a)}: give`} the ${label.toLowerCase()} back to TMDB's?`, { yes: 'Give back' }))) return;
    const language = a.images[type].edited === 'language' ? a.language : target(a);
    await api('DELETE', `/admin/api/metadata/${meta.kind}/${meta.tmdb}/${type}/${encodeURIComponent(language)}`);
    toast(`Back to TMDB's ${label.toLowerCase()}.`);
    await changed();
  }
</script>

<p class="hint">The pictures the site shows.{single ? '' : ' One chosen for all languages goes wherever a language has none of its own; a language\'s own comes first.'}</p>
{#each types as { type, label } (type)}
  <h3>{label}</h3>
  <ul class="list artwork-rows">
    {#each rows as a (a.language)}
      <li>
        {#if a.images[type].url}
          <img class="artwork-thumb {type}" class:logo-preview={type === 'logo'} src={a.images[type].url} alt="" loading="lazy" />
        {:else}
          <span class="artwork-thumb {type} none"></span>
        {/if}
        <div>
          <div class="name">{where(a)}</div>
          <div class="hint">{source(a, type)}</div>
        </div>
        <span class="row-actions">
          {#if own(a, type)}<button type="button" class="small" onclick={() => restore(a, type, label)}>Back to TMDB's</button>{/if}
          <button type="button" class="small" aria-label={`Change the ${label.toLowerCase()}: ${where(a)}`} onclick={() => (picking = { type, language: target(a) })}>Change</button>
        </span>
      </li>
    {/each}
  </ul>
{/each}

{#if picking}
  <ImagePicker {meta} type={picking.type} language={picking.language} label={types.find((t) => t.type === picking!.type)!.label}
    close={() => (picking = null)} chosen={changed} />
{/if}
