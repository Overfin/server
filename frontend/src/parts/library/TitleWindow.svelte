<!-- A film or a series of the libraries, in a window: its identification,
     then (once it has a TMDB title) its texts and its artwork, as Overfin
     shows them and as TMDB says. -->
<script lang="ts">
  import { Modal, Tabs, Loading, Notice } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { message } from '../../lib/format';
  import IdentificationDetail from './IdentificationDetail.svelte';
  import MetadataTexts from './MetadataTexts.svelte';
  import MetadataArtwork from './MetadataArtwork.svelte';
  import type { Identified, Metadata } from '../../lib/types-library';

  let { title: t, reload, edited, close }: {
    title: Identified;
    reload: () => Promise<void>;
    /** edited says the title's texts or artwork are changed here, or not. */
    edited: (yes: boolean) => void;
    close: () => void;
  } = $props();

  let tab = $state('identification');
  let meta = $state<Metadata | null>(null);
  let problem = $state('');

  const tabs = $derived(t.tmdb
    ? [{ id: 'identification', label: 'Identification' }, { id: 'texts', label: 'Texts' }, { id: 'artwork', label: 'Artwork' }]
    : [{ id: 'identification', label: 'Identification' }]);
  const heading = $derived(meta?.name ? `${meta.name}${meta.year ? ` (${meta.year})` : ''}`
    : t.tmdbName ? `${t.tmdbName}${t.tmdbYear ? ` (${t.tmdbYear})` : ''}` : t.name);
  const poster = $derived(meta?.artwork.find((a) => a.language !== '*')?.images.poster.url);

  async function loadMeta() {
    if (!t.tmdb) {
      meta = null;
      return;
    }
    try {
      problem = '';
      meta = await api<Metadata>('GET', `/admin/api/metadata/${t.kind}/${t.tmdb}`);
    } catch (err) {
      meta = null;
      problem = message(err);
    }
  }

  // A text or an image changed: the title is read again, and its row's
  // "Edited here" set from it (the whole list isn't read again for that).
  async function changed() {
    await loadMeta();
    if (meta) edited(meta.texts.some((x) => x.nameEdited || x.overviewEdited) || meta.artwork.some((a) => Object.values(a.images).some((im) => im.edited)));
  }

  // The title may become another (identified again): its texts and artwork
  // are that one's.
  let shownFor = '';
  $effect(() => {
    const key = `${t.kind}/${t.tmdb || ''}`;
    if (key === shownFor) return;
    shownFor = key;
    loadMeta();
  });
</script>

<Modal title={heading} description={t.kind === 'movie' ? 'Film' : 'Series'} size="wide" {close}>
  <div class="title-window-head">
    {#if poster}<img src={poster} alt="" />{:else}<span class="noposter"></span>{/if}
    <div class="hint">{t.folderName || t.fileName || ''}</div>
  </div>
  <Tabs {tabs} bind:current={tab} label="The title">
    {#snippet children(current)}
      {#if current === 'identification'}
        <!-- Identified again: the list is read again, its doubts and names change. -->
        <IdentificationDetail title={t} {reload} />
      {:else if problem}
        <Notice warn>{problem}</Notice>
      {:else if !meta}
        <Loading />
      {:else if current === 'texts'}
        <MetadataTexts {meta} changed={changed} />
      {:else}
        <MetadataArtwork {meta} changed={changed} />
      {/if}
    {/snippet}
  </Tabs>
</Modal>
