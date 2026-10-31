<!-- The GPU Jellyfin transcodes with: what each family decodes and
     encodes, a test on a few of your own videos, and the choice, set back
     at every start. -->
<script lang="ts">
  import { Card, Badge, Field, Status, Busy, ask, toast } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { message } from '../../lib/format';
  import type { Line, Transcoding, TranscodingTest } from '../../lib/types-server';

  let { t }: { t: Transcoding } = $props();

  // svelte-ignore state_referenced_locally
  const families = t.families;
  const vendors = [...new Set(families.map((f) => f.vendor).filter(Boolean))] as string[];
  // svelte-ignore state_referenced_locally
  let choose = $state(t.family || '');
  // svelte-ignore state_referenced_locally
  let device = $state(t.device || t.defaultDevice);
  let results = $state<TranscodingTest | null>(null);
  // svelte-ignore state_referenced_locally
  let st = $state<Line>({ text: t.family ? 'In use.' : 'Not chosen yet: Jellyfin keeps its own setting.', kind: '' });
  let tested = '';
  const chosen = $derived(families.find((f) => f.id === choose));
  // svelte-ignore state_referenced_locally
  const current = families.find((f) => f.id === t.family);
  const fail = (err: Error) => (st = { text: message(err), kind: 'error' });
  const body = () => ({ family: choose, device });
  const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

  async function test() {
    if (!choose) {
      st = { text: 'Choose a GPU first.', kind: 'error' };
      return;
    }
    st = { text: 'Testing on a few of your videos, each for up to 40 seconds. Meanwhile, new playbacks use the settings tested.', kind: '' };
    results = null;
    let r = await api<TranscodingTest>('POST', '/admin/api/transcoding/test', body());
    while (r.running) {
      results = r;
      await sleep(1500);
      r = await api<TranscodingTest>('GET', '/admin/api/transcoding/test');
    }
    results = r;
    if (r.error) {
      st = { text: r.error, kind: 'error' };
      return;
    }
    const list = r.results || [];
    const ok = list.every((x) => x.ok);
    const slow = list.some((x) => x.ok && (x.speed < 1 || x.firstPicture > 5));
    tested = ok ? choose + ' ' + device : '';
    st = { text: !ok ? 'It fails for some videos: the reasons are in the table.'
      : slow ? 'It works, but slowly for some videos: see the table.' : 'It works.', kind: ok && !slow ? 'ok' : 'error' };
  }

  async function use() {
    if (!choose) {
      st = { text: 'Choose a GPU first.', kind: 'error' };
      return;
    }
    if (tested !== choose + ' ' + device && !(await ask('Use it without a test that worked? Playbacks may fail if it doesn\'t suit your GPU.', { yes: 'Use it' }))) return;
    await api('PUT', '/admin/api/transcoding', body());
    st = { text: 'In use.', kind: 'ok' };
    toast('Jellyfin transcodes with it now.');
  }
</script>

<Card title="Transcoding" description="The GPU Jellyfin converts videos with, when a device can't play them as they are. Test it on your own videos before using it.">
  {#snippet badge()}
    {#if current}<Badge kind="ok">{current.vendor ? `${current.vendor} ${current.name}` : current.name}</Badge>{:else}<Badge kind="plain">Not chosen</Badge>{/if}
  {/snippet}
  <div class="fields">
    <Field label="GPU" wide>
      <select bind:value={choose} onchange={() => (results = null)} aria-label="GPU">
        {#if !t.family}<option value="">Choose your GPU</option>{/if}
        {#each families.filter((f) => !f.vendor) as f (f.id)}<option value={f.id}>{f.name}</option>{/each}
        {#each vendors as v (v)}
          <optgroup label={v}>
            {#each families.filter((f) => f.vendor === v) as f (f.id)}<option value={f.id}>{`${f.name} (${f.examples})`}</option>{/each}
          </optgroup>
        {/each}
      </select>
    </Field>
    {#if chosen?.device}
      <Field label="Device" hint={'The GPU\'s render device, as Jellyfin sees it; the first one is usually ' + t.defaultDevice + '.'}>
        <input class="mono" bind:value={device} autocomplete="off" spellcheck="false" />
      </Field>
    {/if}
  </div>
  {#if chosen}
    <div class="box">
      <div><strong>Decodes: </strong>{chosen.decodes?.length ? chosen.decodes.join(', ') : 'nothing: the processor does it'}</div>
      <div><strong>Encodes: </strong>{chosen.encodes?.length ? chosen.encodes.join(', ') : 'nothing: the processor does it'}</div>
      {#if chosen.note}<div class="hint">{chosen.note}</div>{/if}
    </div>
  {/if}
  {#if results}
    <div class="table-wrap">
      <table>
        <thead><tr><th>Video</th><th>Result</th><th class="num">First picture</th><th class="num">Speed</th></tr></thead>
        <tbody>
          {#each results.results || [] as x, i (i)}
            <tr>
              <td><div>{x.kind}</div><div class="hint">{x.title}</div></td>
              <!-- A failure's reason gets the whole row. -->
              {#if x.ok}
                <td>
                  {#if x.speed < 1}<Badge kind="warn">Too slow</Badge>{:else if x.firstPicture > 5}<Badge kind="warn">Slow to start</Badge>{:else}<Badge kind="ok">Works</Badge>{/if}
                </td>
                <td class="num">{x.firstPicture} s</td>
                <td class="num">{x.speed}×</td>
              {:else}
                <td colspan="3"><Badge kind="err">Fails</Badge><div class="hint">{x.reason}</div></td>
              {/if}
            </tr>
          {/each}
          {#each Array.from({ length: Math.max(0, (results.total || 0) - (results.results || []).length) }) as _, i (i)}
            <tr><td colspan="4" class="hint">{i === 0 ? 'Testing…' : 'Waiting…'}</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
  {#snippet foot()}
    <Status text={st.text} kind={st.kind} />
    <Busy action={test} {fail}>Test</Busy>
    <Busy kind="primary" action={use} {fail}>Use it</Busy>
  {/snippet}
</Card>
