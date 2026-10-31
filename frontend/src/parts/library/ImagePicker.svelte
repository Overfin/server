<!-- Another poster, backdrop or logo for a title, in a language (or all):
     one of TMDB's, in any language, or one uploaded. -->
<script lang="ts">
  import { Modal, Loading, Notice, toast } from '@overfin/ui';
  import { api, upload } from '../../lib/api';
  import { languageName, message } from '../../lib/format';
  import type { ImageChoice, ImageType, Metadata } from '../../lib/types-library';

  let { meta, type, language, label, close, chosen }: {
    meta: Metadata; type: ImageType; language: string; label: string;
    close: () => void; chosen: () => Promise<void>;
  } = $props();

  let choices = $state<ImageChoice[] | null>(null);
  let problem = $state('');
  let busy = $state(false);
  let picker = $state<HTMLInputElement>();
  // The images in the language first: its two letters (TMDB files them so).
  // svelte-ignore state_referenced_locally
  let filter = $state(language !== '*' ? language.split('-')[0] : meta.artwork.length === 2 ? meta.artwork[1].language.split('-')[0] : '');

  // What it shows now: with one language offered, that one's.
  const current = $derived((meta.artwork.length === 2 ? meta.artwork[1] : meta.artwork.find((a) => a.language === language))?.images[type].url);
  const languages = $derived([...new Set((choices || []).map((c) => c.language || ''))].sort());
  const shown = $derived((choices || []).filter((c) => filter === '' || (filter === 'none' ? !c.language : c.language === filter)));
  const where = $derived(language !== '*' ? languageName(language) : meta.artwork.length === 2 ? languageName(meta.artwork[1].language) : 'all languages');

  // Asked once, for the title the window opened on.
  // svelte-ignore state_referenced_locally
  api<Record<ImageType, ImageChoice[]>>('GET', `/admin/api/metadata/${meta.kind}/${meta.tmdb}/images`).then(
    (all) => {
      choices = all[type] || [];
      if (filter && !choices.some((c) => c.language === filter)) filter = '';
    },
    (err) => (problem = message(err)),
  );

  async function choose(value: string) {
    busy = true;
    try {
      await api('PUT', `/admin/api/metadata/${meta.kind}/${meta.tmdb}`, { field: type, language, value });
      toast(`${label} chosen.`);
      await chosen();
      close();
    } catch (err) {
      toast(message(err), true);
    } finally {
      busy = false;
    }
  }

  async function picked() {
    const file = picker?.files?.[0];
    if (!file) return;
    busy = true;
    try {
      const up = await upload<{ value: string }>(`/admin/api/metadata/uploads/${type}`, file, 'POST');
      busy = false;
      await choose(up.value);
    } catch (err) {
      toast(message(err), true);
    } finally {
      busy = false;
      if (picker) picker.value = '';
    }
  }
</script>

<Modal title={`${label}: ${where}`} description={`Choose one of TMDB's, in any language, or upload your own (JPEG or PNG${type === 'logo' ? ', transparent is best' : ''}).`} size="wide" {busy} {close}>
  <div class="toolbar">
    <select aria-label="Language" bind:value={filter} disabled={!choices?.length}>
      <option value="">Every language ({choices?.length ?? 0})</option>
      {#each languages as l (l)}
        <option value={l || 'none'}>{l ? languageName(l) : 'Without text'}</option>
      {/each}
    </select>
    <button type="button" disabled={busy} onclick={() => picker?.click()}>Upload…</button>
    <input bind:this={picker} type="file" accept="image/png,image/jpeg" hidden onchange={picked} />
  </div>
  {#if problem}
    <Notice warn>{problem}</Notice>
  {:else if !choices}
    <Loading />
  {:else if !shown.length}
    <p class="empty">{choices.length ? 'None in this language.' : `TMDB has no ${label.toLowerCase()} for this title: upload one.`}</p>
  {:else}
    <div class="image-choices" class:wide={type !== 'poster'} class:logos={type === 'logo'}>
      {#each shown as c (c.value)}
        <button type="button" disabled={busy} aria-pressed={current === c.url}
          aria-label={`${c.language ? languageName(c.language) : 'Without text'}${c.width ? `, ${c.width} by ${c.height}` : ''}`}
          onclick={() => choose(c.value)}>
          <img src={c.url} alt="" loading="lazy" />
          <span>{c.language ? languageName(c.language) : 'Without text'}{c.width ? ` · ${c.width}×${c.height}` : ''}</span>
        </button>
      {/each}
    </div>
  {/if}
</Modal>
