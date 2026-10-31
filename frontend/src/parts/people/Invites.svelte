<!-- Invitations, and the invite templates they're made from, on People →
     Registration. An invitation is for a number of people or any number,
     until a date or with no end; with Overfin's own sign-in, its link can
     go by email (sendInvitation). Each list has its Edit and Delete
     buttons; forms open in a window. -->
<script lang="ts">
  import { Card, Badge, Busy, Field, FormModal, Modal, Notice, Loading, Secret, ask, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { message, plural } from '../../lib/format';
  import type { Template } from '../../lib/types';

  interface InviteTemplate {
    id: string;
    name: string;
    accountTemplate: string;
    accessDays: number;
    validDays: number;
    maxUses: number;
  }
  interface Invite {
    id: string;
    label?: string;
    uses: number;
    maxUses: number;
    expiresAt?: string;
    accountTemplate: string;
    accessDays: number;
  }
  interface Made {
    link: string;
    invite: Invite;
  }

  let { sendInvitation = null }: { sendInvitation?: ((email: string, link: string) => Promise<{ summary: string }>) | null } = $props();

  const call = <T = unknown>(method: string, path: string, body?: unknown) => api<T>(method, `/admin/api/modules/invites${path}`, body);

  let templates = $state<InviteTemplate[] | null>(null);
  let invites = $state<Invite[]>([]);
  let accountTemplates = $state<Template[]>([]);
  let problem = $state('');
  let inviting = $state(false);
  // editing: the invite template whose form is open; null for a new one.
  let editing = $state<InviteTemplate | null | undefined>(undefined);
  // shown: the link just made, shown once.
  let shown = $state<{ link: string; label: string; relative: boolean } | null>(null);

  async function load() {
    try {
      const [t, i, a] = await Promise.all([
        call<{ items: InviteTemplate[] }>('GET', '/templates'), call<{ items: Invite[] }>('GET', '/invites'), call<{ items: Template[] }>('GET', '/account-templates')]);
      templates = t.items;
      invites = i.items;
      accountTemplates = a.items;
    } catch (err) {
      problem = message(err);
    }
  }

  const accountName = (id: string) => accountTemplates.find((t) => t.id === id)?.name || 'a deleted template';
  const days = (n: number) => plural(n, 'day');
  const uses = (n: number) => (n ? plural(n, 'use') : 'any number of uses');
  const status = (inv: Invite): [string, '' | 'ok' | 'err'] => (inv.maxUses && inv.uses >= inv.maxUses ? ['Used', '']
    : inv.expiresAt && new Date(inv.expiresAt) <= new Date() ? ['Expired', 'err'] : ['Waiting', 'ok']);

  // The new invitation's form.
  let forWhom = $state('');
  let from = $state('');
  function newInvite() {
    forWhom = '';
    from = templates?.[0]?.id ?? '';
    inviting = true;
  }
  async function makeInvite() {
    const out = await call<Made>('POST', '/invites', { template: from, label: forWhom.trim() });
    await load();
    // Once this window has closed: the link's, on top.
    setTimeout(() => {
      const relative = out.link.startsWith('/');
      shown = { link: relative ? location.origin.replace(/:\d+$/, '') + out.link : out.link, label: out.invite.label || 'them', relative };
    }, 0);
  }

  async function removeInvite(inv: Invite) {
    if (!(await ask('Delete this invitation? Its link stops working; accounts made with it stay.', { yes: 'Delete', danger: true }))) return;
    await call('DELETE', `/invites/${inv.id}`);
    toast('Invitation deleted.');
    await load();
  }

  // An invite template's form.
  let name = $state('');
  let account = $state('');
  let access = $state<number | string>(0);
  let valid = $state<number | string>(7);
  let maxUses = $state<number | string>(1);
  function editTemplate(t: InviteTemplate | null) {
    name = t?.name || '';
    account = t?.accountTemplate || accountTemplates[0]?.id || '';
    access = t?.accessDays ?? 0;
    valid = t?.validDays ?? 7;
    maxUses = t?.maxUses ?? 1;
    editing = t;
  }
  async function saveTemplate() {
    const t = editing;
    const body = { name: name.trim(), accountTemplate: account, accessDays: Number(access), validDays: Number(valid), maxUses: Number(maxUses) };
    await (t ? call('PUT', `/templates/${t.id}`, body) : call('POST', '/templates', body));
    toast(t ? 'Invite template saved.' : 'Invite template created.');
    await load();
  }
  async function removeTemplate(t: InviteTemplate) {
    if (!(await ask(`Delete the invite template ${t.name}? Invitations made from it stay as they are.`, { yes: 'Delete', danger: true }))) return;
    await call('DELETE', `/templates/${t.id}`);
    toast('Invite template deleted.');
    await load();
  }

  // Sending the link by email, with Overfin's own sign-in.
  let to = $state('');
  let sending = $state(false);
  async function send(event: SubmitEvent) {
    event.preventDefault();
    if (!sendInvitation || !shown) return;
    sending = true;
    try {
      const sent = await sendInvitation(to.trim(), shown.link);
      toast(sent.summary);
      to = '';
    } catch (err) {
      toast(message(err), true);
    } finally {
      sending = false;
    }
  }

  load();
</script>

{#if problem}
  <Notice warn>{problem}</Notice>
{:else if !templates}
  <Loading />
{:else}
  <Card title="Invitations" description="A link that lets someone set up an account here, shaped by an invite template.">
    {#snippet badge()}
      {#if templates?.length}<button type="button" class="primary" onclick={newInvite}>New invitation</button>{/if}
    {/snippet}
    {#if !templates.length}<p class="notice">Create an invite template first, below.</p>{/if}
    <div class="table-wrap">
      <table>
        <thead><tr><th>For</th><th>Status</th><th>Gives</th><th>Link works until</th><th></th></tr></thead>
        <tbody>
          {#each invites as inv (inv.id)}
            {@const [text, kind] = status(inv)}
            <tr>
              <td>
                <div class="name">{inv.label || 'Anyone with the link'}</div>
                <div class="hint">{inv.maxUses ? `${inv.uses} of ${uses(inv.maxUses)}` : `${inv.uses} used, ${uses(0)}`}</div>
              </td>
              <td><Badge {kind}>{text}</Badge></td>
              <td class="hint">{`${accountName(inv.accountTemplate)}, ${inv.accessDays ? days(inv.accessDays) : 'no end'}`}</td>
              <td class="hint">{inv.expiresAt ? new Date(inv.expiresAt).toLocaleDateString() : 'No end'}</td>
              <td class="row-actions"><Busy small kind="danger" action={() => removeInvite(inv)}>Delete</Busy></td>
            </tr>
          {:else}
            <tr><td colspan="5" class="empty">No invitations yet.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </Card>

  <Card title="Invite templates" description="What an invitation gives: an account template, how long access lasts, how long the link works and how many people can use it.">
    {#snippet badge()}<button type="button" class="primary" onclick={() => editTemplate(null)}>New invite template</button>{/snippet}
    <div class="table-wrap">
      <table>
        <thead><tr><th>Name</th><th>Access lasts</th><th>Link works</th><th></th></tr></thead>
        <tbody>
          {#each templates as t (t.id)}
            <tr>
              <td><div class="name">{t.name}</div><div class="hint">{`Account: ${accountName(t.accountTemplate)}`}</div></td>
              <td class="hint">{t.accessDays ? days(t.accessDays) : 'No end'}</td>
              <td class="hint">{`${t.validDays ? days(t.validDays) : 'no end'}, ${uses(t.maxUses)}`}</td>
              <td class="row-actions">
                <button type="button" class="small" onclick={() => editTemplate(t)}>Edit</button>
                <Busy small kind="danger" action={() => removeTemplate(t)}>Delete</Busy>
              </td>
            </tr>
          {:else}
            <tr><td colspan="4" class="empty">No invite templates yet.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </Card>
{/if}

{#if inviting && templates}
  <FormModal title="New invitation" description="A link that lets someone set up an account here, shaped by an invite template." saveLabel="Make the link" save={makeInvite} close={() => (inviting = false)}>
    <div class="fields">
      <Field label="For" hint="Who it's for, to find it again in the list." wide><input bind:value={forWhom} maxlength="64" placeholder="e.g. Bob" /></Field>
      <Field label="From" wide>
        <select bind:value={from}>{#each templates as t (t.id)}<option value={t.id}>{t.name}</option>{/each}</select>
      </Field>
    </div>
  </FormModal>
{/if}

{#if editing !== undefined}
  <FormModal title={editing ? `Edit ${editing.name}` : 'New invite template'} saveLabel={editing ? 'Save' : 'Create'} save={saveTemplate} close={() => (editing = undefined)}
    description="What an invitation gives: an account template, how long access lasts, how long the link works and how many people can use it.">
    <div class="fields">
      <Field label="Name" wide><input bind:value={name} maxlength="64" placeholder="e.g. Friends" required /></Field>
      <Field label="Account template" hint="What the account may do." wide>
        <select bind:value={account}>
          {#each accountTemplates as a (a.id)}
            <option value={a.id}>{`${a.name} · ${plural(a.maxStreams, 'stream')}, ${plural(a.maxProfiles, 'profile')}`}</option>
          {/each}
        </select>
      </Field>
      <Field label="Access lasts" hint="In days; 0: no end."><input type="number" min="0" max="3650" bind:value={access} /></Field>
      <Field label="Link works" hint="In days; 0: no end."><input type="number" min="0" max="3650" bind:value={valid} /></Field>
      <Field label="Uses" hint="How many people; 0: any number."><input type="number" min="0" max="100000" bind:value={maxUses} /></Field>
    </div>
  </FormModal>
{/if}

{#if shown}
  {@const close = () => (shown = null)}
  <Modal title="The invitation's link" {close}>
    <Secret value={shown.link} after={shown.relative ? 'Check the address in front: it must be your site\'s.' : ''}>
      {`Send this link to ${shown.label}, by email, a message, anything. It's shown only now: copy it before closing this window.`}
    </Secret>
    {#if sendInvitation}
      <form class="toolbar" onsubmit={send}>
        <Field label="Or send it to"><input type="email" bind:value={to} placeholder="name@example.org" aria-label="Email" /></Field>
        <button type="submit" disabled={sending}>Send it by email</button>
      </form>
    {/if}
    {#snippet foot()}
      <span class="grow"></span>
      <button type="button" class="primary" onclick={close}>Done</button>
    {/snippet}
  </Modal>
{/if}
