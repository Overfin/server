<!-- Intro Skipper on Overfin's Jellyfin, installed from here; an install
     shows how it goes. -->
<script lang="ts">
  import { Card, Badge, Status, ask, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { message } from '../../lib/format';
  import type { IntroSkipper, Line } from '../../lib/types-server';

  let { sk, reload }: { sk: IntroSkipper; reload: () => void } = $props();

  const installed = $derived(sk.status.installed);
  const describe = (s: IntroSkipper['status']) => (s.installed ? `Version ${s.version}: ${s.state === 'Active' ? 'active' : s.state}.` : 'Not installed.');
  // svelte-ignore state_referenced_locally
  let st = $state<Line>({ text: describe(sk.status), kind: sk.status.installed && sk.status.state !== 'Active' ? 'error' : '' });
  let running = $state(false);

  async function watch() {
    for (;;) {
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

  async function run(action: () => Promise<void>) {
    running = true;
    try {
      await action();
    } catch (err) {
      st = { text: message(err), kind: 'error' };
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

  // An install or removal under way when the page opened: followed.
  // svelte-ignore state_referenced_locally
  if (sk.progress.running) run(watch);
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
    {#if !installed}<button type="button" class="primary" disabled={running} onclick={() => run(install)}>Install</button>{/if}
    {#if installed}<button type="button" class="danger" disabled={running} onclick={() => run(remove)}>Remove</button>{/if}
  {/snippet}
</Card>
