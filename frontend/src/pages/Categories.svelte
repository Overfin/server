<!-- Categories: the site's categories in their order, edited in windows,
     and whether the site's menu shows the libraries too. -->
<script lang="ts">
  import { PageHead, Card, Setting, Badge, Busy, Empty, Notice, Loading, ask, toast } from '@overfin/ui';
  import { api } from '../lib/api';
  import { message, plural } from '../lib/format';
  import CategoryEditor from '../parts/library/CategoryEditor.svelte';
  import type { Categories, Category, CategoryRules } from '../lib/types';

  let data = $state<Categories | null>(null);
  let problem = $state('');
  let shown = $state(false);
  // editing: the category whose form is open; null for a new one.
  let editing = $state<Category | null | undefined>(undefined);

  async function load() {
    try {
      data = await api<Categories>('GET', '/admin/api/categories');
      shown = data.librariesShown;
    } catch (err) {
      problem = message(err);
    }
  }

  const langName = (code: string) => { try { return new Intl.DisplayNames(['en'], { type: 'language' }).of(code) || code; } catch { return code; } };
  const nameOf = (c: Category) => c.names[data!.defaultLanguage] || Object.values(c.names)[0] || 'Unnamed';

  function summary(r: CategoryRules): string {
    const d = data!;
    const genreNames = Object.fromEntries(d.genres.map((g) => [g.id, g.name]));
    const libNames = Object.fromEntries(d.libraries.map((l) => [l.id, l.name]));
    const catNames = Object.fromEntries(d.items.map((c) => [c.id, nameOf(c)]));
    return [
      r.kinds?.length === 1 && (r.kinds[0] === 'movie' ? 'Films' : 'Series'),
      r.genres?.length && r.genres.map((g) => genreNames[g] || g).join(' or '),
      r.languages?.length && `in ${r.languages.map(langName).join(' or ')}`,
      r.libraries?.length && `from ${r.libraries.map((l) => libNames[l] || 'a library').join(' or ')}`,
      r.tags?.length && `tagged ${r.tags.join(' or ')}`,
      r.maxAge !== undefined && r.maxAge !== null && `for ages up to ${r.maxAge}`,
      r.except?.length && `except ${r.except.map((x) => catNames[x] || '?').join(', ')}`,
    ].filter(Boolean).join(' · ') || 'Every film and series';
  }

  async function setShown() {
    try {
      await api('PUT', '/admin/api/categories/libraries', { shown });
      toast(shown ? 'The libraries are in the site\'s menu.' : 'The libraries are out of the site\'s menu.');
    } catch (err) {
      shown = !shown;
      toast(message(err), true);
    }
  }

  async function move(i: number, by: number) {
    const order = data!.items.map((c) => c.id);
    [order[i], order[i + by]] = [order[i + by], order[i]];
    try {
      await api('PUT', '/admin/api/categories/order', { ids: order });
    } catch (err) {
      toast(message(err), true);
    }
    await load();
  }

  async function remove(c: Category) {
    if (!(await ask(`Delete ${nameOf(c)}? Nothing moves on disk: only the category goes.`, { yes: 'Delete', danger: true }))) return;
    await api('DELETE', `/admin/api/categories/${c.id}`);
    toast('Category deleted.');
    await load();
  }

  load();
</script>

<PageHead title="Categories" description="Sections of the site, made from rules: Films, Series, Anime, Kids… A title is in every category it fits. Nothing moves on disk.">
  {#snippet actions()}<button class="primary" type="button" onclick={() => (editing = null)}>New category</button>{/snippet}
</PageHead>

{#if problem}
  <Notice warn>{problem}</Notice>
{:else if !data}
  <Loading />
{:else}
  <Card title="Categories" description={'In the site\'s menu in this order, each with its page; a category with a home row also shows on the home page.'}>
    <div class="table-wrap">
      <table>
        <thead><tr><th>Category</th><th>Titles</th><th><span class="visually-hidden">Home row</span></th><th><span class="visually-hidden">Actions</span></th></tr></thead>
        <tbody>
          {#each data.items as c, i (c.id)}
            <tr>
              <td><div class="name">{nameOf(c)}</div><div class="hint">{summary(c.rules)}</div></td>
              <td class="hint">{plural(c.count, 'title')}</td>
              <td>{#if c.home}<Badge kind="info">Home row</Badge>{/if}</td>
              <td class="row-actions">
                <Busy small disabled={i === 0} label="Up" action={() => move(i, -1)}>↑</Busy>
                <Busy small disabled={i === data.items.length - 1} label="Down" action={() => move(i, 1)}>↓</Busy>
                <button type="button" class="small" onclick={() => (editing = c)}>Edit</button>
                <Busy kind="danger" small action={() => remove(c)}>Delete</Busy>
              </td>
            </tr>
          {:else}
            <tr><td colspan="4"><Empty>No category yet: the site shows its libraries.</Empty></td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </Card>
  <Card title={'The site\'s menu'}>
    <Setting label="Libraries in the menu" hint="With categories, you may want them alone: off, the menu shows only the categories.">
      {#snippet children(ids)}<input type="checkbox" bind:checked={shown} onchange={setShown} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
    </Setting>
  </Card>
{/if}

{#if editing !== undefined && data}
  <CategoryEditor {data} category={editing} {langName} {nameOf} saved={load} close={() => (editing = undefined)} />
{/if}
