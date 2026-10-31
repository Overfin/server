<!-- People: everyone with an account here, and, with Overfin's own
     sign-in, everyone who can sign in. A list, and the selected account's
     detail beside it. -->
<script lang="ts">
  import { PageHead, Card, Avatar, Badge, Notice, Loading } from '@overfin/ui';
  import { api } from '../lib/api';
  import { ago, message } from '../lib/format';
  import { route } from '../lib/router.svelte';
  import type { Template } from '../lib/types';
  import type { Account, Person } from '../lib/types-people';
  import { lastActive, nameOf, statusKind, statusText, subline } from '../parts/people/names';
  import AccountDetail from '../parts/people/AccountDetail.svelte';
  import PersonControls from '../parts/people/PersonControls.svelte';
  import AddPerson from '../parts/people/AddPerson.svelte';

  $effect(() => {
    route.wide = true;
  });

  let items = $state<Account[] | null>(null);
  let directory = $state('');
  let templates = $state<Template[]>([]);
  let people = $state<Person[] | null>(null);
  let problem = $state('');
  let selected = $state('');
  let search = $state('');
  let filter = $state('');
  let adding = $state(false);

  async function load(id = selected) {
    try {
      const [accounts, tmpl, ppl] = await Promise.all([
        api<{ items: Account[]; directory: string }>('GET', '/admin/api/accounts'),
        api<{ items: Template[] }>('GET', '/admin/api/templates'),
        // Only with managed sign-in: who may sign in, by email.
        api<{ items: Person[] }>('GET', '/admin/api/people').then((r) => r.items).catch(() => null),
      ]);
      items = accounts.items;
      directory = accounts.directory;
      templates = tmpl.items;
      people = ppl;
      selected = id;
    } catch (err) {
      problem = message(err);
    }
  }

  const shown = $derived.by(() => {
    const q = search.trim().toLowerCase();
    return (items ?? []).filter((a) => (!filter || a.status === filter) &&
      (!q || [a.label, a.person?.name, a.person?.username, a.person?.email, a.subject, a.id, ...a.profiles.map((p) => p.name)]
        .some((v) => (v || '').toLowerCase().includes(q))));
  });
  const current = $derived(items?.find((a) => a.id === selected));
  const notice = $derived(!people ? ({
    none: 'Names come from the identity provider through a connector (see [connector] in the configuration); until then, labels tell accounts apart.',
    unreachable: 'The identity provider\'s connector didn\'t answer, so names are missing for now. The log says why.',
  } as Record<string, string>)[directory] : '');
  const waiting = $derived(people ? people.filter((x) => !x.accountId) : []);

  load();
</script>

<PageHead title="People" description={people
  ? 'Everyone who can sign in, and their accounts. People sign in with their email and their password.'
  : 'Everyone with an account here.'}>
  {#snippet actions()}
    {#if people}<button class="primary" type="button" onclick={() => (adding = true)}>Add a person</button>{/if}
  {/snippet}
</PageHead>

{#if problem}
  <Notice warn>{problem}</Notice>
{:else if !items}
  <Loading />
{:else}
  <div class="split">
    <div>
      <div class="toolbar">
        <input type="search" bind:value={search} placeholder="Search by name, email or profile" aria-label="Search" />
        <select bind:value={filter} aria-label="Status">
          <option value="">Every status</option>
          {#each Object.entries(statusText) as [v, t] (v)}<option value={v}>{t}</option>{/each}
        </select>
      </div>
      {#if notice}<p class="notice">{notice}</p>{/if}
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>{`Accounts (${items.length})`}</th><th>Status</th><th class="hide-medium">Template</th>
              <th>Profiles</th><th class="hide-narrow">Last active</th>
            </tr>
          </thead>
          <tbody>
            {#each shown as a (a.id)}
              {@const kids = a.profiles.filter((p) => p.kind === 'kids').length}
              <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
              <tr data-id={a.id} class:selected={a.id === selected} onclick={() => (selected = a.id)}>
                <td>
                  <div class="who">
                    <Avatar name={nameOf(a)} />
                    <div>
                      <div class="name">{nameOf(a)}</div>
                      <div class="hint">{subline(a) || (a.person ? '' : 'name unknown')}</div>
                    </div>
                  </div>
                </td>
                <td>
                  <Badge kind={statusKind[a.status] ?? ''}>{statusText[a.status] || a.status}</Badge>
                  {#if a.person && !a.person.active}<div class="hint">off at the identity provider</div>{/if}
                </td>
                <td class="hide-medium">{a.template.name}</td>
                <td class="num">{a.profiles.length}{#if kids > 0}<span class="hint">{` · ${kids} kids`}</span>{/if}</td>
                <td class="hint hide-narrow">{ago(lastActive(a)) || 'never'}</td>
              </tr>
            {:else}
              <tr><td colspan="5" class="empty">{items.length ? 'Nobody matches.' : 'No accounts yet.'}</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if waiting.length > 0}
        <Card title={`No account yet (${waiting.length})`} description="They can sign in; their account is made when they first open the site.">
          <ul class="list">
            {#each waiting as x (x.id)}
              <li><Avatar name={x.email} /><div><PersonControls person={x} reload={() => load()} /></div></li>
            {/each}
          </ul>
        </Card>
      {/if}
    </div>
    <aside class="detail">
      {#if current}
        {#key current}
          <AccountDetail account={current} {templates} {people} reload={(id) => load(id)} />
        {/key}
      {:else}
        <div class="placeholder">Select someone to see their account.</div>
      {/if}
    </aside>
  </div>
{/if}

{#if adding}
  <AddPerson saved={() => load()} close={() => (adding = false)} />
{/if}
