<!-- Registration: who may make an account here, and how. With Overfin's
     own sign-in: the password rules, and invitations, which need email
     (the person's address is confirmed by a link). With an identity
     provider: invitations, and whether accounts are only by invitation. -->
<script lang="ts">
  import { PageHead, Card, Setting, Status, Busy, Loading } from '@overfin/ui';
  import { api } from '../lib/api';
  import { message, numberIn } from '../lib/format';
  import type { FeaturesState, Registration } from '../lib/types-people';
  import Invites from '../parts/people/Invites.svelte';

  let loaded = $state(false);
  let reg = $state<Registration | null>(null);
  let features = $state<FeaturesState | null>(null);
  let status = $state<{ text: string; kind: '' | 'ok' | 'error' }>({ text: '', kind: '' });

  // The password rules' fields.
  let minLength = $state<number | string>(10);
  let digit = $state(false);
  let special = $state(false);
  // With an identity provider: accounts only by invitation.
  let only = $state(false);

  async function load() {
    reg = await api<Registration>('GET', '/admin/api/registration').catch(() => null);
    if (reg) {
      ({ minLength, digit, special } = reg.rules);
    } else {
      features = await api<FeaturesState>('GET', '/admin/api/features');
      only = features.features.modules?.invites?.required === 'on';
    }
    loaded = true;
  }

  async function saveRules() {
    const out = await api<Registration>('PUT', '/admin/api/registration', { rules: { minLength: numberIn(minLength, 'Minimum length'), digit, special } });
    status = { text: 'Saved: every new password follows them.', kind: 'ok' };
    Object.assign(reg!, out);
  }

  async function saveOnly() {
    const f = features!.features;
    f.modules = f.modules || {};
    f.modules.invites = { ...(f.modules.invites || {}), required: only ? 'on' : 'off' };
    await api('PUT', '/admin/api/features', f);
    status = { text: 'Saved.', kind: 'ok' };
  }

  const fail = (err: Error) => (status = { text: message(err), kind: 'error' });

  load();
</script>

<PageHead title="Registration" description={!loaded ? '' : reg
  ? 'Who can make an account here: with an invitation, choosing their password, then confirming their address by email.'
  : 'Invitations, and who gets an account.'} />

{#if !loaded}
  <Loading />
{:else}
  {#if reg}
    <Card title="Passwords" description="What every new password must have: registering, a forgotten one, the account page, and those you set.">
      <Setting label="Minimum length" hint="From 10 to 64 characters.">
        {#snippet children(ids)}<input type="number" min="10" max="64" bind:value={minLength} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
      <Setting label="A digit">
        {#snippet children(ids)}<input type="checkbox" bind:checked={digit} aria-labelledby={ids.label} />{/snippet}
      </Setting>
      <Setting label="A special character" hint="Anything but a letter or a digit; a space counts.">
        {#snippet children(ids)}<input type="checkbox" bind:checked={special} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
      {#snippet foot()}
        <Status text={status.text} kind={status.kind} />
        <Busy kind="primary" action={saveRules} {fail}>Save</Busy>
      {/snippet}
    </Card>
    {#if !reg.mail}
      <p class="notice">Invitations need email (Server → Email) and the site's address (site.url): the person confirms their address by a link sent there. Until then, accounts are made by you, on People.</p>
    {/if}
  {:else}
    <Card title="Who gets an account" description="People sign in at your identity provider; their account here is made at their first visit.">
      <Setting label="Accounts only by invitation" hint="Without one, nobody gets an account; admins and people bringing a previous account still do.">
        {#snippet children(ids)}<input type="checkbox" bind:checked={only} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
      {#snippet foot()}
        <Status text={status.text} kind={status.kind} />
        <Busy kind="primary" action={saveOnly} {fail}>Save</Busy>
      {/snippet}
    </Card>
  {/if}
  {#if !reg || reg.mail}
    <Invites sendInvitation={reg ? (email, link) => api<{ summary: string }>('POST', '/admin/api/registration/send', { email, link }) : null} />
  {/if}
{/if}
