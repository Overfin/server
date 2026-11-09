<!-- Someone's setup link, in a window: the link to choose their password,
     valid 7 days and once. Overfin emails it (opening it proves their
     address), or the admin copies it and hands it over. A new link voids
     the one before. -->
<script lang="ts">
  import { Modal, Secret, Busy, Loading, Notice, toast, day } from '@overfin/ui';
  import { api } from '../../lib/api';
  import type { Person, Registration } from '../../lib/types-people';

  let { person: x, done, close }: { person: Person; done: () => void; close: () => void } = $props();

  let mail = $state<boolean | null>(null);
  let copied = $state<{ link: string; expires: string } | null>(null);
  let problem = $state('');

  api<Registration>('GET', '/admin/api/registration').then((r) => (mail = r.mail), () => (mail = false));

  // Without the site's address, the link is only its path: this page's
  // address goes in front.
  const full = (link: string) => (link.startsWith('/') ? location.origin + link : link);

  async function send() {
    const r = await api<{ expires: string }>('POST', `/admin/api/people/${x.id}/setup-link`, { send: true });
    toast(`A link is on its way to ${x.email}, valid until ${day(r.expires)}.`);
    done();
    close();
  }

  async function copy() {
    const r = await api<{ link: string; expires: string }>('POST', `/admin/api/people/${x.id}/setup-link`, { send: false });
    copied = { link: full(r.link), expires: r.expires };
    done();
  }
</script>

<Modal title={`A setup link for ${x.email}`} description="With it, they choose their own password; you never know it. It's valid 7 days, once, and a new link voids this one." {close}>
  {#if mail === null}
    <Loading />
  {:else if copied}
    <Secret value={copied.link} after={`Valid until ${day(copied.expires)}. Anyone with it can choose this account's password: send it to ${x.email} only.`}>Copy it now: it won't be shown again.</Secret>
    {#if mail}<p class="hint">Their address isn't proven by a copied link: they'll be asked to confirm it, as People → Registration says.</p>{/if}
  {:else}
    {#if !mail}<Notice>This server doesn't send emails: copy the link and hand it to them yourself.</Notice>{/if}
    {#if problem}<Notice warn>{problem}</Notice>{/if}
  {/if}
  {#snippet foot()}
    <span class="grow"></span>
    {#if copied || mail === null}
      <button type="button" class="primary" onclick={close}>Done</button>
    {:else}
      <Busy action={copy}>Copy the link</Busy>
      {#if mail}<Busy kind="primary" action={send}>Email it to them</Busy>{/if}
    {/if}
  {/snippet}
</Modal>
