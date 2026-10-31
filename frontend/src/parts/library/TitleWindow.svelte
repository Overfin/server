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

  let { title: t, reload, close }: { title: Identified; reload: () => Promise<void>; close: () => void } = $props();

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

  // An edit changes the list's "Edited here" too.
  async function changed() {
    await Promise.all([loadMeta(), reload()]);
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
        <IdentificationDetail title={t} reload={changed} />
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
