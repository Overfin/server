<!-- Overfin's first-run setup wizard: one step per screen, each saved as
     it's done, with Back and Next. The steps come from the server: those
     with something to ask, in order. A Jellyfin someone else set up takes
     two more rounds on its step: its administrator's login, then a
     confirmation once the page shows what it holds. The server remembers
     the steps done: reloaded, the page asks for the code again, then goes
     on from the first step not done. -->
<script lang="ts">
  import { tick } from 'svelte';
  import { Progress, Status } from '@overfin/ui';

  interface Step {
    id: string;
    done: boolean;
    optional?: boolean;
  }
  interface Survey {
    users?: string[];
    libraries: number;
  }
  interface JellyfinAnswer {
    done?: boolean;
    summary?: string;
    server: { id: string; name?: string; version: string };
    survey?: Survey;
  }

  const command = `docker logs overfin | grep -o '"code":"[^"]*' | tail -1 | cut -d'"' -f4`;
  const skippedNames: Record<string, string> = { tmdb: 'TMDB', introskipper: 'Intro Skipper', seerr: 'Seerr' };

  let setup = $state<{ steps: Step[]; fixedUrl: string; site: boolean; languages?: { tag: string; name: string }[] }>({ steps: [], fixedUrl: '', site: false });
  let at = $state(0);
  let busy = $state(false);
  let skipped = $state<string[]>([]);
  let status = $state<{ text: string; kind: '' | 'error' }>({ text: '', kind: '' });
  let finished = $state(false);
  let copied = $state(false);
  let section = $state<HTMLElement>();

  // The steps' fields and what they said once saved.
  let code = $state('');
  let language = $state('');
  let url = $state('');
  let jfUsername = $state('');
  let jfPassword = $state('');
  let dedicated = $state(false);
  let tmdbToken = $state('');
  let seerrUrl = $state('');
  let seerrKey = $state('');
  let email = $state('');
  let password = $state('');
  let notes = $state<Record<string, string>>({});
  // The Jellyfin found: its administrator's login is asked, then (survey)
  // what it holds is shown for confirmation.
  let found = $state<JellyfinAnswer | null>(null);
  // shown: the server shown for confirmation, once its administrator signed in.
  let shown = $state<string | null>(null);
  let skipperVersion = $state('12.0.5.0');

  const step = $derived(setup.steps[at]);
  const confirming = $derived(step?.id === 'jellyfin' && !!found?.survey && !dedicated);
  const locked = $derived(!!step?.done && step.id !== 'done');
  const nextLabel = $derived(!step ? 'Next' : step.id === 'done' ? 'Finish'
    : step.id === 'introskipper' && !step.done ? 'Install'
    : step.id === 'jellyfin' && found?.survey ? 'Hand it over to Overfin' : 'Next');

  async function load() {
    try {
      const res = await fetch('/admin/api/setup');
      if (res.ok) setup = await res.json();
    } catch {
      // Said below: Overfin didn't answer.
    }
    if (!setup.steps.length) {
      fail("Overfin didn't answer. Is it still running?");
      return;
    }
    if (setup.languages) language = guessLanguage(setup.languages);
    show(0);
  }

  // guessLanguage picks the browser's language among the choices: the
  // same tag, else the same language, else English (United States).
  function guessLanguage(choices: { tag: string }[]): string {
    const tags = choices.map((c) => c.tag);
    for (const want of navigator.languages || [navigator.language]) {
      if (tags.includes(want)) return want;
      const same = tags.find((t) => t.split('-')[0] === want.split('-')[0]);
      if (same) return same;
    }
    return 'en-US';
  }

  // show displays step i, and focuses its first field.
  async function show(i: number) {
    at = i;
    const s = setup.steps[i];
    // Done before a reload: kept as it is, Next moves on.
    if (s.done && s.id !== 'done' && !notes[s.id]) notes[s.id] = 'Saved earlier.';
    say('');
    await tick();
    const first = section?.querySelector<HTMLElement>('input:not([readonly]):not(:disabled), select:not(:disabled)');
    first?.focus();
  }

  function say(text: string, kind: '' | 'error' = '') {
    status = { text, kind };
  }

  function fail(text: string) {
    say(text, 'error');
  }

  // post sends a step's answer with the code; it returns the answer, or
  // null once it said what's wrong.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  async function post(id: string, fields: Record<string, unknown> = {}): Promise<any> {
    let res: Response;
    try {
      res = await fetch(`/admin/api/setup/${id}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ code, ...fields }),
      });
    } catch {
      fail("Overfin didn't answer. Is it still running?");
      return null;
    }
    const body = res.status === 204 ? {} : await res.json().catch(() => ({}));
    if (res.ok) return body;
    if (res.status === 403 && id !== 'code') {
      // Overfin restarted, so its code changed: back to the first step.
      show(0);
    } else if (res.status === 409 && body.step) {
      const i = setup.steps.findIndex((s) => s.id === body.step);
      if (i >= 0) show(i);
    }
    fail(body.error || `Something went wrong (${res.status}).`);
    return null;
  }

  // The steps' Next: what each saves. It returns true to move on, 'stay'
  // when its answer is worth reading first.
  const actions: Record<string, () => Promise<boolean | 'stay'>> = {
    code: async () => (await post('code')) !== null,
    language: async () => (await post('language', { language })) !== null,
    jellyfin: async () => {
      const ans: JellyfinAnswer | null = await post('jellyfin', {
        url, username: jfUsername, password: jfPassword,
        confirm: shown && dedicated ? shown : '',
      });
      if (!ans) return false;
      if (!ans.done) {
        jellyfinRound(ans);
        return false;
      }
      found = null;
      notes.jellyfin = ans.summary || '';
      return 'stay';
    },
    tmdb: async () => {
      if (!tmdbToken.trim()) {
        fail('Paste the token, or skip this step.');
        return false;
      }
      const ans = await post('tmdb', { token: tmdbToken });
      if (!ans) return false;
      tmdbToken = '';
      notes.tmdb = ans.summary;
      return 'stay';
    },
    introskipper: async () => {
      let p = await post('introskipper', { start: true });
      while (p && p.running) {
        say(p.step || 'Installing…');
        await new Promise((r) => setTimeout(r, 2000));
        p = await post('introskipper');
      }
      if (!p) return false;
      if (p.error) {
        fail(p.error);
        return false;
      }
      notes.introskipper = `Intro Skipper ${p.status.version} is installed and active.`;
      return 'stay';
    },
    seerr: async () => {
      const ans = await post('seerr', { url: seerrUrl, key: seerrKey });
      if (!ans) return false;
      seerrKey = '';
      notes.seerr = ans.summary;
      return 'stay';
    },
    admin: async () => {
      if ((await post('admin', { email, password })) === null) return false;
      password = '';
      notes.admin = 'Your account is made.';
      return true;
    },
    done: async () => {
      if ((await post('finish')) === null) return false;
      finished = true;
      return false;
    },
  };

  // jellyfinRound shows what the server asks for next: its administrator's
  // login, then what it holds.
  function jellyfinRound(j: JellyfinAnswer) {
    found = j;
    if (j.survey) {
      shown = j.server.id;
      dedicated = false;
      say('Check what it holds, then confirm.');
    } else {
      say('Sign in as its administrator.');
    }
  }

  // Changing the address or the login starts the Jellyfin step over.
  function resetJellyfin() {
    shown = null;
    if (found) found = { ...found, survey: undefined };
  }

  async function next() {
    const s = step;
    if (busy || confirming) return;
    if (s.done && s.id !== 'done') {
      show(at + 1);
      return;
    }
    busy = true;
    say(s.id === 'done' ? 'Finishing…' : 'Checking…');
    try {
      const result = await actions[s.id]();
      if (result) {
        s.done = s.id !== 'code' && s.id !== 'done';
        skipped = skipped.filter((id) => id !== s.id);
        if (result === 'stay') {
          // Its answer is worth reading: Next moves on.
          show(at);
        } else if (s.id === 'code') {
          // On from the first step not done: after a reload, where it was.
          show(setup.steps.findIndex((st, i) => i > 0 && !st.done));
        } else {
          show(at + 1);
        }
      }
    } finally {
      busy = false;
    }
  }

  function skip() {
    if (busy) return;
    if (!skipped.includes(step.id)) skipped = [...skipped, step.id];
    show(at + 1);
  }

  async function copy(input: HTMLInputElement) {
    try {
      await navigator.clipboard.writeText(command);
    } catch {
      // Not over HTTPS, the clipboard API isn't there: copy the selection.
      input.select();
      document.execCommand('copy');
    }
    copied = true;
    setTimeout(() => (copied = false), 2000);
  }

  // Enter in a field is Next.
  function enter(e: KeyboardEvent) {
    const el = e.target as HTMLInputElement;
    if (e.key === 'Enter' && el.tagName === 'INPUT' && el.type !== 'checkbox' && !el.readOnly) next();
  }

  let commandInput = $state<HTMLInputElement>();
  load();
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<main class="single" onkeydown={enter}>
  <div class="brand"><img class="logo" src="/admin/logo.svg" alt="" /><div><strong>Overfin</strong><small>Setup</small></div></div>
  {#if setup.steps.length}
    <Progress value={Math.round((at / (setup.steps.length - 1)) * 100)} label="Setup" />
    <p class="hint">Step {at + 1} of {setup.steps.length}</p>
  {/if}

  {#if step}
    <section bind:this={section}>
      {#if step.id === 'code'}
        <div class="card-head"><div>
          <h2>Your setup code</h2>
          <p>Until it's set up, anyone who reaches this page could set Overfin up for themselves. So it asks for a code only Overfin's log shows: whoever can read that log runs this server.</p>
        </div></div>
        <p>With Docker, this command prints it:</p>
        <div class="copyable">
          <input bind:this={commandInput} value={command} readonly spellcheck="false" aria-label="Command" />
          <button type="button" onclick={() => copy(commandInput!)}>{copied ? 'Copied' : 'Copy'}</button>
        </div>
        <p class="hint">If your container isn't named <code>overfin</code>, change that word. "Permission denied": put <code>sudo</code> in front. The code changes each time Overfin restarts: use the latest.</p>
        <div class="fields">
          <label class="field full"><span>Setup code</span><input bind:value={code} autocomplete="off" spellcheck="false" placeholder="XXXX-XXXX" /></label>
        </div>
      {:else if step.id === 'language'}
        <div class="card-head"><div>
          <h2>Default language</h2>
          <p>Of the site, titles and emails, for new accounts and before anyone signs in. You can offer more languages later in the control panel, under Server: each account then picks its own.</p>
        </div></div>
        <div class="fields">
          <label class="field full"><span>Default language</span>
            <select bind:value={language} disabled={locked}>
              {#each setup.languages || [] as c (c.tag)}<option value={c.tag}>{c.name}</option>{/each}
            </select>
          </label>
        </div>
      {:else if step.id === 'jellyfin'}
        <div class="card-head"><div>
          <h2>Jellyfin</h2>
          <p>Overfin needs a Jellyfin of its own, on your media: it reads your files and streams them through it. A fresh one, Overfin sets up itself. One already set up, Overfin takes only if you hand it over.</p>
        </div></div>
        <div class="fields">
          {#if setup.fixedUrl}
            <p class="field full">At {setup.fixedUrl}, as Overfin's configuration says.</p>
          {:else}
            <label class="field full"><span>Jellyfin's address, as Overfin reaches it</span>
              <input bind:value={url} oninput={resetJellyfin} disabled={locked} autocomplete="off" spellcheck="false" placeholder="http://jellyfin:8096" />
              <span class="hint">Not localhost: in a container, that's Overfin itself.</span>
            </label>
          {/if}
        </div>
        {#if found && !locked}
          <p class="notice">{found.server.name ? `${found.server.name} (Jellyfin ${found.server.version})` : `This Jellyfin (${found.server.version})`} is already set up. To hand it over to Overfin, sign in as one of its administrators: Overfin uses this login once, to make a key of its own, and doesn't keep it.</p>
          <div class="fields">
            <label class="field"><span>Its administrator's username</span><input bind:value={jfUsername} oninput={resetJellyfin} autocomplete="off" spellcheck="false" /></label>
            <label class="field"><span>Password</span><input type="password" bind:value={jfPassword} oninput={resetJellyfin} autocomplete="off" /></label>
          </div>
          {#if found.survey}
            {@const users = found.survey.users || []}
            <div class="notice warn">
              <p>It holds <strong title={users.join(', ')}>{users.length === 1 ? '1 user' : `${users.length} users`}</strong> and <strong>{found.survey.libraries === 1 ? '1 library' : `${found.survey.libraries} libraries`}</strong>.</p>
              <p>From now on, Overfin manages it: the settings it needs are its own, and changes made in Jellyfin itself may be undone. Don't hand over a Jellyfin people use directly.</p>
              <label class="inline"><input type="checkbox" bind:checked={dedicated} /> This Jellyfin is dedicated to Overfin.</label>
            </div>
          {/if}
        {/if}
        {#if notes.jellyfin}<p class="notice">{notes.jellyfin}</p>{/if}
      {:else if step.id === 'tmdb'}
        <div class="card-head"><div>
          <h2>TMDB <span class="badge plain warn">Needed for now</span></h2>
          <p>TMDB is where Overfin gets titles' names, synopses, artwork and ratings, in your languages, and what to discover. It's free; you need a token of your own, once.</p>
        </div></div>
        <ol class="how">
          <li><a href="https://www.themoviedb.org/signup" target="_blank" rel="noopener">Create a TMDB account</a>, and confirm its email.</li>
          <li>In your account's <a href="https://www.themoviedb.org/settings/api" target="_blank" rel="noopener">Settings → API</a>, ask for API access: personal use is fine.</li>
          <li>Copy the <strong>API Read Access Token</strong> (the long one, not the API key), and paste it here.</li>
        </ol>
        <div class="fields">
          <label class="field full"><span>TMDB's token</span><input type="password" bind:value={tmdbToken} disabled={locked} autocomplete="off" spellcheck="false" /></label>
        </div>
        <p class="hint">For now, Overfin can't do without it: skipped, titles show with no name or picture, Home stays empty, and kids profiles see nothing. You can add a token later in the control panel (Server, TMDB).</p>
        {#if notes.tmdb}<p class="notice">{notes.tmdb}</p>{/if}
      {:else if step.id === 'introskipper'}
        <div class="card-head"><div>
          <h2>Intro Skipper <span class="badge plain info">Optional</span></h2>
          <p>A Jellyfin plugin that finds each episode's intro and credits, so the player offers "Skip intro" and "Skip credits", and goes to the next episode sooner.</p>
        </div></div>
        <p>Overfin adds its repository to its Jellyfin, installs version <span>{skipperVersion}</span> (the one Overfin is tested with, checked before it's installed), and restarts Jellyfin. It takes under a minute. Episodes are then analysed in the background.</p>
        <p class="hint">It's made by the Intro Skipper project, not by Overfin or Jellyfin.</p>
        {#if notes.introskipper}<p class="notice">{notes.introskipper}</p>{/if}
      {:else if step.id === 'seerr'}
        <div class="card-head"><div>
          <h2>Requests, with Seerr <span class="badge plain info">Optional</span></h2>
          <p>With Seerr, people can ask for titles you don't have; with Radarr and Sonarr behind it, those titles arrive by themselves. Overfin gives each account its own Seerr user, and sends their requests through it.</p>
        </div></div>
        <ol class="how">
          <li><a href="https://docs.seerr.dev/" target="_blank" rel="noopener">Install Seerr</a> and run its setup, with a Jellyfin that sees the same media: another one, or Overfin's own once you open its access in the control panel.</li>
          <li>In Seerr, connect Radarr and Sonarr: they fetch what's requested.</li>
          <li>In Seerr, set what new users may do: permissions, auto-approval, limits. Every account gets those.</li>
          <li>Give Overfin Seerr's address and its API key (in Seerr's settings, under General).</li>
        </ol>
        <div class="fields">
          <label class="field"><span>Seerr's address, as Overfin reaches it</span><input bind:value={seerrUrl} disabled={locked} autocomplete="off" spellcheck="false" placeholder="http://seerr:5055" /></label>
          <label class="field"><span>Its API key</span><input type="password" bind:value={seerrKey} disabled={locked} autocomplete="off" spellcheck="false" /></label>
        </div>
        <p class="hint">You can connect it later in the control panel.</p>
        {#if notes.seerr}<p class="notice">{notes.seerr}</p>{/if}
      {:else if step.id === 'admin'}
        <div class="card-head"><div>
          <h2>You, the first admin</h2>
          <p>The account you sign in with, to the site and to the control panel.</p>
        </div></div>
        <div class="fields">
          <label class="field"><span>Your email</span><input type="email" bind:value={email} disabled={locked} autocomplete="username" spellcheck="false" /><span class="hint">You sign in with it.</span></label>
          <label class="field"><span>A password</span><input type="password" bind:value={password} disabled={locked} autocomplete="new-password" /><span class="hint">At least 10 characters, with a digit and a special character.</span></label>
        </div>
        {#if notes.admin}<p class="notice">{notes.admin}</p>{/if}
      {:else if step.id === 'done'}
        <div class="card-head"><div>
          <h2>{finished ? 'Done' : 'Ready'}</h2>
          <p>{finished ? 'Overfin is set up.' : 'Everything is in place. Finish, and Overfin starts.'}</p>
        </div></div>
        {#if skipped.length}
          <ul class="hint">
            {#each skipped as id (id)}
              <li>{id === 'tmdb' ? 'TMDB: skipped. The site can\'t be used until you add a token in the control panel (Server, TMDB).' : `${skippedNames[id]}: skipped, you can set it up later in the control panel.`}</li>
            {/each}
          </ul>
        {/if}
        {#if finished}
          {#if setup.site}
            <p>Overfin is starting. In a few seconds, <a href="/">sign in</a>.</p>
          {:else}
            <p>Overfin is starting. In a few seconds, <a href="/admin/">reload this page</a> to sign in to the control panel.</p>
          {/if}
        {/if}
      {/if}
    </section>
  {/if}

  {#if !finished}
    <div class="wizard-foot">
      {#if at > 0}<button type="button" disabled={busy} onclick={() => { if (!busy && at > 0) show(at - 1); }}>Back</button>{/if}
      <Status text={status.text} kind={status.kind} />
      {#if step?.optional && !step.done}<button type="button" disabled={busy} onclick={skip}>Skip for now</button>{/if}
      <button type="button" class="primary" disabled={busy || confirming || !step} onclick={next}>{nextLabel}</button>
    </div>
  {/if}
</main>
