<!-- Intro Skipper on Overfin's Jellyfin, installed from here; an install
     shows how it goes. -->
<script lang="ts">
  import { onDestroy } from 'svelte';
  import { Card, Badge, Status, Busy, ask, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { message } from '../../lib/format';
  import type { IntroSkipper, Line } from '../../lib/types-server';

  let { sk, reload }: { sk: IntroSkipper; reload: () => void } = $props();

  const installed = $derived(sk.status.installed);
  const describe = (s: IntroSkipper['status']) => (s.installed ? `Version ${s.version}: ${s.state === 'Active' ? 'active' : s.state}.` : 'Not installed.');
  // svelte-ignore state_referenced_locally
  let st = $state<Line>({ text: describe(sk.status), kind: sk.status.installed && sk.status.state !== 'Active' ? 'error' : '' });
  let running = $state(false);
  // Leaving the page stops the following: no more asking every 2 s.
  let gone = false;
  onDestroy(() => (gone = true));

  async function watch() {
    while (!gone) {
      const now = await api<IntroSkipper>('GET', '/admin/api/intro-skipper');
      if (!now.progress.running) {
        if (now.progress.error) throw new Error(now.progress.error);
        toast(now.status.installed ? 'Intro Skipper is installed.' : 'Intro Skipper is removed.');
        reload();
        return;
      }
      st = { text: now.progress.step || 'Installing…', kind: '' };
      await new Promise((r) => setTimeout(r, 2000));
    }
  }

  const fail = (err: Error) => (st = { text: message(err), kind: 'error' });

  // An install or removal under way when the page opened: followed, the
  // buttons off meanwhile.
  async function follow() {
    running = true;
    try {
      await watch();
    } catch (err) {
      fail(err instanceof Error ? err : new Error(String(err)));
    } finally {
      running = false;
    }
  }

  async function install() {
    if (!(await ask(`Overfin adds Intro Skipper's repository to its Jellyfin, installs version ${sk.version}, and restarts Jellyfin: playback stops for a few seconds. Install it?`, { yes: 'Install' }))) return;
    await api('POST', '/admin/api/intro-skipper', {});
    await watch();
  }

  async function remove() {
    if (!(await ask('Overfin uninstalls Intro Skipper, removes its repository from its Jellyfin, and restarts Jellyfin: playback stops for a few seconds, and the player no longer offers to skip intros and credits. Remove it?', { yes: 'Remove', danger: true }))) return;
    await api('DELETE', '/admin/api/intro-skipper');
    await watch();
  }

  // svelte-ignore state_referenced_locally
  if (sk.progress.running) follow();
</script>

<Card title="Intro Skipper" description="A Jellyfin plugin, by the Intro Skipper project, that finds intros and credits: the player then offers to skip them.">
  {#snippet badge()}
    {#if installed}
      <Badge kind={sk.status.state === 'Active' ? 'ok' : 'warn'}>{sk.status.state === 'Active' ? 'Installed' : sk.status.state}</Badge>
    {:else}
      <Badge kind="plain">Not installed</Badge>
    {/if}
  {/snippet}
  <span hidden></span>
  {#snippet foot()}
    <Status text={st.text} kind={st.kind} />
    <!-- Busy: the focus comes back to the button after the question. -->
    {#if !installed}<Busy kind="primary" disabled={running} action={install} {fail}>Install</Busy>{/if}
    {#if installed}<Busy kind="danger" disabled={running} action={remove} {fail}>Remove</Busy>{/if}
  {/snippet}
</Card>
