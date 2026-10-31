<!-- Overfin's emails: whose templates write each, and a sample of each
     sent to the admin. -->
<script lang="ts">
  import { Card, Badge, Setting, Status, Busy } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { message } from '../../lib/format';
  import type { Line, Mail, MailTemplates } from '../../lib/types-server';

  let { t, m }: { t: MailTemplates; m: Mail | null } = $props();

  const emailNames: Record<string, string> = { verify: 'Confirming an address', reset: 'A new password', exists: 'Already an account', invite: 'Invitation',
    test: 'Test', joined: 'Notice: a new account', expiring: 'Notice: access ends soon', expired: 'Notice: access ended',
    ending: 'To the person: their access ends soon' };
  const on = $derived(Boolean(m && m.host));
  let st = $state<Line>({ text: '', kind: '' });

  async function send(name: string) {
    const out = await api<{ summary: string }>('POST', `/admin/api/mail/templates/${name}/test`, {});
    st = { text: out.summary, kind: 'ok' };
  }
</script>

<Card title="Emails" description={`What people get, in their language. Overfin's own, or yours: put templates of the same names in ${t.dir} (docs/branding-and-emails.md, in Overfin's repository, says how).`}>
  {#each t.emails as e (e.name)}
    {@const yours = [e.html, e.text].filter(Boolean)}
    <Setting label={emailNames[e.name] || e.name} hint={yours.length ? yours.join(', ') : ''}>
      {#snippet children()}
        <div class="row">
          {#if yours.length}<Badge kind="info">Yours</Badge>{:else}<Badge kind="plain">Overfin's</Badge>{/if}
          <Busy disabled={!on} action={() => send(e.name)} fail={(err) => (st = { text: message(err), kind: 'error' })}>Send me this one</Busy>
        </div>
      {/snippet}
    </Setting>
  {/each}
  {#snippet foot()}<Status text={st.text} kind={st.kind} />{/snippet}
</Card>
