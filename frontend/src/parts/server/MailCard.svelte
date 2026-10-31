<!-- Email: for invitations, address confirmations and forgotten
     passwords; a test email goes to the admin before it's saved. -->
<script lang="ts">
  import { Card, Badge, Field, Status, Busy, ask, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { message } from '../../lib/format';
  import type { Line, Mail } from '../../lib/types-server';

  let { m, reload }: { m: Mail; reload: () => void } = $props();

  // svelte-ignore state_referenced_locally
  let host = $state(m.host || '');
  // svelte-ignore state_referenced_locally
  let tls = $state(m.tls || 'starttls');
  // svelte-ignore state_referenced_locally
  let port = $state<number | string>(m.port || '');
  // svelte-ignore state_referenced_locally
  let from = $state(m.from || '');
  // svelte-ignore state_referenced_locally
  let username = $state(m.username || '');
  let password = $state('');
  // svelte-ignore state_referenced_locally
  const on = Boolean(m.host);
  let st = $state<Line>({ text: on ? 'Invitations and forgotten passwords work by email.' : 'Without email, you add people yourself and set their passwords.', kind: '' });
  const fail = (err: Error) => (st = { text: message(err), kind: 'error' });

  type Body = { host: string; port: number; tls: string; from: string; username: string; password: string };
  async function send(body: Body) {
    if (!body.host.trim() && !on) {
      toast('Nothing to save.');
      return;
    }
    st = { text: body.host ? 'Sending you a test email…' : 'Turning email off…', kind: '' };
    const out = await api<{ summary: string }>('PUT', '/admin/api/mail', body);
    st = { text: out.summary, kind: 'ok' };
    toast(body.host ? 'Email saved.' : 'Email turned off.');
    reload();
  }

  const save = () => send({ host, port: Number(port) || 0, tls, from, username, password });

  async function off() {
    if (await ask('Turn email off? Invitations and forgotten-password emails stop; you will set passwords yourself.', { yes: 'Turn off', danger: true })) {
      await send({ host: '', port: 0, tls: '', from: '', username: '', password: '' });
    }
  }
</script>

<Card title="Email" description="For invitations, address confirmations and forgotten passwords. Your email provider gives these settings; a test email goes to you before they're saved.">
  {#snippet badge()}{#if on}<Badge kind="ok">On</Badge>{:else}<Badge>Off</Badge>{/if}{/snippet}
  <div class="fields">
    <Field label="SMTP server" wide><input bind:value={host} autocomplete="off" spellcheck="false" placeholder="smtp.example.org" /></Field>
    <Field label="Encryption" hint="STARTTLS goes with port 587, TLS with 465. None: a relay on your network.">
      <select bind:value={tls}><option value="starttls">STARTTLS</option><option value="tls">TLS</option><option value="none">None</option></select>
    </Field>
    <Field label="Port"><input type="number" min="1" max="65535" bind:value={port} placeholder="The usual one" /></Field>
    <Field label="Sender" hint="The address emails come from." wide><input bind:value={from} autocomplete="off" placeholder="Media <media@example.org>" /></Field>
    <Field label="Username" hint="Often the sender's address."><input bind:value={username} autocomplete="off" spellcheck="false" /></Field>
    <Field label="Password" hint={m.hasPassword ? 'Empty keeps the current one.' : ''}><input type="password" bind:value={password} autocomplete="new-password" placeholder={m.hasPassword ? 'Unchanged' : ''} /></Field>
  </div>
  {#snippet foot()}
    <Status text={st.text} kind={st.kind} />
    {#if on}<Busy action={off} {fail}>Turn off</Busy>{/if}
    <Busy kind="primary" action={save} {fail}>Send a test and save</Busy>
  {/snippet}
</Card>
