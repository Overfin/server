<!-- Server: what Overfin connects to, each part tested before it's saved.
     A part that changes the others reloads the whole page. -->
<script lang="ts">
  import { PageHead, Loading } from '@overfin/ui';
  import { api } from '../lib/api';
  import type { Catalog, IntroSkipper, Jellyfin, Languages, Mail, MailTemplates, Previews, Ratings, Seerr, Transcoding } from '../lib/types-server';
  import JellyfinCard from '../parts/server/JellyfinCard.svelte';
  import TranscodingCard from '../parts/server/TranscodingCard.svelte';
  import PreviewsCard from '../parts/server/PreviewsCard.svelte';
  import CatalogCard from '../parts/server/CatalogCard.svelte';
  import MailCard from '../parts/server/MailCard.svelte';
  import EmailsCard from '../parts/server/EmailsCard.svelte';
  import SeerrCard from '../parts/server/SeerrCard.svelte';
  import LanguagesCard from '../parts/server/LanguagesCard.svelte';
  import TmdbCard from '../parts/server/TmdbCard.svelte';
  import IntroSkipperCard from '../parts/server/IntroSkipperCard.svelte';
  import InFile from '../parts/server/InFile.svelte';

  interface Data {
    jellyfin: Jellyfin | null;
    transcoding: Transcoding | null;
    previews: Previews | null;
    mail: Mail | null;
    catalog: Catalog | null;
    seerr: Seerr | null;
    ratings: Ratings | null;
    languages: Languages | null;
    skipper: IntroSkipper | null;
    templates: MailTemplates | null;
  }

  let data = $state<Data | null>(null);
  // Each load makes the cards again, from what the server says now.
  let version = $state(0);

  const get = <T,>(path: string) => api<T>('GET', path).catch(() => null);

  async function load() {
    const [jellyfin, transcoding, previews, mail, catalog, seerr, ratings, languages, skipper, templates] = await Promise.all([
      get<Jellyfin>('/admin/api/jellyfin'),
      get<Transcoding>('/admin/api/transcoding'),
      get<Previews>('/admin/api/previews'),
      get<Mail>('/admin/api/mail'),
      get<Catalog>('/admin/api/catalog'),
      get<Seerr>('/admin/api/seerr'),
      get<Ratings>('/admin/api/ratings'),
      get<Languages>('/admin/api/languages'),
      get<IntroSkipper>('/admin/api/intro-skipper'),
      get<MailTemplates>('/admin/api/mail/templates'),
    ]);
    data = { jellyfin, transcoding, previews, mail, catalog, seerr, ratings, languages, skipper, templates };
    version++;
  }

  const reload = () => void load();

  load();
</script>

<PageHead title="Server" description="What Overfin connects to. Every change is tested before it's saved, and applies at once." />

{#if !data}
  <Loading />
{:else}
  {#key version}
    <div class="cards">
      {#if data.jellyfin}<JellyfinCard j={data.jellyfin} />{/if}
      {#if data.transcoding}<TranscodingCard t={data.transcoding} />{/if}
      {#if data.previews}<PreviewsCard pv={data.previews} {reload} />{/if}
      {#if data.catalog}<CatalogCard c={data.catalog} />{/if}
      {#if data.mail}
        {#if data.mail.inFile}
          <InFile title="Email" text={`${data.mail.host}:${data.mail.port}, from ${data.mail.from}`} />
        {:else}
          <MailCard m={data.mail} {reload} />
        {/if}
      {/if}
      {#if data.templates}<EmailsCard t={data.templates} m={data.mail} />{/if}
      {#if data.seerr}<SeerrCard sr={data.seerr} {reload} />{/if}
      {#if data.languages}<LanguagesCard l={data.languages} />{/if}
      {#if data.ratings}<TmdbCard rt={data.ratings} {reload} />{/if}
      {#if data.skipper}<IntroSkipperCard sk={data.skipper} {reload} />{/if}
    </div>
  {/key}
{/if}
