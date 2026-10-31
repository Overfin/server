<!-- Features & limits: what this server offers, and how far it goes. One
     form for the whole page, saved at once from its bar, which shows only
     with unsaved changes. Changes kept in the browser (a draft) survive
     the sign-in a save may need first. -->
<script lang="ts">
  import { PageHead, Card, Setting, Busy, Notice, Loading, toast } from '@overfin/ui';
  import { api, ApiError } from '../lib/api';
  import { featureName, message, numberIn } from '../lib/format';
  import type { FeatureSettings, FeaturesState } from '../lib/types-people';

  const DRAFT = 'overfin-panel-draft';

  const privacyNames: Record<string, string> = {
    'version.names': 'Version names (from file names)', 'photo.takenAt': 'When photos were taken', 'photo.location': 'Where photos were taken',
    'photo.camera': 'Which camera took photos', 'item.tags': 'Tags',
  };
  const privacyName = (n: string) => privacyNames[n] || n;

  // A field of the form: where its value goes in the settings, and how.
  // A "list" field ticks one value in a list (plugins.enable, privacy.expose).
  type Kind = 'number' | 'choice' | 'list' | 'text' | 'onOff';
  interface Field {
    path: string;
    kind: Kind;
    value?: string;
    label: string;
    zero?: string;
  }

  let data = $state<FeaturesState | null>(null);
  let problem = $state('');
  let draft = $state<FeatureSettings | null>(null);
  // The form's values, by field key.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let values = $state<Record<string, any>>({});
  let baseline = $state('');
  let fields: Field[] = [];

  const key = (f: Field) => (f.kind === 'list' ? `${f.path}|${f.value}` : f.path);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const at = (obj: any, path: string) => path.split('.').reduce((o, k) => o?.[k], obj);

  function load(f: FeatureSettings) {
    for (const fl of fields) {
      const v = at(f, fl.path);
      values[key(fl)] = fl.kind === 'list' ? (v || []).includes(fl.value)
        : fl.kind === 'onOff' ? v === 'on'
        : fl.kind === 'text' ? v || '' : v;
    }
  }

  function collect(): FeatureSettings {
    const out = structuredClone($state.snapshot(data!.features));
    for (const fl of fields) {
      const keys = fl.path.split('.');
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const parent = keys.slice(0, -1).reduce((o: any, k) => (o[k] ??= {}), out);
      const last = keys.at(-1)!;
      const v = values[key(fl)];
      if (fl.kind === 'list') {
        const list = new Set<string>(parent[last] || []);
        if (v) list.add(fl.value!); else list.delete(fl.value!);
        parent[last] = [...list].sort();
      } else {
        parent[last] = fl.kind === 'number' ? Number(v) : fl.kind === 'onOff' ? (v ? 'on' : 'off') : fl.kind === 'text' ? String(v ?? '').trim() : v;
      }
    }
    return out;
  }

  // What's saved, as the form reads it back: unsaved changes are any
  // difference from it.
  function settle() {
    load(data!.features);
    baseline = JSON.stringify(collect());
  }

  const dirty = $derived(data !== null && baseline !== '' && JSON.stringify(collect()) !== baseline);

  async function start() {
    try {
      const s = await api<FeaturesState>('GET', '/admin/api/features');
      try { draft = JSON.parse(sessionStorage.getItem(DRAFT) || 'null'); } catch { draft = null; }
      const f: Field[] = [];
      for (const p of s.plugins) {
        f.push({ path: 'plugins.enable', kind: 'list', value: p.name, label: featureName(p.name) });
        for (const st of p.settings || []) f.push({ path: `modules.${p.name}.${st.key}`, kind: st.text ? 'text' : st.choices ? 'choice' : 'onOff', label: st.label });
      }
      f.push(
        { path: 'limits.onLimit', kind: 'choice', label: 'One stream too many' },
        { path: 'limits.maxTranscodesTotal', kind: 'number', label: 'Transcodes on the server', zero: '0: no limit' },
        { path: 'limits.maxBitrateKbps', kind: 'number', label: 'Bitrate cap (kbps)', zero: '0: no cap' },
        { path: 'accounts.onDelete', kind: 'choice', label: 'When an account is deleted' },
        { path: 'limits.pinAttempts', kind: 'number', label: 'Wrong PINs before a lock' },
      );
      for (const name of s.exposableFields) f.push({ path: 'privacy.expose', kind: 'list', value: name, label: privacyName(name) });
      fields = f;
      data = s;
      // A draft fills in over the current settings: one kept from an older
      // version may lack sections, or have some that are gone.
      const begin = structuredClone({ ...s.features,
        ...Object.fromEntries(Object.entries(draft || {}).filter(([k]) => k in s.features)) });
      settle();
      load(begin);
    } catch (err) {
      problem = message(err);
    }
  }

  function discard() {
    sessionStorage.removeItem(DRAFT);
    settle();
  }

  async function save() {
    // An empty number field is refused, not saved as 0.
    for (const fl of fields) if (fl.kind === 'number') numberIn(values[key(fl)], fl.label, fl.zero);
    const wanted = collect();
    try {
      const out = await api<{ features: FeatureSettings }>('PUT', '/admin/api/features', wanted);
      sessionStorage.removeItem(DRAFT);
      data!.features = out.features;
      settle();
      toast('Saved and applied.');
    } catch (err) {
      if (err instanceof ApiError && err.type === 'step-up-required') {
        // Changes need a recent sign-in: keep them, sign in, come back.
        sessionStorage.setItem(DRAFT, JSON.stringify(wanted));
        location.href = '/admin/login?return=/admin/%23/features';
        return;
      }
      throw err;
    }
  }

  const listKey = (path: string, value: string) => `${path}|${value}`;

  start();
</script>

<PageHead title="Features & limits" description="What this server offers, and how far it goes. Changes apply once saved." />

{#if problem}
  <Notice warn>{problem}</Notice>
{:else if !data}
  <Loading />
{:else}
  {#if draft}<div class="notice warn">Your unsaved changes are back: save them to apply them.</div>{/if}

  <Card title="Features" description="Switched on and off for everyone at once.">
    {#each data.plugins as p (p.name)}
      {@const on = values[listKey('plugins.enable', p.name)]}
      <!-- Each feature: its switch, and under it its own settings, dimmed
           while it's off. -->
      <Setting label={featureName(p.name)} hint={[p.description, p.needs && `Needs ${p.needs.map(featureName).join(', ')}.`].filter(Boolean).join(' ')}>
        {#snippet children(ids)}<input type="checkbox" bind:checked={values[listKey('plugins.enable', p.name)]} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
      {#each p.settings || [] as s (s.key)}
        {@const path = `modules.${p.name}.${s.key}`}
        <Setting label={s.label} hint={s.hint || ''} sub off={!on}>
          {#snippet children(ids)}
            {#if s.text}
              <input bind:value={values[path]} autocomplete="off" spellcheck="false" aria-labelledby={ids.label} aria-describedby={ids.hint} />
            {:else if s.choices}
              <select bind:value={values[path]} aria-labelledby={ids.label} aria-describedby={ids.hint}>
                {#each s.choices as c (c.value)}<option value={c.value}>{c.label}</option>{/each}
              </select>
            {:else}
              <input type="checkbox" bind:checked={values[path]} aria-labelledby={ids.label} aria-describedby={ids.hint} />
            {/if}
          {/snippet}
        </Setting>
      {/each}
    {/each}
  </Card>

  <div class="cards">
    <Card title="Streams">
      <Setting label="One stream too many" hint="When an account starts more streams than its template allows.">
        {#snippet children(ids)}
          <select bind:value={values['limits.onLimit']} aria-labelledby={ids.label} aria-describedby={ids.hint}>
            <option value="replace-oldest">Replace the oldest</option>
            <option value="refuse">Refuse the new one</option>
          </select>
        {/snippet}
      </Setting>
      <Setting label="Transcodes on the server" hint="Re-encoded video only, not repackaging. 0: no limit.">
        {#snippet children(ids)}<input type="number" min="0" bind:value={values['limits.maxTranscodesTotal']} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
      <Setting label="Bitrate cap (kbps)" hint="0: no cap.">
        {#snippet children(ids)}<input type="number" min="0" bind:value={values['limits.maxBitrateKbps']} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
    </Card>
    <Card title="Accounts">
      <Setting label="When an account is deleted" hint="What happens to its watch history.">
        {#snippet children(ids)}
          <select bind:value={values['accounts.onDelete']} aria-labelledby={ids.label} aria-describedby={ids.hint}>
            <option value="delete">Delete it too</option>
            <option value="disable">Keep it, unusable</option>
          </select>
        {/snippet}
      </Setting>
      <Setting label="Wrong PINs before a lock" hint="From 3 to 10.">
        {#snippet children(ids)}<input type="number" min="3" max="10" bind:value={values['limits.pinAttempts']} aria-labelledby={ids.label} aria-describedby={ids.hint} />{/snippet}
      </Setting>
    </Card>
  </div>

  <Card title="Privacy" description="Details hidden from everyone unless switched on here.">
    {#each data.exposableFields as name (name)}
      <Setting label={privacyName(name)}>
        {#snippet children(ids)}<input type="checkbox" bind:checked={values[listKey('privacy.expose', name)]} aria-labelledby={ids.label} />{/snippet}
      </Setting>
    {/each}
  </Card>

  {#if dirty}
    <div class="savebar">
      <span class="grow">You have unsaved changes.</span>
      <button type="button" class="ghost" onclick={discard}>Discard</button>
      <Busy kind="primary" action={save}>Save changes</Busy>
    </div>
  {/if}
{/if}
