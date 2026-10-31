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
    reasons?: string[];
    height?: number;
    bitrateKbps?: number;
  }

  // Jellyfin's reasons for converting, in words (as the site's player says them).
  const convertReasons: Record<string, string> = {
    ContainerNotSupported: 'the browser can\'t read the container (repackaged)',
    VideoCodecNotSupported: 'the browser can\'t play the video format',
    AudioCodecNotSupported: 'the browser can\'t play the audio format',
    SubtitleCodecNotSupported: 'picture subtitles, drawn into the video',
    AudioIsExternal: 'the audio is in a separate file',
    SecondaryAudioNotSupported: 'another audio track than the first',
    VideoProfileNotSupported: 'the video\'s profile',
    VideoLevelNotSupported: 'the video\'s level',
    VideoResolutionNotSupported: 'the picture is larger than allowed',
    VideoBitDepthNotSupported: 'the video\'s bit depth',
    VideoFramerateNotSupported: 'the frame rate',
    AudioChannelsNotSupported: 'too many audio channels',
    ContainerBitrateExceedsLimit: 'the bitrate is above the limit',
    VideoBitrateNotSupported: 'the video\'s bitrate is above the limit',
    AudioBitrateNotSupported: 'the audio\'s bitrate is above the limit',
    VideoRangeTypeNotSupported: 'the screen or browser can\'t show HDR',
    DirectPlayError: 'playing the file as it is failed',
  };
  const methodNames: Record<string, [string, 'ok' | 'plain' | 'warn']> = { direct: ['As is', 'ok'], remux: ['Repackaged', 'plain'], transcode: ['Converted', 'warn'] };

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

  const why = (pb: Playback) => (pb.reasons || []).map((r) => convertReasons[r] || r).join('; ');
  const quality = (pb: Playback) => [pb.height && `${pb.height}p`, pb.bitrateKbps && `${(pb.bitrateKbps / 1000).toFixed(1)} Mb/s`].filter(Boolean).join(', ');
</script>

<PageHead title="Now playing" description="What's playing on the server now, refreshed every few seconds. Nothing of it is kept: history stays each profile's own." />

{#if problem}<Notice warn>{problem}</Notice>{/if}
{#if !data}
  {#if !problem}<Loading />{/if}
{:else}
  <Card title={plural(data.items.length, 'playback')} description={`${plural(data.transcodes, 'conversion')}${data.maxTranscodes ? ` of ${data.maxTranscodes} allowed` : ''} running.`}>
    <div class="table-wrap">
      <table>
        <thead><tr><th>Who</th><th>What</th><th>How</th><th></th></tr></thead>
        <tbody>
          {#each data.items as pb (pb.id)}
            {@const [method, kind] = methodNames[pb.method] || [pb.method, 'plain']}
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
