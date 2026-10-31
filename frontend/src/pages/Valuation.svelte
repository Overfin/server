<!-- Valuation: how much each criterion counts in the titles' values, with
     the most and least valuable titles updated as the sliders move. -->
<script lang="ts">
  import { PageHead, Card, Setting, Status, Busy, Notice, Loading, toast } from '@overfin/ui';
  import { api } from '../lib/api';
  import { message, numberIn, plural } from '../lib/format';
  import { route } from '../lib/router.svelte';
  import type { Valuation, Valued } from '../lib/types-library';

  route.wide = true;

  const criteria: Record<string, [string, string]> = {
    watchTime: ['Watch time', 'How much it\'s watched, in full views: two people watching half count as one watching all. Recent viewing counts more, and one account counts for three views at most.'],
    audience: ['Audience', 'How many accounts watched it, even a little.'],
    followed: ['Followed', 'For series: the season people are on and those after it, while they keep watching.'],
    favorites: ['Favorites', 'How many accounts marked it a favorite.'],
    watchlist: ['Watchlist', 'How many accounts keep it to watch later.'],
    requested: ['Requested', 'How many accounts asked for it.'],
    tastes: ['Tastes', 'How well it fits what people here like: what they watch (genres, series or films), and what they said when asked. What someone watches counts more and more over what they said.'],
    rating: ['Rating', 'TMDB\'s rating. One from a few votes counts for little.'],
    newRelease: ['New release', 'How recent the film or the season is: its release, or its first air date.'],
    recentlyAdded: ['Recently added', 'How recently it arrived here (a season: its latest episode), for about two months: what just came isn\'t the least valuable for not being watched yet.'],
    popularity: ['Popularity', 'TMDB\'s popularity: how much people look it up these days.'],
  };
  const criterionName = (c: string) => (criteria[c] || [c])[0];
  const valuedName = (t: Valued) => {
    const name = t.name || 'Untitled';
    return /_s\d+$/.test(t.id) ? `${name} · ${t.season ? `Season ${t.season}` : 'Specials'}` : name;
  };

  let data = $state<Valuation | null>(null);
  let problem = $state('');
  let weights = $state<Record<string, number>>({});
  let saved = $state<Record<string, number>>({});
  let askTastes = $state(false);
  let share = $state<number | string>(0);
  let status = $state<{ text: string; kind: '' | 'ok' | 'error' }>({ text: '', kind: '' });
  let search = $state('');
  let most = $state<Valued[] | null>(null);
  let least = $state<Valued[] | null>(null);
  let found = $state<Valued[] | null>(null);
  let selected = $state('');

  const changed = $derived(!!data && data.criteria.some((c) => weights[c.name] !== saved[c.name]));
  const current = $derived([...(most || []), ...(least || []), ...(found || [])].find((t) => t.id === selected));

  api<Valuation>('GET', '/admin/api/valuation').then(
    (d) => {
      data = d;
      weights = Object.fromEntries(d.criteria.map((c) => [c.name, c.weight]));
      saved = { ...weights };
      askTastes = d.askTastes;
      share = d.discoveryShare;
      preview();
    },
    (err) => (problem = message(err)),
  );

  let timer: ReturnType<typeof setTimeout> | undefined;
  let asked = 0;
  // preview asks for the values with the sliders as they are, a moment
  // after they stop; an answer older than the last asked is dropped.
  function preview() {
    clearTimeout(timer);
    timer = setTimeout(async () => {
      const n = ++asked;
      let res: { most: Valued[]; least: Valued[]; found: Valued[] };
      try {
        res = await api('POST', '/admin/api/valuation/preview', { weights, query: search.trim() });
      } catch (err) {
        toast(message(err), true);
        return;
      }
      if (n !== asked) return; // a newer one is on its way
      most = res.most;
      least = res.least;
      found = res.found;
    }, 150);
  }
  $effect(() => () => clearTimeout(timer));

  function slid(name: string, value: number) {
    weights[name] = value;
    status = { text: changed ? 'Not saved yet.' : '', kind: '' };
    preview();
  }

  async function save() {
    await api('PUT', '/admin/api/valuation', { weights });
    saved = { ...weights };
    status = { text: 'Saved.', kind: 'ok' };
  }

  async function reset() {
    const res = await api<Valuation>('PUT', '/admin/api/valuation', { reset: true });
    for (const c of data!.criteria) {
      const w = res.criteria.find((x) => x.name === c.name)!.weight;
      weights[c.name] = w;
      saved[c.name] = w;
    }
    status = { text: 'Back to the defaults.', kind: 'ok' };
    preview();
  }

  async function setAsk() {
    try {
      await api('PUT', '/admin/api/valuation', { askTastes });
      toast(askTastes ? 'People will be asked what they like.' : 'People won\'t be asked any more; what they watch still counts.');
      preview();
    } catch (err) {
      askTastes = !askTastes;
      toast(message(err), true);
    }
  }

  async function setShare() {
    try {
      const n = Math.round(numberIn(share, 'Discovery share', '0: the picks alone'));
      if (!(n >= 0 && n <= 100)) throw new Error('The discovery share goes from 0 to 100.');
      await api('PUT', '/admin/api/valuation', { discoveryShare: n });
      share = n;
      data!.discoveryShare = n;
      toast(n ? `${n}% of the suggestions will come from trending and popular titles.` : 'Suggestions come from the picks alone.');
    } catch (err) {
      share = data!.discoveryShare;
      toast(message(err), true);
    }
  }
</script>

{#snippet list(title: string, rows: Valued[] | null, empty: string)}
  <div class="table-wrap">
    <table>
      <thead><tr><th>{title}</th><th>Value</th></tr></thead>
      <tbody>
        {#each rows || [] as t (t.id)}
          <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
          <tr data-id={t.id} class:selected={t.id === selected} onclick={() => (selected = t.id)}>
            <td><button type="button" class="pick name" aria-pressed={t.id === selected} onclick={(e) => { e.stopPropagation(); selected = t.id; }}>{valuedName(t)}</button>{#if !t.inLibrary}<div class="hint">Not in the libraries</div>{/if}</td>
            <td class="num">{t.score.toFixed(1)}</td>
          </tr>
        {:else}
          {#if rows}<tr><td colspan="2" class="empty">{empty}</td></tr>{/if}
        {/each}
      </tbody>
    </table>
  </div>
{/snippet}

<PageHead title="Valuation" description="How much each film and each season is worth to the people here, from 0 to 100: what they watch, mark, keep for later and ask for, and what TMDB says of it. Values never depend on files or their size, and say nothing of who did what. Apps you give a key, like Overtide, read them." />

{#if problem}
  <Notice warn>{problem}</Notice>
{:else if !data}
  <Loading />
{:else}
  <div class="split">
    <div>
      <Card title="Criteria" description={`From 0 (left out) to ${data.max} (counts the most). ${plural(data.accounts, 'account')} used in the last six months count${data.accounts === 1 ? 's' : ''}; ${data.titles === 1 ? '1 film or season is' : `${data.titles} films and seasons are`} valued.`}>
        <div>
          {#each data.criteria as c (c.name)}
            <Setting label={criterionName(c.name)} hint={(criteria[c.name] || [])[1] || ''}>
              {#snippet children(ids)}
                <div class="weight">
                  <input type="range" min="0" max={String(data!.max)} step="1" value={weights[c.name]} aria-labelledby={ids.label} aria-describedby={ids.hint}
                    oninput={(e) => slid(c.name, Number(e.currentTarget.value))} />
                  <output>{weights[c.name]}</output>
                </div>
              {/snippet}
            </Setting>
          {/each}
          <Setting label="Ask people what they like" hint={'When they set up their account, and on their account page: series or films, genres, kids\' content. Off, Tastes come from what they watch alone.'}>
            {#snippet children(ids)}<input type="checkbox" bind:checked={askTastes} onchange={setAsk} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
          </Setting>
        </div>
        {#snippet foot()}
          <Status text={status.text} kind={status.kind} />
          <Busy action={reset}>Reset to defaults</Busy>
          <Busy kind="primary" disabled={!changed} action={save}>Save</Busy>
        {/snippet}
      </Card>
      <Card title="Suggestions" description="What apps you give a key, like Overtide, are told to add: the titles the libraries lack, the most valuable first.">
        <div>
          <Setting label="Discovery share" hint={'The part of the suggestions taken from TMDB\'s trending and popular titles whatever their value, so people find new things and the titles they hear about. 0 turns it off.'}>
            {#snippet children(ids)}
              <div class="weight">
                <input type="number" min="0" max="100" step="1" bind:value={share} onchange={setShare} aria-labelledby={ids.label} aria-describedby={ids.hint} />
                <span>%</span>
              </div>
            {/snippet}
          </Setting>
        </div>
      </Card>
      <div class="columns">
        <section><h2>Most valuable</h2>{@render list('In the libraries', most, 'No titles in the libraries yet.')}</section>
        <section><h2>Least valuable</h2>{@render list('In the libraries', least, 'No titles in the libraries yet.')}</section>
      </div>
      <section>
        <div class="toolbar"><input type="search" placeholder="Find a title" aria-label="Find a title" bind:value={search} oninput={preview} /></div>
        {@render list('Title', found, search.trim() ? 'Nothing matches.' : 'Search any title, in the libraries or not.')}
      </section>
    </div>
    <aside class="detail" aria-label="Why this value">
      {#if current}
        {@const t = current}
        <div class="detail-head"><div>
          <h2>{valuedName(t)}</h2>
          <div class="hint">{t.inLibrary ? 'In the libraries' : 'Not in the libraries'}</div>
        </div></div>
        <p><strong>Value {t.score.toFixed(1)}</strong> out of 100.</p>
        <p class="hint">Each criterion ranks the title among all the others, from 0 (no sign of it, or the lowest) to 100 (the highest); the value is their average, weighted by the sliders. What people do (watching, favorites, lists, requests, tastes) counts in the value but isn't shown title by title.</p>
        <div class="table-wrap">
          <table>
            <thead><tr><th>Criterion</th><th>Rank</th><th>Weight</th></tr></thead>
            <tbody>
              {#each data.criteria.filter((c) => c.name in t.parts) as c (c.name)}
                <tr>
                  <td>{criterionName(c.name)}</td>
                  <td class="num">{t.parts[c.name] > 0 ? `${Math.round(t.parts[c.name] * 100)} / 100` : 'none'}</td>
                  <td class="num">{#if weights[c.name]}× {weights[c.name]}{:else}<span class="hint">left out</span>{/if}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        {#if data.accounts < 10}
          <p class="hint">With {data.accounts} account{data.accounts === 1 ? '' : 's'} counted, what people do is pulled toward the middle, so one person's habits don't decide.</p>
        {/if}
      {:else}
        <div class="placeholder">Select a title to see why it has its value.</div>
      {/if}
    </aside>
  </div>
{/if}
