<!-- Notices: which account events the operator is told about, and where:
     the admins' email, a webhook, and the person whose access ends. -->
<script lang="ts">
  import { PageHead, Card, Field, Setting, Badge, Status, Busy, Secret, Loading, Notice, ask, toast } from '@overfin/ui';
  import { api } from '../lib/api';
  import { message, numberIn } from '../lib/format';

  interface Notices {
    joined: boolean;
    expiring: boolean;
    expired: boolean;
    days: number;
    admins: boolean;
    person: boolean;
    webhook: string;
    signed: boolean;
    managed: boolean;
    mail: boolean;
  }

  let n = $state<Notices | null>(null);
  let problem = $state('');
  let joined = $state(false);
  let expiring = $state(false);
  let expired = $state(false);
  let days = $state<number | string>(7);
  let admins = $state(false);
  let person = $state(false);
  let webhook = $state('');
  let status = $state<{ text: string; kind: '' | 'ok' | 'error' }>({ text: '', kind: '' });
  // key: the webhook's new key, shown once.
  let key = $state('');

  async function load() {
    try {
      n = await api<Notices>('GET', '/admin/api/notices');
      ({ joined, expiring, expired, days, admins, person, webhook } = n);
    } catch (err) {
      problem = message(err);
    }
  }

  const noEmail = $derived(!n ? '' : !n.managed ? 'Only with Overfin\'s own sign-in: with an identity provider, Overfin doesn\'t know people\'s addresses.'
    : !n.mail ? 'Email isn\'t set up yet (Server, Email).' : '');
  const values = () => ({ joined, expiring, expired, days: numberIn(days, 'Days before'), admins, webhook: webhook.trim(), person });
  const failed = (err: Error) => (status = { text: err.message, kind: 'error' });

  async function save() {
    await api('PUT', '/admin/api/notices', values());
    toast('Notices saved.');
    status = { text: '', kind: '' };
    await load();
  }

  async function test() {
    status = { text: 'Sending…', kind: '' };
    status = { text: (await api<{ summary: string }>('POST', '/admin/api/notices/test', values())).summary, kind: 'ok' };
  }

  async function newKey() {
    if (n!.signed && !(await ask('Make a new key? The old one stops matching: whatever checks the signatures needs the new one.', { yes: 'Make a new key' }))) return;
    const { secret } = await api<{ secret: string }>('POST', '/admin/api/notices/secret', {});
    n!.signed = true;
    key = secret;
  }

  load();
</script>

<PageHead title="Notices" description="Overfin can tell you about accounts: one made, one whose access ends soon or has ended. Never what anyone watches. All off until you switch them on." />

{#if problem}
  <Notice warn>{problem}</Notice>
{:else if !n}
  <Loading />
{:else}
  <Card title="What to tell">
    {#snippet badge()}{#if n!.joined || n!.expiring || n!.expired}<Badge kind="ok">On</Badge>{:else}<Badge kind="plain">Off</Badge>{/if}{/snippet}
    <Setting label="A new account" hint="Someone signed in for the first time, invited or not.">
      {#snippet children(ids)}<input type="checkbox" bind:checked={joined} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
    </Setting>
    <Setting label="Access ends soon" hint="An account with an end date (People) gets near it.">
      {#snippet children(ids)}<input type="checkbox" bind:checked={expiring} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
    </Setting>
    <Setting label="Days before" hint="How long before its access ends an account is noticed, and its person warned." sub>
      {#snippet children(ids)}<input type="number" min="1" max="60" bind:value={days} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
    </Setting>
    <Setting label="Access ended" hint="An account's end date has passed.">
      {#snippet children(ids)}<input type="checkbox" bind:checked={expired} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
    </Setting>
  </Card>

  <Card title="Where" description="Email, a webhook, or both.">
    <Setting label="Email the admins" hint={noEmail || 'Every admin gets them, in the server\'s default language.'}>
      {#snippet children(ids)}<input type="checkbox" bind:checked={admins} disabled={!n!.managed} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
    </Setting>
    <Field label="Webhook" wide hint={'Overfin posts JSON to this address: "event", "text" and "content" (the notice in words, as Discord and Slack read it), "account" ({id, name}), "expiresAt", "at". Leave empty for none.'}>
      <input type="url" bind:value={webhook} placeholder="https://…" autocomplete="off" spellcheck="false" />
    </Field>
    <Setting label="Signing" hint={n.signed ? 'Posts are signed.' : 'Posts aren\'t signed: make a key if what receives them checks signatures.'}>
      {#snippet children()}<Busy action={newKey}>{n!.signed ? 'Make a new key' : 'Sign the posts'}</Busy>{/snippet}
    </Setting>
    {#if key}
      <Secret value={key} after={'Each post carries X-Overfin-Signature: "sha256=" and the HMAC-SHA256 of its body with this key, in hex.'}>The webhook's key. Copy it now: it won't be shown again.</Secret>
    {/if}
  </Card>

  <Card title="Warning the person" description="Before their access ends, the person gets an email in their language, so they can ask for more time.">
    <Setting label="Email the person" hint={noEmail || 'Sent once per end date, the days set above before it.'}>
      {#snippet children(ids)}<input type="checkbox" bind:checked={person} disabled={!n!.managed} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
    </Setting>
    {#snippet foot()}
      <Status text={status.text} kind={status.kind} />
      <Busy action={test} fail={failed}>Send a test</Busy>
      <Busy kind="primary" action={save} fail={failed}>Save</Busy>
    {/snippet}
  </Card>
{/if}
