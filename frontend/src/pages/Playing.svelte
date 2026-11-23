<!-- Now playing: the playbacks running now, refreshed while the page is
     open, and stopping one. Nothing of it is kept. -->
<script lang="ts">
  import { PageHead, Card, Badge, Empty, Loading, Notice, ask, toast } from '@overfin/ui';
  import { api } from '../lib/api';
  import { ago, clock, message, plural } from '../lib/format';

  interface Playback {
    id: string;
    account: { name: string };
    profile?: string;
    device?: string;
    title?: string;
    paused: boolean;
    positionMs: number;
    durationMs?: number;
    startedAt: string;
    method: string;
    height?: number;
    bitrateKbps?: number;
    // A playback of a copy: its delivery, what it converts, why the
    // player chose it (a closed code), what the copy's label is made of.
    delivery?: 'file' | 'remux' | 'transcode' | 'baseline';
    converts?: ('video' | 'audio')[];
    reason?: string;
    videoCodec?: string;
    hdr?: boolean;
  }

  // A paired server's title converts there, if at all.
  const methodNames: Record<string, [string, 'ok' | 'plain' | 'warn']> = { remote: ['On a paired server', 'plain'] };
  // A copy's delivery, by what it converts: the picture takes a
  // conversion slot, the sound alone doesn't.
  const deliveryName = (pb: Playback): [string, 'ok' | 'plain' | 'warn'] => {
    const c = pb.converts ?? [];
    if (pb.delivery === 'file') return ['Original file', 'ok'];
    if (c.includes('video')) return [c.includes('audio') ? 'Picture and sound converted' : 'Picture converted', 'warn'];
    if (c.includes('audio')) return ['Sound converted', 'plain'];
    return ['Repackaged', 'plain'];
  };
  // Why the player chose it, from the player's closed list.
  const reasonNames: Record<string, string> = {
    'as-is': 'the device plays the file as it is',
    container: 'the device plays the streams, not their file',
    'audio-track': 'another audio track than the one the device plays in the file',
    'audio-codec': 'the device can\'t play the sound\'s format',
    'video-codec': 'the device can\'t play the picture\'s format',
    'picture-range': 'the screen can\'t show the picture\'s HDR',
    bitrate: 'above the quality chosen or the bitrate limit',
    fallback: 'an earlier way failed on this device',
    quirk: 'a known quirk of the device',
    'in-doubt': 'what the file holds isn\'t known well enough (the most common format)',
  };

  let data = $state<{ items: Playback[]; transcodes: number; maxTranscodes: number } | null>(null);
  let problem = $state('');
  let timer: ReturnType<typeof setTimeout> | undefined;
  let gone = false;

  async function load() {
    clearTimeout(timer);
    try {
      data = await api('GET', '/admin/api/playing');
      problem = '';
    } catch (err) {
      problem = message(err);
    }
    // One refresh at a time, however the page was opened again.
    if (!gone) timer = setTimeout(load, document.hidden ? 30000 : 5000);
  }

  $effect(() => {
    load();
    return () => {
      gone = true;
      clearTimeout(timer);
    };
  });

  async function stop(pb: Playback) {
    if (!(await ask(`Stop ${pb.title || 'this playback'} for ${pb.account.name}? Their player says the server's administrator stopped it.`, { yes: 'Stop', danger: true }))) return;
    try {
      await api('POST', `/admin/api/playing/${pb.id}/stop`, {}).catch((err) => { if (!/ended/.test(err.message)) throw err; });
      toast('Playback stopped.');
    } catch (err) {
      toast(message(err), true);
    }
    await load();
  }

  const why = (pb: Playback) => (pb.reason ? reasonNames[pb.reason] ?? '' : '');
  const codecNames: Record<string, string> = { h264: 'H.264', hevc: 'HEVC', av1: 'AV1', vp9: 'VP9' };
  const quality = (pb: Playback) => [pb.height && `${pb.height}p`, pb.videoCodec && (codecNames[pb.videoCodec] ?? pb.videoCodec.toUpperCase()),
    pb.hdr && 'HDR', pb.bitrateKbps && `${(pb.bitrateKbps / 1000).toFixed(1)} Mb/s`].filter(Boolean).join(', ');
</script>

<PageHead title="Now playing" description="What's playing on the server now, refreshed every few seconds. Nothing of it is kept: history stays each profile's own." />

{#if problem}<Notice warn>{problem}</Notice>{/if}
{#if !data}
  {#if !problem}<Loading />{/if}
{:else}
  <Card title={plural(data.items.length, 'playback')} description={`${plural(data.transcodes, 'conversion')}${data.maxTranscodes ? ` of ${data.maxTranscodes} allowed` : ''} running.`}>
    <div class="table-wrap">
      <table>
        <thead><tr><th>Who</th><th>What</th><th>How</th><th><span class="visually-hidden">Actions</span></th></tr></thead>
        <tbody>
          {#each data.items as pb (pb.id)}
            {@const [method, kind] = pb.delivery ? deliveryName(pb) : methodNames[pb.method] || [pb.method, 'plain']}
            <tr>
              <td><div class="name">{pb.account.name}</div><div class="hint">{[pb.profile, pb.device].filter(Boolean).join(' · ')}</div></td>
              <td>
                <div>{pb.title || 'Starting…'}</div>
                <div class="hint">{pb.paused ? 'Paused at' : 'At'} {clock(pb.positionMs)}{pb.durationMs ? ` of ${clock(pb.durationMs)}` : ''} · started {ago(pb.startedAt)}</div>
              </td>
              <td>
                <Badge {kind}>{method}</Badge>
                {#if quality(pb)}<div class="hint">{quality(pb)}</div>{/if}
                {#if why(pb)}<div class="hint">{why(pb)}</div>{/if}
              </td>
              <td class="actions"><button type="button" class="small danger" onclick={() => stop(pb)}>Stop</button></td>
            </tr>
          {:else}
            <tr><td colspan="4"><Empty>Nothing is playing.</Empty></td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </Card>
{/if}
