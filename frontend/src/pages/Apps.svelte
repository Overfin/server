<!-- Apps: the keys other apps (Overtide…) read Overfin's values and
     statistics with: made here, for what the operator ticks, shown once,
     revoked here. -->
<script lang="ts">
  import { PageHead, Card, Field, FormModal, Modal, Secret, Empty, Loading, Notice, ask, toast } from '@overfin/ui';
  import { api } from '../lib/api';
  import { ago, day, message } from '../lib/format';

  interface Key {
    id: string;
    name: string;
    scope: string;
    createdAt: string;
    lastUsedAt?: string;
  }

  const scopeNames: Record<string, string> = { score: 'Values', stats: 'Watch statistics', availability: 'In the libraries', history: 'What came and went', 'watch-stats': 'Watch statistics' };
  const scopes = ['score', 'stats', 'availability', 'history'];

  let items = $state<Key[] | null>(null);
  let problem = $state('');
  let making = $state(false);
  let name = $state('');
  let picked = $state<string[]>([...scopes]);
  // made: the new key, shown once.
  let made = $state<{ name: string; key: string } | null>(null);

  async function load() {
    try {
      ({ items } = await api<{ items: Key[] }>('GET', '/admin/api/apps'));
    } catch (err) {
      problem = message(err);
    }
  }

  function newKey() {
    name = '';
    picked = [...scopes];
    making = true;
  }

  async function make() {
    const k = await api<{ name: string; key: string }>('POST', '/admin/api/apps', { name, scopes: picked });
    await load();
    // Once this window has closed: the key's, on top.
    setTimeout(() => (made = k), 0);
  }

  async function revoke(k: Key) {
    if (!(await ask(`Revoke ${k.name}'s key? The app stops getting anything.`, { yes: 'Revoke', danger: true }))) return;
    try {
      await api('DELETE', `/admin/api/apps/${k.id}`);
      toast('Key revoked.');
    } catch (err) {
      toast(message(err), true);
    }
    await load();
  }

  const scopeText = (k: Key) => [...new Set(k.scope.split(' ').map((s) => scopeNames[s] || s))].join(', ');

  load();
</script>

<PageHead title="Apps" description="Other apps (Overtide…) can read each film's and season's value, how much it's watched (plays, viewers, favorites, the day last played) and whether the libraries have it, as each key allows. Totals per title only, never anything about a person; counts below 2 are given as 0, and so are plays and favorites unless two people account for them.">
  {#snippet actions()}<button type="button" class="primary" onclick={newKey}>New key</button>{/snippet}
</PageHead>

{#if problem}
  <Notice warn>{problem}</Notice>
{:else if !items}
  <Loading />
{:else}
  <Card title="Keys">
    <div class="table-wrap">
      <table>
        <thead><tr><th>App</th><th>Made</th><th>Last used</th><th></th></tr></thead>
        <tbody>
          {#each items as k (k.id)}
            <tr>
              <td><div class="name">{k.name}</div><div class="hint">{scopeText(k)}</div></td>
              <td class="hint">{day(k.createdAt)}</td>
              <td class="hint">{k.lastUsedAt ? ago(k.lastUsedAt) : 'never'}</td>
              <td class="row-actions"><button type="button" class="small danger" onclick={() => revoke(k)}>Revoke</button></td>
            </tr>
          {:else}
            <tr><td colspan="4"><Empty>No app has a key yet.</Empty></td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </Card>
{/if}

{#if making}
  <FormModal title="New key" description="For one app: what it may read." saveLabel="Make the key" save={make} close={() => (making = false)}>
    <Field label="For" wide><input type="text" bind:value={name} placeholder="e.g. Overtide" maxlength="64" required /></Field>
    <div class="setting stack">
      <div><div class="label">It may read</div></div>
      <div class="checks">
        {#each scopes as s (s)}<label class="inline"><input type="checkbox" value={s} bind:group={picked} /> {scopeNames[s]}</label>{/each}
      </div>
    </div>
  </FormModal>
{/if}

{#if made}
  <Modal title={`${made.name}'s key`} close={() => (made = null)}>
    <Secret value={made.key} after={'The app sends it as "Authorization: Bearer <key>" to GET /apps/v1/titles on this address.'}>Copy it now: it won't be shown again.</Secret>
    {#snippet foot()}
      <span class="grow"></span>
      <button type="button" class="primary" onclick={() => (made = null)}>Done</button>
    {/snippet}
  </Modal>
{/if}
