<!-- Templates: what accounts may do. Every account follows one; accounts
     follow theirs live: saving changes every account on it at once. -->
<script lang="ts">
  import { PageHead, Card, Badge, Empty, Loading, Notice, ask, toast } from '@overfin/ui';
  import { api } from '../lib/api';
  import { message, plural } from '../lib/format';
  import TemplateForm from '../parts/TemplateForm.svelte';
  import type { Categories, Template } from '../lib/types';

  let items = $state<Template[] | null>(null);
  let cats = $state<Categories | null>(null);
  let problem = $state('');
  // editing: the template whose form is open; null for a new one.
  let editing = $state<Template | null | undefined>(undefined);

  async function load() {
    try {
      [{ items }, cats] = await Promise.all([api<{ items: Template[] }>('GET', '/admin/api/templates'), api<Categories>('GET', '/admin/api/categories')]);
    } catch (err) {
      problem = message(err);
    }
  }

  const catName = (id: string) => {
    const c = cats?.items.find((x) => x.id === id);
    return c ? c.names[cats!.defaultLanguage] || Object.values(c.names)[0] || 'Unnamed' : '';
  };
  const only = (t: Template) => (t.categories || []).map(catName).filter(Boolean);

  async function remove(t: Template) {
    if (!(await ask(`Delete the template "${t.name}"?`, { yes: 'Delete', danger: true }))) return;
    try {
      await api('DELETE', `/admin/api/templates/${t.id}`);
      toast('Template deleted.');
    } catch (err) {
      toast(message(err), true);
    }
    await load();
  }

  load();
</script>

<PageHead title="Templates" description="What accounts may do. Every account follows one: Default, unless you choose another for it in People.">
  {#snippet actions()}<button type="button" class="primary" onclick={() => (editing = null)}>New template</button>{/snippet}
</PageHead>

{#if problem}
  <Notice warn>{problem}</Notice>
{:else if !items || !cats}
  <Loading />
{:else}
  <Card title="Templates">
    <div class="table-wrap">
      <table>
        <thead><tr><th>Template</th><th>Streams</th><th>Profiles</th><th>Accounts</th><th><span class="visually-hidden">Actions</span></th></tr></thead>
        <tbody>
          {#each items as t (t.id)}
            <tr>
              <td>
                <div class="name">{t.name}{#if t.default}{' '}<Badge kind="info">Default</Badge>{/if}</div>
                <div class="hint">{only(t).length ? `Only ${only(t).join(', ')}` : 'Every category'}</div>
              </td>
              <td>{t.maxStreams}{#if t.reserveStreams}<div class="hint">one per profile</div>{/if}</td>
              <td>{t.maxProfiles}</td>
              <td>{t.accounts}</td>
              <td class="row-actions">
                <button type="button" class="small" onclick={() => (editing = t)}>Edit</button>
                {#if !t.default}
                  <!-- Only one no account follows: move them first, in People. -->
                  <button type="button" class="small danger" disabled={t.accounts > 0} onclick={() => remove(t)}
                    title={t.accounts > 0 ? `${plural(t.accounts, 'account')} follow it: move them to another template first, in People.` : undefined}>Delete</button>
                {/if}
              </td>
            </tr>
          {:else}
            <tr><td colspan="5"><Empty>No template yet.</Empty></td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </Card>
{/if}

{#if editing !== undefined && cats}
  <TemplateForm template={editing} {cats} saved={load} close={() => (editing = undefined)} />
{/if}
