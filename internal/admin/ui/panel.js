// Overfin control panel. Each view is a function that fetches what it shows
// and draws it into <main>. Text from the server is only ever set with
// textContent (through h()).

const $ = (sel) => document.querySelector(sel);

// h makes an element: attributes (on… are listeners), then children;
// false, null and undefined children are left out.
function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null || v === false) continue;
    if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'class') el.className = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children.flat(Infinity)) {
    if (c === undefined || c === null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

// fill replaces an element's content, leaving out what's empty.
function fill(el, ...nodes) {
  el.replaceChildren(...nodes.flat(Infinity).filter((n) => n !== undefined && n !== null && n !== false));
}

// svg makes an icon from path data, drawn with the current color.
function icon(paths) {
  const ns = 'http://www.w3.org/2000/svg';
  const s = document.createElementNS(ns, 'svg');
  s.setAttribute('viewBox', '0 0 24 24');
  s.setAttribute('fill', 'none');
  s.setAttribute('stroke', 'currentColor');
  s.setAttribute('stroke-width', '1.8');
  s.setAttribute('stroke-linecap', 'round');
  s.setAttribute('stroke-linejoin', 'round');
  for (const d of paths) {
    const p = document.createElementNS(ns, 'path');
    p.setAttribute('d', d);
    s.append(p);
  }
  return s;
}

const icons = {
  play: ['M6 4l14 8-14 8z'],
  bell: ['M6 16V11a6 6 0 0 1 12 0v5l2 2H4z', 'M10 20a2 2 0 0 0 4 0'],
  server: ['M4 5h16v6H4z', 'M4 13h16v6H4z', 'M8 8h.01', 'M8 16h.01'],
  sliders: ['M4 6h10', 'M18 6h2', 'M4 12h4', 'M12 12h8', 'M4 18h12', 'M20 18h0', 'M16 4v4', 'M10 10v4', 'M18 16v4'],
  people: ['M16 19v-1a4 4 0 0 0-4-4H7a4 4 0 0 0-4 4v1', 'M9.5 10a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7z', 'M21 19v-1a4 4 0 0 0-3-3.87', 'M16 3.13a3.5 3.5 0 0 1 0 6.75'],
  layers: ['M12 3l9 5-9 5-9-5 9-5z', 'M3 13l9 5 9-5'],
  grid: ['M4 4h7v7H4z', 'M13 4h7v7h-7z', 'M4 13h7v7H4z', 'M13 13h7v7h-7z'],
  mail: ['M4 6h16v12H4z', 'M4 7l8 6 8-6'],
  move: ['M5 12h14', 'M13 6l6 6-6 6'],
  check: ['M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14z', 'M20 20l-4-4'],
  key: ['M15 7a4 4 0 1 1-3.87 5H4v3h3v3h3v-3h1.13', 'M15.5 8.5h.01'],
  puzzle: ['M10 4h4v3a2 2 0 1 0 4 0V4h2v6h-3a2 2 0 1 0 0 4h3v6h-6v-3a2 2 0 1 0-4 0v3H4v-6h3a2 2 0 1 0 0-4H4V4h6z'],
  scale: ['M12 4v16', 'M7 20h10', 'M5 8h14', 'M5 8l-3 6a3 3 0 0 0 6 0z', 'M19 8l-3 6a3 3 0 0 0 6 0z'],
  folder: ['M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z'],
  palette: ['M12 3a9 9 0 1 0 0 18c.9 0 1.5-.7 1.5-1.5 0-.4-.2-.8-.4-1.1-.3-.3-.4-.6-.4-1.1 0-.8.7-1.5 1.5-1.5H16a5 5 0 0 0 5-5c0-4.3-4-7.8-9-7.8z',
    'M7.5 10.5h.01', 'M12 7.5h.01', 'M16.5 10.5h.01'],
};

async function api(method, path, body) {
  const init = { method, headers: {} };
  if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  const res = await fetch(path, init);
  sessionLeft(res);
  if (res.status === 204) return null;
  const data = await res.json().catch(() => ({}));
  if (res.status === 401) {
    // Back to this page once signed in.
    signInAgain();
    throw new Error('signing in');
  }
  if (!res.ok) {
    const err = new Error(data.detail || data.error || data.type || `HTTP ${res.status}`);
    err.type = data.type;
    throw err;
  }
  return data;
}

// ---- The session's end ---------------------------------------------------------
// The server ends a session after a while without a request, and after some
// hours at most; each answer says how long it has left. Someone typing or
// clicking here is still here: the session is kept alive for them, at most
// once a minute. Once it's over (they went away), the panel goes to the
// sign-in page at once, and back here after: nothing is ever changed on a
// page whose session already ended.
let sessionEnds = 0; // Date.now() it ends at; 0 until an answer says
let sessionTimer;
let lastAsked = Date.now();

function signInAgain() {
  location.href = `/admin/login?return=${encodeURIComponent(`/admin/${location.hash}`)}`;
}

function sessionLeft(res) {
  lastAsked = Date.now();
  const left = Number(res.headers.get('Overfin-Session-Left'));
  if (!res.headers.has('Overfin-Session-Left') || !Number.isFinite(left)) return;
  sessionEnds = Date.now() + left * 1000;
  clearTimeout(sessionTimer);
  // A little early: the clocks never quite agree.
  sessionTimer = setTimeout(sessionOver, Math.max(0, left * 1000 - 5000));
}

function sessionOver() {
  if (sessionEnds && Date.now() >= sessionEnds - 5000) signInAgain();
}

// A timer may wait while the computer sleeps or the tab is hidden: checked
// again when it's back.
document.addEventListener('visibilitychange', () => {
  if (!document.hidden) sessionOver();
});
for (const kind of ['keydown', 'pointerdown', 'input', 'wheel']) {
  document.addEventListener(kind, () => {
    if (!sessionEnds || Date.now() - lastAsked < 60_000) return;
    if (Date.now() >= sessionEnds - 5000) {
      signInAgain();
      return;
    }
    lastAsked = Date.now();
    fetch('/admin/api/session').then((res) => (res.status === 401 ? signInAgain() : sessionLeft(res)), () => {});
  }, { passive: true, capture: true });
}

// ---- Building blocks -----------------------------------------------------------

// toast shows a short message in the corner for a few seconds.
function toast(text, error = false) {
  const t = h('div', { class: error ? 'toast error' : 'toast', role: 'status' }, text);
  $('#toasts').append(t);
  setTimeout(() => t.remove(), error ? 7000 : 3500);
}

// busy runs an action with its button disabled, and tells any failure.
async function busy(button, action, fail = (err) => toast(err.message, true)) {
  if (button) button.disabled = true;
  try {
    return await action();
  } catch (err) {
    if (err.message !== 'signing in') fail(err);
    return undefined;
  } finally {
    if (button) button.disabled = false;
  }
}

// numberIn reads a number field. Empty, or not a number, it refuses with
// a message naming the field, so nothing is saved as 0 or "no limit" by
// mistake; zero, when given, says what 0 means there.
function numberIn(input, label, zero) {
  const v = input.value.trim();
  if (v === '' || !Number.isFinite(Number(v))) throw new Error(`${label}: enter a number${zero ? ` (${zero})` : ''}.`);
  return Number(v);
}

function pageHead(title, description, ...actions) {
  return h('div', { class: 'page-head' },
    h('div', {}, h('h1', {}, title), description && h('p', {}, description)),
    actions.length > 0 && h('div', { class: 'actions' }, actions));
}

function badge(text, kind = '') {
  return h('span', { class: `badge ${kind}` }, text);
}

// card is a section with a head (title, badge, description), a body and a
// foot for its status and actions.
function card({ title, badge: b, description }, body, foot) {
  return h('section', {},
    h('div', { class: 'card-head' }, h('div', {}, h('h2', {}, title), description && h('p', {}, description)), b),
    body, foot && h('div', { class: 'card-foot' }, foot));
}

function field(label, input, hint, wide = false) {
  return h('label', { class: wide ? 'field full' : 'field' }, h('span', {}, label), input, hint && h('span', { class: 'hint' }, hint));
}

function setting(label, hint, control, sub = false) {
  return h('div', { class: sub ? 'setting sub' : 'setting' },
    h('div', {}, h('div', { class: 'label' }, label), hint && h('div', { class: 'hint' }, hint)), control);
}

// status is a line that says where something stands, or what went wrong.
function status(text) {
  const el = h('span', { class: 'status' }, text);
  el.set = (t, kind = '') => { el.className = `status ${kind}`; el.textContent = t; };
  return el;
}

// avatar is someone's initials on a color of their own (one of eight
// classes: the panel's CSP allows no inline style).
function avatar(name, big = false) {
  const text = (name || '?').trim();
  const initials = text.includes('@') ? text[0] : text.split(/\s+/).slice(0, 2).map((w) => w[0]).join('');
  let n = 0;
  for (const c of text) n = (n * 31 + c.charCodeAt(0)) >>> 0;
  return h('span', { class: `avatar c${n % 8}${big ? ' big' : ''}`, 'aria-hidden': 'true' }, initials.toUpperCase());
}

// secret shows a value to copy once: a key, a link.
function secret(intro, value, after) {
  const input = h('input', { value, readonly: true, class: 'mono', onfocus: (e) => e.target.select() });
  const copy = h('button', { type: 'button', onclick: () => navigator.clipboard?.writeText(value).then(() => toast('Copied.'), () => input.select()) }, 'Copy');
  setTimeout(() => input.select(), 0);
  return h('div', { class: 'notice secret' }, h('div', {}, intro), h('div', { class: 'row' }, input, copy), after && h('div', { class: 'hint' }, after));
}

// ---- Windows ---------------------------------------------------------------------
// Nothing is edited in place: a list shows the things, each with its Edit
// (and Delete) button, and a form opens in a window over the page; a new
// one too. Confirmations are windows of their own, never the browser's.

// modal opens a window: its title and description, then body (a card's
// content), then its actions. Escape, the close button or a click outside
// it close it (unless it's busy: a save under way). It returns close().
let modalCount = 0;
function modal({ title, description, wide = false }, body, ...actions) {
  const id = `modal-${++modalCount}`;
  const close = () => dialog.open && dialog.close();
  const dialog = h('dialog', { class: wide ? 'modal wide' : 'modal', 'aria-labelledby': `${id}-title` },
    h('div', { class: 'card-head' },
      h('div', {}, h('h2', { id: `${id}-title` }, title), description && h('p', {}, description)),
      h('button', { type: 'button', class: 'close', 'aria-label': 'Close', title: 'Close', onclick: close }, '×')),
    h('div', { class: 'modal-body' }, body),
    actions.length > 0 && h('div', { class: 'card-foot' }, actions));
  dialog.addEventListener('click', (e) => { if (e.target === dialog) close(); });
  // Busy: its own buttons (Save…) wait for the server.
  dialog.addEventListener('cancel', (e) => { if (dialog.querySelector(':scope > .card-foot button:disabled')) e.preventDefault(); });
  dialog.addEventListener('close', () => dialog.remove());
  document.body.append(dialog);
  dialog.showModal();
  dialog.querySelector('.modal-body input:not([type=checkbox]):not([readonly]), .modal-body select, .modal-body textarea')?.focus();
  return close;
}

// ask asks before something is done, in a window: true once confirmed.
// danger: it deletes or stops something.
function ask(message, { yes = 'Confirm', danger = false, title = 'Are you sure?' } = {}) {
  return new Promise((resolve) => {
    let answer = false;
    const ok = h('button', { type: 'button', class: danger ? 'danger' : 'primary', onclick: () => { answer = true; close(); } }, yes);
    const no = h('button', { type: 'button', onclick: () => close() }, 'Cancel');
    const close = modal({ title }, h('p', { class: 'ask' }, message), h('span', { class: 'grow' }), no, ok);
    const dialog = ok.closest('dialog');
    dialog.setAttribute('role', 'alertdialog');
    dialog.classList.add('small');
    dialog.addEventListener('close', () => resolve(answer));
    (danger ? no : ok).focus();
  });
}

// formModal opens a form in a window: fields, then a status line and its
// buttons; save runs when it's sent, with the button busy, and closes the
// window once it's done (a failure is said in the window, which stays).
function formModal({ title, description, wide, saveLabel = 'Save' }, fields, save, ...more) {
  const st = status('');
  const send = h('button', { class: 'primary', type: 'submit' }, saveLabel);
  const cancel = h('button', { type: 'button', onclick: () => close() }, 'Cancel');
  const form = h('form', { class: 'modal-form', onsubmit: (e) => {
    e.preventDefault();
    busy(send, async () => { await save(st); close(); }, (err) => st.set(err.message, 'error'));
  } }, fields);
  const close = modal({ title, description, wide }, form, st, more, cancel, send);
  send.setAttribute('form', form.id = `${form.closest('dialog').getAttribute('aria-labelledby')}-form`);
  return close;
}

const when = (iso) => (iso ? new Date(iso).toLocaleString() : '');
const day = (iso) => (iso ? new Date(iso).toLocaleDateString() : '');

// ago says how long ago, roughly: "5 min ago", "3 h ago", "2 d ago".
function ago(iso) {
  if (!iso) return '';
  const min = Math.round((Date.now() - new Date(iso)) / 60000);
  if (min < 1) return 'just now';
  if (min < 60) return `${min} min ago`;
  if (min < 48 * 60) return `${Math.round(min / 60)} h ago`;
  return `${Math.round(min / 1440)} d ago`;
}

// toLocalInput turns an RFC 3339 time into a datetime-local value.
function toLocalInput(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  const pad = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

const plural = (n, one, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

// ---- Libraries -----------------------------------------------------------------

const kindNames = { films: 'Films', series: 'Series' };

// librariesView: the libraries of Overfin's own Jellyfin, with their
// folders as Jellyfin sees them, and a scan on demand.
async function librariesView() {
  const { items } = await api('GET', '/admin/api/libraries');
  const reload = () => librariesView();
  const add = h('button', { class: 'primary', type: 'button', onclick: () => libraryForm(null, reload) }, 'Add a library');
  const scan = h('button', { type: 'button', onclick: () => busy(scan, async () => {
    await api('POST', '/admin/api/libraries/scan', {});
    toast('Jellyfin is reading every folder again.');
    setTimeout(reload, 1500);
  }) }, 'Scan now');
  show(pageHead('Libraries', 'Where your films and series are, folder by folder, as Jellyfin sees them. New files show up by themselves; a scan reads everything again.', scan, add),
    items.length
      ? h('div', { class: 'cards' }, items.map((lib) => libraryCard(lib, reload)))
      : h('p', { class: 'notice' }, 'No library yet. Add one with the folders where your films or series are.'));
  // While Jellyfin scans, its progress, unless a form is open.
  if (items.some((l) => l.scanning)) {
    setTimeout(() => { if (location.hash === '#/libraries' && !$('#main form') && !$('dialog[open]')) reload(); }, 3000);
  }
}

function libraryCard(lib, reload) {
  const edit = h('button', { type: 'button', class: 'small', onclick: () => libraryForm(lib, reload) }, 'Edit');
  const remove = h('button', { type: 'button', class: 'small danger', onclick: async () => {
    if (!(await ask(`Remove the library ${lib.name}? Its files stay where they are; Overfin and its apps stop showing them.`, { yes: 'Remove', danger: true }))) return;
    busy(remove, async () => {
      await api('DELETE', `/admin/api/libraries/${encodeURIComponent(lib.name)}`);
      toast(`${lib.name} removed.`);
      reload();
    });
  } }, 'Remove');
  const body = [h('ul', { class: 'list' }, lib.folders.map((f) => h('li', {}, icon(icons.folder), h('div', { class: 'mono' }, f)))),
    h('div', { class: 'actions-row' }, edit, remove)];
  const b = lib.scanning ? badge(`Scanning ${Math.round(lib.progress)}%`, 'info') : badge(kindNames[lib.kind] || lib.kind, 'plain');
  return card({ title: lib.name, badge: b }, body);
}

// libraryForm adds a library (lib null) or changes one, in a window: its
// name, its kind when it's new, and its folders, picked as Jellyfin sees
// them.
function libraryForm(lib, done) {
  const name = h('input', { value: lib?.name || '', autocomplete: 'off', placeholder: 'Films', required: true });
  const kind = h('select', {}, h('option', { value: 'films' }, 'Films'), h('option', { value: 'series' }, 'Series'));
  const folders = [...(lib?.folders || [])];
  const list = h('ul', { class: 'list' });
  const picker = h('div', {});
  const draw = () => fill(list, folders.length ? folders.map((f, i) => h('li', {}, icon(icons.folder), h('div', { class: 'mono' }, f),
    h('button', { type: 'button', class: 'small ghost', onclick: () => { folders.splice(i, 1); draw(); } }, 'Remove')))
    : h('li', {}, h('div', { class: 'hint' }, 'No folder yet.')));
  const addFolder = h('button', { type: 'button', class: 'small', onclick: () => fill(picker, folderPicker((f) => {
    if (!folders.includes(f)) folders.push(f);
    draw();
    fill(picker);
  }, () => fill(picker))) }, 'Add a folder');
  draw();
  formModal({ title: lib ? `Edit ${lib.name}` : 'Add a library', wide: true, saveLabel: lib ? 'Save' : 'Add',
    description: 'Films and series go in libraries of their own. Folders are as Jellyfin sees them: in a container, its paths, not the host\'s.' },
  [h('div', { class: 'fields' }, field('Name', name), !lib && field('Holds', kind)),
    h('h3', {}, 'Folders'), list, h('div', { class: 'actions-row' }, addFolder), picker],
  async () => {
    if (lib) {
      await api('PATCH', `/admin/api/libraries/${encodeURIComponent(lib.name)}`, { name: name.value, folders });
      toast(`${name.value} saved.`);
    } else {
      await api('POST', '/admin/api/libraries', { name: name.value, kind: kind.value, folders });
      toast(`${name.value} added; Jellyfin is reading it.`);
    }
    done();
  });
}

// folderPicker browses folders as Jellyfin sees them, and picks one.
function folderPicker(pick, close) {
  const box = h('div', { class: 'box' });
  const typed = h('input', { class: 'mono', placeholder: '/media/films', autocomplete: 'off', spellcheck: 'false' });
  const open = async (path) => {
    try {
      const d = await api('GET', `/admin/api/folders?path=${encodeURIComponent(path)}`);
      typed.value = d.path;
      fill(box,
        h('div', { class: 'row' }, typed,
          h('button', { type: 'button', onclick: () => open(typed.value) }, 'Go'),
          h('button', { type: 'button', class: 'primary', disabled: !d.path, onclick: () => pick(d.path) }, 'Choose this folder'),
          h('button', { type: 'button', class: 'ghost', onclick: close }, 'Cancel')),
        h('ul', { class: 'list' },
          d.parent !== '' && h('li', {}, h('a', { href: '#', onclick: (e) => { e.preventDefault(); open(d.parent); } }, '↑ Up')),
          // At the top, Jellyfin's drives and mounts ("/", "/media"): whole paths.
          d.folders.map((f) => h('li', {}, icon(icons.folder),
            h('a', { href: '#', class: 'mono', onclick: (e) => { e.preventDefault(); open(f); } }, d.path ? f.split('/').pop() : f))),
          !d.folders.length && h('li', {}, h('div', { class: 'hint' }, 'No folders in here.'))));
    } catch (err) {
      toast(err.message, true);
    }
  };
  typed.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); open(typed.value); } });
  open('');
  return box;
}

// ---- Branding ------------------------------------------------------------------

// The files branding may have: what each is for.
const brandingKinds = [
  ['logo', 'Logo', 'Square: the icon in browser tabs. Without one, Overfin\'s.'],
  ['banner-dark', 'Banner', 'Your logo and your name as one picture, with a transparent background, for dark pages: about 4:1, at least 64 pixels tall. It\'s shown as tall as the header\'s logo, never wider than a few centimeters. Without one, your name shows as text.'],
  ['banner-light', 'Banner for light pages', 'Optional: for the sign-in page on light screens, when the banner doesn\'t suit them (the site\'s header is dark on every page). Without one, the banner shows there too.'],
];

// brandingView: the operator's own name, logo and banners, instead of
// Overfin's, wherever people look. The control panel stays Overfin's.
async function brandingView() {
  let b = await api('GET', '/admin/api/branding');
  let home = await api('GET', '/admin/api/home');
  const draw = () => {
    const st = status('');
    const on = h('input', { type: 'checkbox', 'aria-label': 'Use your own branding' });
    on.checked = b.on;
    const name = h('input', { autocomplete: 'off', spellcheck: 'false', maxlength: 40, value: b.name, 'aria-label': 'Name' });
    const save = h('button', { class: 'primary', type: 'button', onclick: () => busy(save, async () => {
      if (on.checked === b.on && name.value.trim() === b.name) {
        toast('Nothing to save.');
        return;
      }
      const wasOn = b.on;
      b = await api('PUT', '/admin/api/branding', { on: on.checked, name: name.value });
      // Say it was switched only when it was.
      toast(b.on === wasOn ? 'Saved.' : b.on ? 'Your branding is on.' : 'Overfin\'s branding is back.');
      draw();
    }, (err) => st.set(err.message, 'error')) }, 'Save');
    const main = card({ title: 'Your branding', badge: b.on ? badge('On', 'ok') : badge('Overfin\'s', 'plain'),
      description: 'On, your site shows your name, logo and banner instead of Overfin\'s: the site, its sign-in page, its emails. It then says "Powered by Overfin", discreetly. The control panel stays Overfin\'s.' },
    [setting('Use your own branding', 'Off, everything shows Overfin\'s, and your files stay here unused.', on),
      setting('Name', 'Your site\'s name: in browser tabs, and as the sender of its emails.', name)],
    [st, save]);

    const fileRows = brandingKinds.map(([kind, label, hint]) => {
      const rst = status('');
      const url = b.files[kind];
      const picker = h('input', { type: 'file', accept: 'image/png,image/jpeg,image/webp,image/gif', hidden: true });
      const upload = h('button', { type: 'button', onclick: () => picker.click() }, url ? 'Replace…' : 'Upload…');
      picker.addEventListener('change', () => busy(upload, async () => {
        const file = picker.files[0];
        if (!file) return;
        const type = file.type || 'image/png';
        const res = await fetch(`/admin/api/branding/files/${kind}`, { method: 'PUT', headers: { 'Content-Type': type }, body: file });
        const out = await res.json().catch(() => ({}));
        if (!res.ok) throw new Error(out.detail || (res.status === 415 ? 'That isn\'t an image.' : `HTTP ${res.status}`));
        b = out;
        toast(`${label} saved.`);
        draw();
      }, (err) => rst.set(err.message, 'error')));
      const remove = url && h('button', { type: 'button', class: 'danger', onclick: () => busy(remove, async () => {
        if (!(await ask(`Remove this file (${label})? It's deleted from the server.`, { yes: 'Remove', danger: true }))) return;
        b = await api('DELETE', `/admin/api/branding/files/${kind}`);
        draw();
      }, (err) => rst.set(err.message, 'error')) }, 'Remove');
      const preview = url
        ? h('div', { class: `brand-preview ${kind === 'banner-light' ? 'light' : 'dark'}${kind === 'logo' ? ' square' : ''}` }, h('img', { src: url, alt: label }))
        : h('div', { class: 'hint' }, 'None.');
      return h('div', { class: 'brand-file' },
        h('div', {}, h('div', { class: 'label' }, label), h('div', { class: 'hint' }, hint), rst),
        preview, h('div', { class: 'row' }, picker, upload, remove));
    });
    const files = card({ title: 'Files', description: 'Kept as you upload them: PNG works everywhere, emails included (JPEG too; not SVG, which emails can\'t show). 4 MB at most each.' }, fileRows);

    // The site's header (dark on every page), and the sign-in page on a
    // light screen, as people will see them.
    const banner = (light) => {
      const src = light ? (b.files['banner-light'] || b.files['banner-dark']) : b.files['banner-dark'];
      return src ? h('img', { src, alt: b.name }) : h('strong', {}, b.name || 'Your name');
    };
    const preview = card({ title: 'Preview', description: 'The top of your site (dark on every page), then the sign-in page on a light screen.' },
      [h('div', { class: 'brand-preview header dark' }, banner(false)), h('div', { class: 'brand-preview header light' }, banner(true))]);

    // The home page: whether titles no library has show there too.
    const hst = status('');
    const missing = h('input', { type: 'checkbox', 'aria-label': 'Titles no library has' });
    missing.checked = home.missingTitles;
    const saveHome = h('button', { class: 'primary', type: 'button', onclick: () => busy(saveHome, async () => {
      home = await api('PUT', '/admin/api/home', { missingTitles: missing.checked });
      toast('Home page saved.');
    }, (err) => hst.set(err.message, 'error')) }, 'Save');
    const homeCard = card({ title: 'Home page', description: 'What your site\'s home page shows besides the libraries.' },
      [setting('Titles no library has', 'Also show them among what\'s trending and in the slideshow, to be requested (which ones: Server, TMDB, Titles shown). Off: the home page shows the libraries only.', missing)],
      [hst, saveHome]);

    show(pageHead('Branding', 'Your site\'s name, logo and banner, instead of Overfin\'s, and its home page.'),
      h('div', { class: 'cards' }, main, homeCard, files, preview));
  };
  draw();
}

// ---- Server --------------------------------------------------------------------

// serverView: what Overfin connects to, each part tested before it's saved.
async function serverView() {
  const [jellyfin, transcoding, previews, mail, catalog, seerr, ratings, languages, skipper, templates] = await Promise.all([
    api('GET', '/admin/api/jellyfin').catch(() => null),
    api('GET', '/admin/api/transcoding').catch(() => null),
    api('GET', '/admin/api/previews').catch(() => null),
    api('GET', '/admin/api/mail').catch(() => null),
    api('GET', '/admin/api/catalog').catch(() => null),
    api('GET', '/admin/api/seerr').catch(() => null),
    api('GET', '/admin/api/ratings').catch(() => null),
    api('GET', '/admin/api/languages').catch(() => null),
    api('GET', '/admin/api/intro-skipper').catch(() => null),
    api('GET', '/admin/api/mail/templates').catch(() => null),
  ]);
  show(pageHead('Server', 'What Overfin connects to. Every change is tested before it\'s saved, and applies at once.'),
    h('div', { class: 'cards' }, jellyfinCard(jellyfin), transcodingCard(transcoding), previewsCard(previews), catalogCard(catalog), mailCard(mail), emailsCard(templates, mail), seerrCard(seerr), languagesCard(languages), tmdbCard(ratings), introSkipperCard(skipper)));
}

// introSkipperCard: Intro Skipper on Overfin's Jellyfin, installed from
// here; an install shows how it goes.
function introSkipperCard(sk) {
  if (!sk) return null;
  const st = status('');
  const installed = sk.status.installed;
  const describe = (s) => (s.installed ? `Version ${s.version}: ${s.state === 'Active' ? 'active' : s.state}.` : 'Not installed.');
  st.set(describe(sk.status), installed && sk.status.state !== 'Active' ? 'error' : '');
  const watch = async () => {
    for (;;) {
      const now = await api('GET', '/admin/api/intro-skipper');
      if (!now.progress.running) {
        if (now.progress.error) throw new Error(now.progress.error);
        toast(now.status.installed ? 'Intro Skipper is installed.' : 'Intro Skipper is removed.');
        serverView();
        return;
      }
      st.set(now.progress.step || 'Installing…');
      await new Promise((r) => setTimeout(r, 2000));
    }
  };
  const install = !installed && h('button', { class: 'primary', type: 'button', onclick: async () => busy(install, async () => {
    if (!(await ask(`Overfin adds Intro Skipper's repository to its Jellyfin, installs version ${sk.version}, and restarts Jellyfin: playback stops for a few seconds. Install it?`, { yes: 'Install' }))) return;
    await api('POST', '/admin/api/intro-skipper', {});
    await watch();
  }, (err) => st.set(err.message, 'error')) }, 'Install');
  const remove = installed && h('button', { class: 'danger', type: 'button', onclick: async () => busy(remove, async () => {
    if (!(await ask('Overfin uninstalls Intro Skipper, removes its repository from its Jellyfin, and restarts Jellyfin: playback stops for a few seconds, and the player no longer offers to skip intros and credits. Remove it?', { yes: 'Remove', danger: true }))) return;
    await api('DELETE', '/admin/api/intro-skipper');
    await watch();
  }, (err) => st.set(err.message, 'error')) }, 'Remove');
  if (sk.progress.running) busy(install || remove, watch, (err) => st.set(err.message, 'error'));
  return card({ title: 'Intro Skipper', badge: installed ? badge(sk.status.state === 'Active' ? 'Installed' : sk.status.state, sk.status.state === 'Active' ? 'ok' : 'warn') : badge('Not installed', 'plain'),
    description: 'A Jellyfin plugin, by the Intro Skipper project, that finds intros and credits: the player then offers to skip them.' },
  [], [st, install, remove]);
}

function inFileCard(title, text) {
  return card({ title, badge: badge('Configuration file', 'info') },
    h('div', { class: 'box' }, h('div', {}, text), h('div', { class: 'hint' }, "Set in Overfin's configuration (its file, or OVERFIN_JELLYFIN_URL): change it there.")));
}

function jellyfinCard(j) {
  if (!j) return null;
  const description = 'Overfin\'s own Jellyfin, where your media is read. Overfin is the only one that talks to it, with a key it made itself.';
  if (j.inFile) {
    return card({ title: 'Jellyfin', badge: badge('Connected', 'ok'), description },
      [h('div', { class: 'box' }, h('div', {}, j.url), h('div', { class: 'hint' }, "Set in Overfin's configuration (its file, or OVERFIN_JELLYFIN_URL): change it there.")),
        jellyfinAccess(j.access)]);
  }
  const url = h('input', { value: j.url, autocomplete: 'off', spellcheck: 'false' });
  const st = status('Connected.');
  const save = h('button', { class: 'primary', type: 'button', onclick: () => busy(save, async () => {
    st.set('Testing…');
    const out = await api('PUT', '/admin/api/jellyfin', { url: url.value });
    url.value = out.url;
    st.set(out.summary, 'ok');
    toast('Jellyfin saved.');
  }, (err) => st.set(err.message, 'error')) }, 'Test and save');
  return card({ title: 'Jellyfin', badge: badge('Connected', 'ok'), description },
    [h('div', { class: 'fields' },
      field('Address', url, 'As Overfin reaches it; a new address for the same server. Not localhost: in a container, that\'s Overfin itself.', true)),
    jellyfinAccess(j.access)],
    [st, save]);
}

// jellyfinAccess opens or closes access to Jellyfin itself: closed, nobody
// can sign in there; opened, Overfin's administrator account gets a new
// password, shown here once.
function jellyfinAccess(a) {
  const box = h('div', {});
  const draw = (open, since) => {
    const button = h('button', { type: 'button', class: open ? 'small' : 'small danger', onclick: async () => {
      if (!open && !(await ask('Open access to Jellyfin itself? Changes made there may break Overfin, and Overfin sets the settings it needs back when it starts.', { yes: 'Open access', danger: true }))) return;
      busy(button, async () => {
        const out = await api('PUT', '/admin/api/jellyfin/access', { open: !open });
        if (open) {
          toast('Access to Jellyfin closed.');
          draw(false);
          return;
        }
        draw(true, new Date().toISOString());
        box.append(secret(`Sign in to Jellyfin as ${out.username} with this password:`, out.password,
          'Shown once, never kept. Close access when you\'re done: that ends every sign-in there.'));
      });
    } }, open ? 'Close access' : 'Open access');
    fill(box, h('h3', {}, 'Access to Jellyfin itself'),
      open
        ? h('p', { class: 'notice warn' }, `Open since ${when(since)}. Changes made in Jellyfin may break Overfin, and the settings it needs are set back when it starts.`)
        : h('p', { class: 'hint' }, 'Closed: nobody can sign in to Jellyfin itself, and Overfin doesn\'t need anyone to. Open it only to look inside, for a while.'),
      h('div', { class: 'actions-row' }, button));
  };
  draw(!!a?.open, a?.since);
  return box;
}

// transcodingCard chooses the GPU Jellyfin transcodes with: what each
// family decodes and encodes, a test on a few of your own videos, and the
// choice, set back at every start.
function transcodingCard(t) {
  if (!t) return null;
  const families = t.families;
  const vendors = [...new Set(families.map((f) => f.vendor).filter(Boolean))];
  const choose = h('select', { 'aria-label': 'GPU' },
    !t.family && h('option', { value: '' }, 'Choose your GPU'),
    families.filter((f) => !f.vendor).map((f) => h('option', { value: f.id }, f.name)),
    vendors.map((v) => h('optgroup', { label: v }, families.filter((f) => f.vendor === v).map((f) => h('option', { value: f.id }, `${f.name} (${f.examples})`)))));
  choose.value = t.family || '';
  const device = h('input', { class: 'mono', value: t.device || t.defaultDevice, autocomplete: 'off', spellcheck: 'false' });
  const deviceField = field('Device', device, 'The GPU\'s render device, as Jellyfin sees it; the first one is usually ' + t.defaultDevice + '.');
  const about = h('div', {});
  const results = h('div', {});
  const st = status(t.family ? 'In use.' : 'Not chosen yet: Jellyfin keeps its own setting.');
  let tested = '';
  const chosen = () => families.find((f) => f.id === choose.value);
  const draw = () => {
    const f = chosen();
    deviceField.hidden = !f?.device;
    fill(results);
    fill(about, f && h('div', { class: 'box' },
      h('div', {}, h('strong', {}, 'Decodes: '), f.decodes?.length ? f.decodes.join(', ') : 'nothing: the processor does it'),
      h('div', {}, h('strong', {}, 'Encodes: '), f.encodes?.length ? f.encodes.join(', ') : 'nothing: the processor does it'),
      f.note && h('div', { class: 'hint' }, f.note)));
  };
  choose.addEventListener('change', draw);
  const body = () => ({ family: choose.value, device: device.value });
  // drawResults shows the videos tested so far, and those still to come.
  const drawResults = (t) => fill(results, h('div', { class: 'table-wrap' }, h('table', {},
    h('thead', {}, h('tr', {}, h('th', {}, 'Video'), h('th', {}, 'Result'), h('th', { class: 'num' }, 'First picture'), h('th', { class: 'num' }, 'Speed'))),
    h('tbody', {},
      (t.results || []).map((x) => h('tr', {},
        h('td', {}, h('div', {}, x.kind), h('div', { class: 'hint' }, x.title)),
        // A failure's reason gets the whole row.
        x.ok
          ? [h('td', {}, x.speed < 1 ? badge('Too slow', 'warn') : x.firstPicture > 5 ? badge('Slow to start', 'warn') : badge('Works', 'ok')),
            h('td', { class: 'num' }, `${x.firstPicture} s`), h('td', { class: 'num' }, `${x.speed}×`)]
          : h('td', { colspan: '3' }, badge('Fails', 'err'), h('div', { class: 'hint' }, x.reason)))),
      Array.from({ length: Math.max(0, (t.total || 0) - (t.results || []).length) }, (_, i) => h('tr', {},
        h('td', { colspan: '4', class: 'hint' }, i === 0 ? 'Testing…' : 'Waiting…')))))));
  const test = h('button', { type: 'button', onclick: () => {
    if (!choose.value) return st.set('Choose a GPU first.', 'error');
    busy(test, async () => {
      st.set('Testing on a few of your videos, each for up to 40 seconds. Meanwhile, new playbacks use the settings tested.');
      fill(results);
      let t = await api('POST', '/admin/api/transcoding/test', body());
      while (t.running) {
        drawResults(t);
        await new Promise((r) => setTimeout(r, 1500));
        t = await api('GET', '/admin/api/transcoding/test');
      }
      drawResults(t);
      if (t.error) return st.set(t.error, 'error');
      const r = t.results;
      const ok = r.every((x) => x.ok);
      const slow = r.some((x) => x.ok && (x.speed < 1 || x.firstPicture > 5));
      tested = ok ? choose.value + ' ' + device.value : '';
      st.set(!ok ? 'It fails for some videos: the reasons are in the table.'
        : slow ? 'It works, but slowly for some videos: see the table.' : 'It works.', ok && !slow ? 'ok' : 'error');
    }, (err) => st.set(err.message, 'error'));
  } }, 'Test');
  const use = h('button', { class: 'primary', type: 'button', onclick: async () => {
    if (!choose.value) return st.set('Choose a GPU first.', 'error');
    if (tested !== choose.value + ' ' + device.value && !(await ask('Use it without a test that worked? Playbacks may fail if it doesn\'t suit your GPU.', { yes: 'Use it' }))) return;
    busy(use, async () => {
      await api('PUT', '/admin/api/transcoding', body());
      st.set('In use.', 'ok');
      toast('Jellyfin transcodes with it now.');
    }, (err) => st.set(err.message, 'error'));
  } }, 'Use it');
  draw();
  const current = families.find((f) => f.id === t.family);
  return card({ title: 'Transcoding', badge: current ? badge(current.vendor ? `${current.vendor} ${current.name}` : current.name, 'ok') : badge('Not chosen', 'plain'),
    description: 'The GPU Jellyfin converts videos with, when a device can\'t play them as they are. Test it on your own videos before using it.' },
  [h('div', { class: 'fields' }, field('GPU', choose, '', true), deviceField), about, results], [st, test, use]);
}

// previewsCard: whether Jellyfin makes seek-bar previews, off by default.
function previewsCard(pv) {
  if (!pv) return null;
  const st = status('');
  const on = h('input', { type: 'checkbox', 'aria-label': 'Make previews' });
  on.checked = pv.on;
  const save = h('button', { class: 'primary', type: 'button', onclick: () => busy(save, async () => {
    if (on.checked === pv.on) return;
    Object.assign(pv, await api('PUT', '/admin/api/previews', { on: on.checked }));
    toast(pv.on ? 'Jellyfin makes previews now.' : 'Jellyfin makes no more previews.');
    serverView();
  }, (err) => st.set(err.message, 'error')) }, 'Save');
  return card({ title: 'Seek-bar previews', badge: pv.on ? badge('On', 'ok') : badge('Off', 'plain'),
    description: 'Small pictures shown above the seek bar while seeking. Jellyfin makes them from each video: it takes time, the GPU, and disk space (a few MB per video).' },
  [setting('Make previews', 'On: Jellyfin makes them for every video, new ones as they arrive; the first time, with a large library, that takes days. Off: no more are made.', on)],
  [st, save]);
}

function catalogCard(c) {
  if (!c) return null;
  const t = c.titles || {};
  const n = (v) => (v || 0).toLocaleString();
  const first = !c.fullAt || c.fullAt.startsWith('0001');
  const u = c.unmatched || {};
  const parts = [
    [u.movie, 'films', 'without a TMDB ID'], [u.series, 'series', 'without a TMDB ID'],
    [u.season, 'seasons', 'without their number'], [u.episode, 'episodes', 'without their season or episode number'],
  ].filter(([k]) => k > 0).map(([k, label, why]) => `${k} ${label} ${why}`);
  return card({
    title: 'Library index',
    badge: c.error ? badge('Last read failed', 'err') : first ? badge('First read', 'warn') : badge('Up to date', 'ok'),
    description: first ? 'The library is being read for the first time.' : `Read in full ${ago(c.fullAt)}; changes read ${ago(c.changesAt)}.`,
  },
  [
    h('div', { class: 'stats' }, [['movie', 'Films'], ['series', 'Series'], ['season', 'Seasons'], ['episode', 'Episodes']]
      .map(([k, label]) => h('div', { class: 'stat' }, h('b', {}, n(t[k])), h('span', {}, label)))),
    parts.length > 0 && h('p', { class: 'hint' }, `Can't be matched yet: ${parts.join('; ')}. Refreshing their metadata in Jellyfin usually fixes it; Identification lists them.`),
    c.error && h('p', { class: 'error' }, c.error),
  ]);
}

function mailCard(m) {
  if (!m) return null;
  if (m.inFile) return inFileCard('Email', `${m.host}:${m.port}, from ${m.from}`);
  const host = h('input', { value: m.host || '', autocomplete: 'off', spellcheck: 'false', placeholder: 'smtp.example.org' });
  const tls = h('select', {}, [['starttls', 'STARTTLS'], ['tls', 'TLS'], ['none', 'None']]
    .map(([v, t]) => h('option', { value: v, selected: v === (m.tls || 'starttls') }, t)));
  const port = h('input', { type: 'number', min: '1', max: '65535', value: m.port || '', placeholder: 'The usual one' });
  const from = h('input', { value: m.from || '', autocomplete: 'off', placeholder: 'Media <media@example.org>' });
  const username = h('input', { value: m.username || '', autocomplete: 'off', spellcheck: 'false' });
  const password = h('input', { type: 'password', autocomplete: 'new-password', placeholder: m.hasPassword ? 'Unchanged' : '' });
  const on = Boolean(m.host);
  const st = status(on ? 'Invitations and forgotten passwords work by email.' : 'Without email, you add people yourself and set their passwords.');
  const send = (body, button) => busy(button, async () => {
    if (!body.host.trim() && !on) {
      toast('Nothing to save.');
      return;
    }
    st.set(body.host ? 'Sending you a test email…' : 'Turning email off…');
    const out = await api('PUT', '/admin/api/mail', body);
    st.set(out.summary, 'ok');
    toast(body.host ? 'Email saved.' : 'Email turned off.');
    route();
  }, (err) => st.set(err.message, 'error'));
  const save = h('button', { class: 'primary', type: 'button', onclick: () => send({ host: host.value, port: Number(port.value) || 0, tls: tls.value,
    from: from.value, username: username.value, password: password.value }, save) }, 'Send a test and save');
  const off = on && h('button', { type: 'button', onclick: async () => {
    if ((await ask('Turn email off? Invitations and forgotten-password emails stop; you will set passwords yourself.', { yes: 'Turn off', danger: true }))) {
      send({ host: '', port: 0, tls: '', from: '', username: '', password: '' }, off);
    }
  } }, 'Turn off');
  return card({ title: 'Email', badge: on ? badge('On', 'ok') : badge('Off'),
    description: 'For invitations, address confirmations and forgotten passwords. Your email provider gives these settings; a test email goes to you before they\'re saved.' },
  h('div', { class: 'fields' },
    field('SMTP server', host, null, true),
    field('Encryption', tls, 'STARTTLS goes with port 587, TLS with 465. None: a relay on your network.'),
    field('Port', port),
    field('Sender', from, 'The address emails come from.', true),
    field('Username', username, 'Often the sender\'s address.'),
    field('Password', password, m.hasPassword ? 'Empty keeps the current one.' : null)),
  [st, off, save]);
}

// emailsCard: Overfin's emails, whose templates write each, and a sample
// of each sent to the admin.
const emailNames = { verify: 'Confirming an address', reset: 'A new password', exists: 'Already an account', invite: 'Invitation',
  test: 'Test', joined: 'Notice: a new account', expiring: 'Notice: access ends soon', expired: 'Notice: access ended',
  ending: 'To the person: their access ends soon' };
function emailsCard(t, m) {
  if (!t) return null;
  const st = status('');
  const on = Boolean(m && m.host);
  const rows = t.emails.map((e) => {
    const yours = [e.html, e.text].filter(Boolean);
    const send = h('button', { type: 'button', disabled: !on, onclick: () => busy(send, async () => {
      const out = await api('POST', `/admin/api/mail/templates/${e.name}/test`, {});
      st.set(out.summary, 'ok');
    }, (err) => st.set(err.message, 'error')) }, 'Send me this one');
    return setting(emailNames[e.name] || e.name, yours.length ? yours.join(', ') : null,
      h('div', { class: 'row' }, yours.length ? badge('Yours', 'info') : badge('Overfin\'s', 'plain'), send));
  });
  return card({ title: 'Emails', description: `What people get, in their language. Overfin's own, or yours: put templates of the same names in ${t.dir} (README.md, Your brand, and your emails, says how).` },
    rows, [st]);
}

function seerrCard(sr) {
  if (!sr) return null;
  const url = h('input', { value: sr.url, autocomplete: 'off', spellcheck: 'false', placeholder: 'http://192.168.1.10:5055' });
  const key = h('input', { type: 'password', autocomplete: 'off', placeholder: sr.url ? 'Unchanged' : '' });
  const st = status(sr.url ? 'Requests go to Seerr.' : 'Without Seerr, nobody can request titles.');
  const send = (body, button) => busy(button, async () => {
    if (!body.url.trim() && !sr.url) {
      toast('Nothing to save.');
      return;
    }
    st.set('Testing…');
    const out = await api('PUT', '/admin/api/seerr', body);
    st.set(out.summary, 'ok');
    toast(out.url ? 'Seerr saved.' : 'Seerr disconnected.');
    route();
  }, (err) => st.set(err.message, 'error'));
  const save = h('button', { class: 'primary', type: 'button', onclick: () => send({ url: url.value, key: key.value }, save) }, 'Test and save');
  const off = sr.url && h('button', { type: 'button', onclick: async () => {
    if ((await ask('Disconnect Seerr? Requests stop until it\'s connected again.', { yes: 'Disconnect', danger: true }))) send({ url: '', key: '' }, off);
  } }, 'Disconnect');
  return card({ title: 'Seerr', badge: sr.url ? badge('Connected', 'ok') : badge('Not connected'),
    description: 'Optional: where requests go. Keep it off the internet; only Overfin needs to reach it.' },
  h('div', { class: 'fields' },
    field('Address', url, 'As Overfin reaches it.', true),
    field('API key', key, 'In Seerr\'s settings, under General.', true)),
  [st, off, save]);
}

// languagesCard: the languages offered (each account picks one when there
// are several) and the default, for new accounts and before sign-in.
function languagesCard(l) {
  if (!l) return null;
  const st = status('');
  const def = h('select', { 'aria-label': 'Default language' });
  const boxes = l.choices.map((c) => {
    const box = h('input', { type: 'checkbox', value: c.tag, 'aria-label': c.name });
    box.checked = l.offered.includes(c.tag);
    box.addEventListener('change', () => drawDefault());
    return [c, box];
  });
  const drawDefault = () => {
    const keep = def.value || l.default;
    const offered = boxes.filter(([, b]) => b.checked);
    fill(def, offered.map(([c]) => h('option', { value: c.tag }, c.name)));
    def.value = offered.some(([c]) => c.tag === keep) ? keep : (offered[0]?.[0].tag || '');
  };
  drawDefault();
  def.value = l.default;
  const save = h('button', { class: 'primary', type: 'button', onclick: async () => busy(save, async () => {
    const offered = boxes.filter(([, b]) => b.checked).map(([c]) => c.tag);
    const changed = def.value !== l.default;
    if (changed && !(await ask('Titles\' texts and artwork are fetched again in the new default language. Change it?', { yes: 'Change' }))) return;
    const out = await api('PUT', '/admin/api/languages', { offered, default: def.value });
    Object.assign(l, out);
    st.set('Saved.', 'ok');
    toast('Languages saved.');
  }, (err) => st.set(err.message, 'error')) }, 'Save');
  return card({ title: 'Languages', description: 'Of the site, titles and emails. With several, each account picks its own.' },
    [setting('Default language', 'For new accounts, and before signing in.', def),
      h('div', { class: 'checks' }, boxes.map(([c, box]) => h('label', { class: 'inline' }, box, c.name)))],
    [st, save]);
}

// TMDB: the operator's token, the ratings country kids profiles are held to,
// and which titles not in the libraries are shown.
function tmdbCard(rt) {
  if (!rt) return null;
  // The token: tested by TMDB before it's kept; never shown again.
  const keySt = status('');
  const key = h('input', { type: 'password', autocomplete: 'off', placeholder: rt.token.set ? 'Unchanged' : 'API Read Access Token', disabled: rt.token.inEnv });
  const setKey = (button, value, done) => busy(button, async () => {
    const out = await api('PUT', '/admin/api/tmdb-token', { token: value });
    key.value = '';
    toast(out.summary);
    done(out.state);
  }, (err) => keySt.set(err.message, 'error'));
  const redraw = () => serverView();
  const testKey = h('button', { type: 'button', disabled: rt.token.inEnv, onclick: () => {
    if (!key.value.trim()) return keySt.set('Paste the token first.', 'error');
    setKey(testKey, key.value, (state) => { Object.assign(rt, state); redraw(); });
  } }, 'Test and save');
  const removeKey = rt.token.set && !rt.token.inEnv && h('button', { type: 'button', class: 'danger', onclick: async () => {
    if (!(await ask('Without a token, the site can\'t be used for now: titles lose their names and pictures, and TMDB\'s data kept so far is removed unless Seerr gives it. Remove the token?', { yes: 'Remove', danger: true }))) return;
    setKey(removeKey, '', (state) => { Object.assign(rt, state); redraw(); });
  } }, 'Remove');
  const keyHint = rt.token.inEnv
    ? 'Set in Overfin\'s environment (OVERFIN_TMDB_TOKEN): change it there.'
    : h('span', {}, 'Free, once: sign up at ', h('a', { href: 'https://www.themoviedb.org/signup', target: '_blank', rel: 'noopener' }, 'themoviedb.org'),
      ', then in your account\'s ', h('a', { href: 'https://www.themoviedb.org/settings/api', target: '_blank', rel: 'noopener' }, 'Settings → API'),
      ', ask for API access (personal use is fine), then copy the API Read Access Token: the long one, not the API key.');
  let names = null;
  try { names = new Intl.DisplayNames([navigator.language, 'en'], { type: 'region' }); } catch { names = null; }
  const countries = rt.countries.length ? rt.countries : [rt.country];
  const country = h('select', {}, countries
    .map((cc) => [cc, `${(names && names.of(cc)) || cc} (${cc})`])
    .sort((a, b) => a[1].localeCompare(b[1]))
    .map(([cc, label]) => h('option', { value: cc }, label)));
  country.value = rt.country;
  const shown = h('select', {}, [['all', 'All, announced ones too'], ['released', 'Released, in theatres too'],
    ['home', 'Watchable at home']].map(([v, t]) => h('option', { value: v }, t)));
  shown.value = rt.shown;
  const describe = (r) => {
    if (!r.source && !r.token.set) return ['Without TMDB, no title can be rated, so kids profiles see nothing.', 'error'];
    if (!r.source) return ['TMDB can\'t be reached: no title can be rated, so kids profiles see nothing.', 'error'];
    if (!r.countries.length) return ['Fetching the rating scales from TMDB… Kids profiles see nothing until titles are rated.', ''];
    return [`${r.fetched.toLocaleString()} of ${r.total.toLocaleString()} films and series rated.`, ''];
  };
  const st = status('');
  st.set(...describe(rt));
  const save = h('button', { class: 'primary', type: 'button', disabled: !rt.countries.length, onclick: async () => busy(save, async () => {
    if (country.value === rt.country && shown.value === rt.shown) return;
    if (country.value !== rt.country &&
      !(await ask('Every kids profile will need new limits from its account\'s owner, and sees nothing until then. Change the country?', { yes: 'Change' }))) {
      country.value = rt.country;
      return;
    }
    const out = await api('PUT', '/admin/api/ratings', { country: country.value, shown: shown.value });
    Object.assign(rt, { country: out.country, shown: out.shown });
    st.set(...describe(out));
    toast('TMDB settings saved.');
  }, (err) => st.set(err.message, 'error')) }, 'Save');
  return card({ title: 'TMDB', badge: rt.source ? badge('Reachable', 'ok') : badge(rt.token.set ? 'Unreachable' : 'No token', 'err'),
    description: 'Where titles, artwork and ratings come from. For now, the site can\'t be used without it.' },
  [
    setting('Token', rt.token.inEnv ? keyHint : 'TMDB\'s API Read Access Token.', h('div', { class: 'row' }, key, testKey, removeKey)),
    !rt.token.inEnv && !rt.token.set && h('p', { class: 'hint' }, keyHint),
    keySt,
    setting('Ratings country', 'Titles\' ages, for kids profiles and categories, go by this country\'s ratings first; a title with none there gets the middle of the other countries\'.', country),
    setting('Titles shown', 'Of those no library has, in discovery and requests. A title a library has is always shown.', shown),
  ],
  [st, save]);
}

// ---- Features and limits -------------------------------------------------------

const DRAFT = 'overfin-panel-draft';

async function featuresView() {
  const state = await api('GET', '/admin/api/features');
  let draft = null;
  try { draft = JSON.parse(sessionStorage.getItem(DRAFT) || 'null'); } catch { draft = null; }
  // A draft fills in over the current settings: one kept from an older
  // version may lack sections, or have some that are gone.
  const start = structuredClone({ ...state.features,
    ...Object.fromEntries(Object.entries(draft || {}).filter(([k]) => k in state.features)) });

  const fields = [];
  const bind = (path, get, set, input, value) => {
    fields.push({ path, get, set, input, value });
    input.addEventListener(input.type === 'checkbox' ? 'change' : 'input', changed);
    return input;
  };
  // A number field; zero, when 0 has a meaning there, says it.
  const numbers = [];
  const num = (label, hint, path, min, max, zero) => {
    const input = h('input', { type: 'number', min: String(min), max: max && String(max), 'aria-label': label });
    numbers.push({ input, label, zero });
    return setting(label, hint, bind(path, (el) => Number(el.value), (el, v) => { el.value = v; }, input));
  };
  const choice = (label, hint, path, options, sub) => setting(label, hint,
    bind(path, (el) => el.value, (el, v) => { el.value = v; }, h('select', {}, options.map(([v, t]) => h('option', { value: v }, t)))), sub);
  const toggle = (label, hint, path, value, sub) => setting(label, hint,
    bind(path, (el) => el.checked, (el, list) => { el.checked = (list || []).includes(value); }, h('input', { type: 'checkbox', 'aria-label': label }), value), sub);
  const text = (label, hint, path) => setting(label, hint,
    bind(path, (el) => el.value.trim(), (el, v) => { el.value = v || ''; }, h('input', { autocomplete: 'off', spellcheck: 'false', 'aria-label': label })), true);
  const onOff = (label, hint, path) => setting(label, hint,
    bind(path, (el) => (el.checked ? 'on' : 'off'), (el, v) => { el.checked = v === 'on'; }, h('input', { type: 'checkbox', 'aria-label': label })), true);

  // Each feature: its switch, and under it its own settings, dimmed while
  // it's off.
  const features = state.plugins.map((p) => {
    const subs = (p.settings || []).map((s) => {
      const path = `modules.${p.name}.${s.key}`;
      if (s.text) return text(s.label, s.hint, path);
      return s.choices ? choice(s.label, s.hint, path, s.choices.map((c) => [c.value, c.label]), true) : onOff(s.label, s.hint, path);
    });
    const main = toggle(featureName(p.name), [p.description, p.needs && `Needs ${p.needs.map(featureName).join(', ')}.`].filter(Boolean).join(' '),
      'plugins.enable', p.name);
    const sw = main.querySelector('input');
    const dim = () => subs.forEach((s) => s.classList.toggle('off', !sw.checked));
    sw.addEventListener('change', dim);
    return { main, subs, dim };
  });

  const at = (obj, path) => path.split('.').reduce((o, k) => o?.[k], obj);
  const load = (f) => {
    for (const fl of fields) fl.set(fl.input, at(f, fl.path));
    features.forEach((x) => x.dim());
  };
  const collect = () => {
    const out = structuredClone(state.features);
    for (const fl of fields) {
      const keys = fl.path.split('.');
      const parent = keys.slice(0, -1).reduce((o, k) => (o[k] ??= {}), out);
      const key = keys.at(-1);
      if (fl.value !== undefined) {
        const list = new Set(parent[key] || []);
        if (fl.get(fl.input)) list.add(fl.value); else list.delete(fl.value);
        parent[key] = [...list].sort();
      } else {
        parent[key] = fl.get(fl.input);
      }
    }
    return out;
  };
  // What's saved, as the form reads it back: unsaved changes are any
  // difference from it.
  let baseline = '';
  const settle = () => {
    load(state.features);
    baseline = JSON.stringify(collect());
  };

  const bar = h('div', { class: 'savebar', hidden: true });
  const message = h('span', { class: 'grow' }, 'You have unsaved changes.');
  const discard = h('button', { type: 'button', class: 'ghost', onclick: () => {
    sessionStorage.removeItem(DRAFT);
    settle();
    changed();
  } }, 'Discard');
  const save = h('button', { type: 'button', class: 'primary', onclick: () => busy(save, async () => {
    // An empty number field is refused, not saved as 0.
    for (const n of numbers) numberIn(n.input, n.label, n.zero);
    const wanted = collect();
    try {
      const out = await api('PUT', '/admin/api/features', wanted);
      sessionStorage.removeItem(DRAFT);
      state.features = out.features;
      settle();
      changed();
      toast('Saved and applied.');
    } catch (err) {
      if (err.type === 'step-up-required') {
        // Changes need a recent sign-in: keep them, sign in, come back.
        sessionStorage.setItem(DRAFT, JSON.stringify(wanted));
        location.href = '/admin/login?return=/admin/%23/features';
        return;
      }
      throw err;
    }
  }) }, 'Save changes');
  fill(bar, message, discard, save);
  function changed() {
    bar.hidden = JSON.stringify(collect()) === baseline;
  }

  show(
    pageHead('Features & limits', 'What this server offers, and how far it goes. Changes apply once saved.'),
    draft && h('div', { class: 'notice warn' }, 'Your unsaved changes are back: save them to apply them.'),
    card({ title: 'Features', description: 'Switched on and off for everyone at once.' }, features.map((x) => [x.main, x.subs])),
    h('div', { class: 'cards' },
      card({ title: 'Streams' }, [
        choice('One stream too many', 'When an account starts more streams than its template allows.', 'limits.onLimit',
          [['replace-oldest', 'Replace the oldest'], ['refuse', 'Refuse the new one']]),
        num('Transcodes on the server', 'Re-encoded video only, not repackaging. 0: no limit.', 'limits.maxTranscodesTotal', 0, undefined, '0: no limit'),
        num('Bitrate cap (kbps)', '0: no cap.', 'limits.maxBitrateKbps', 0, undefined, '0: no cap'),
      ]),
      card({ title: 'Accounts' }, [
        choice('When an account is deleted', 'What happens to its watch history.', 'accounts.onDelete',
          [['delete', 'Delete it too'], ['disable', 'Keep it, unusable']]),
        num('Wrong PINs before a lock', 'From 3 to 10.', 'limits.pinAttempts', 3, 10),
      ])),
    card({ title: 'Privacy', description: 'Details hidden from everyone unless switched on here.' },
      state.exposableFields.map((name) => toggle(privacyName(name), null, 'privacy.expose', name))),
    bar);
  settle();
  load(start);
  changed();
}

const featureNames = { library: 'Library', playback: 'Playback', watchlist: 'Watchlist', invites: 'Invitations', migration: 'Migration' };
const featureName = (n) => featureNames[n] || n.charAt(0).toUpperCase() + n.slice(1);
const privacyNames = {
  'version.names': 'Version names (from file names)', 'photo.takenAt': 'When photos were taken', 'photo.location': 'Where photos were taken',
  'photo.camera': 'Which camera took photos', 'item.tags': 'Tags',
};
const privacyName = (n) => privacyNames[n] || n;

// ---- People --------------------------------------------------------------------

const statusText = { active: 'Active', disabled: 'Disabled', expired: 'Expired' };
const statusKind = { active: 'ok', disabled: 'err', expired: 'warn' };
const lastActive = (a) => a.sessions.map((s) => s.lastActiveAt).sort().at(-1) || '';
// nameOf is the best name for an account: the identity provider's, then the
// operator's label, then the owner profile's name.
const nameOf = (a) => a.person?.name || a.person?.username || a.label || a.profiles.find((p) => p.owner)?.name || 'Unnamed';
const subline = (a) => [a.person?.username && a.person.name ? a.person.username : '', a.person?.email && a.person.email !== nameOf(a) ? a.person.email : '']
  .filter(Boolean).join(' · ');

async function peopleView(selected = '') {
  const [{ items, directory }, { items: templates }, people] = await Promise.all([
    api('GET', '/admin/api/accounts'), api('GET', '/admin/api/templates'),
    // Only with managed sign-in: who may sign in, by email.
    api('GET', '/admin/api/people').then((r) => r.items).catch(() => null)]);
  $('#main').classList.add('wide');
  const reload = (id = selected) => peopleView(id);
  const search = h('input', { type: 'search', placeholder: 'Search by name, email or profile', 'aria-label': 'Search' });
  const filter = h('select', { 'aria-label': 'Status' }, h('option', { value: '' }, 'Every status'),
    ...Object.entries(statusText).map(([v, t]) => h('option', { value: v }, t)));
  const tbody = h('tbody', {});
  const detail = h('aside', { class: 'detail' }, h('div', { class: 'placeholder' }, 'Select someone to see their account.'));

  const select = (a) => {
    selected = a.id;
    for (const tr of tbody.children) tr.classList.toggle('selected', tr.dataset.id === a.id);
    fill(detail, accountDetail(a, templates, reload, people));
  };
  const draw = () => {
    const q = search.value.trim().toLowerCase();
    const shown = items.filter((a) => (!filter.value || a.status === filter.value) &&
      (!q || [a.label, a.person?.name, a.person?.username, a.person?.email, a.subject, a.id, ...a.profiles.map((p) => p.name)]
        .some((v) => (v || '').toLowerCase().includes(q))));
    fill(tbody, shown.map((a) => {
      const kids = a.profiles.filter((p) => p.kind === 'kids').length;
      return h('tr', { 'data-id': a.id, onclick: () => select(a) },
        h('td', {}, h('div', { class: 'who' }, avatar(nameOf(a)), h('div', {}, h('div', { class: 'name' }, nameOf(a)),
          h('div', { class: 'hint' }, subline(a) || (a.person ? '' : 'name unknown'))))),
        h('td', {}, badge(statusText[a.status] || a.status, statusKind[a.status]),
          a.person && !a.person.active && h('div', { class: 'hint' }, 'off at the identity provider')),
        h('td', { class: 'hide-medium' }, a.template.name),
        h('td', { class: 'num' }, String(a.profiles.length), kids > 0 && h('span', { class: 'hint' }, ` · ${kids} kids`)),
        h('td', { class: 'hint hide-narrow' }, ago(lastActive(a)) || 'never'));
    }));
    if (!shown.length) tbody.append(h('tr', {}, h('td', { colspan: '5', class: 'empty' }, items.length ? 'Nobody matches.' : 'No accounts yet.')));
    const current = items.find((a) => a.id === selected);
    if (current) select(current);
  };
  search.addEventListener('input', draw);
  filter.addEventListener('change', draw);

  const notice = !people && {
    none: 'Names come from the identity provider through a connector (see [connector] in the configuration); until then, labels tell accounts apart.',
    unreachable: 'The identity provider\'s connector didn\'t answer, so names are missing for now. The log says why.',
  }[directory];
  const waiting = people ? people.filter((x) => !x.accountId) : [];
  const addButton = people && h('button', { class: 'primary', type: 'button', onclick: () => addPerson(() => reload()) }, 'Add a person');

  show(
    pageHead('People', people
      ? 'Everyone who can sign in, and their accounts. People sign in with their email and their password.'
      : 'Everyone with an account here.', addButton),
    h('div', { class: 'split' },
      h('div', {},
        h('div', { class: 'toolbar' }, search, filter),
        notice && h('p', { class: 'notice' }, notice),
        h('div', { class: 'table-wrap' }, h('table', {},
          h('thead', {}, h('tr', {},
            h('th', {}, `Accounts (${items.length})`), h('th', {}, 'Status'), h('th', { class: 'hide-medium' }, 'Template'),
            h('th', {}, 'Profiles'), h('th', { class: 'hide-narrow' }, 'Last active'))),
          tbody)),
        waiting.length > 0 && card({ title: `No account yet (${waiting.length})`,
          description: 'They can sign in; their account is made when they first open the site.' },
        h('ul', { class: 'list' }, waiting.map((x) => h('li', {}, avatar(x.email), h('div', {}, personControls(x, () => reload())))))),
      ),
      detail));
  draw();
}

// addPerson is the form to make someone's account here, in a window: their
// email and a first password, which they choose again at their first
// sign-in unless told otherwise.
function addPerson(reload) {
  const email = h('input', { type: 'email', placeholder: 'name@example.org', autocomplete: 'off', required: true });
  const password = h('input', { type: 'password', autocomplete: 'new-password', required: true });
  const mustChange = h('input', { type: 'checkbox', checked: true });
  const admin = h('input', { type: 'checkbox' });
  formModal({ title: 'Add a person', description: 'Their account is ready at once: hand them their email and this password, privately.', saveLabel: 'Add' },
    h('div', { class: 'fields' },
      field('Email', email, null, true),
      field('Password', password, 'The rules are on People → Registration.', true),
      h('label', { class: 'inline full' }, mustChange, 'Ask for a new password at the first sign-in'),
      h('label', { class: 'inline full' }, admin, 'Admin')),
    async () => {
      await api('POST', '/admin/api/people', { email: email.value, password: password.value, mustChange: mustChange.checked, admin: admin.checked });
      toast(`${email.value} can sign in now.`);
      reload();
    });
}

// personControls are what an admin can do about someone who signs in here:
// a new password (they forgot theirs), the admin mark, and, before they
// have an account, removing them.
function personControls(x, reload) {
  const passwordButton = h('button', { type: 'button', class: 'small', onclick: () => {
    const password = h('input', { type: 'password', autocomplete: 'new-password', required: true });
    const mustChange = h('input', { type: 'checkbox', checked: true });
    formModal({ title: `A new password for ${x.email}`, description: 'Hand it over privately. Their logins end.', saveLabel: 'Set it' },
      h('div', { class: 'fields' },
        field('New password', password, null, true),
        h('label', { class: 'inline full' }, mustChange, 'Ask for a new password at the next sign-in')),
      async () => {
        await api('PUT', `/admin/api/people/${x.id}/password`, { password: password.value, mustChange: mustChange.checked });
        toast(`${x.email} has a new password; their logins ended.`);
        reload();
      });
  } }, 'Set a new password');
  const adminButton = !x.you && h('button', { type: 'button', class: 'small', onclick: () => busy(adminButton, async () => {
    await api('PATCH', `/admin/api/people/${x.id}`, { admin: !x.admin });
    toast(x.admin ? `${x.email} is no longer an admin.` : `${x.email} is an admin now.`);
    reload();
  }) }, x.admin ? 'Remove admin' : 'Make admin');
  const removeButton = !x.accountId && !x.you && h('button', { type: 'button', class: 'small danger', onclick: async () => {
    if (!(await ask(`Remove ${x.email}? They can't sign in any more.`, { yes: 'Remove', danger: true }))) return;
    busy(removeButton, async () => {
      await api('DELETE', `/admin/api/people/${x.id}`);
      toast(`${x.email} removed.`);
      reload();
    });
  } }, 'Remove');
  const state = !x.confirmed ? 'Registered: waiting for their address\'s confirmation'
    : x.mustChange ? 'Chooses a new password at the next sign-in' : '';
  return [
    h('div', { class: 'name' }, x.email, ' ', x.admin && badge('Admin', 'info'), ' ', x.you && badge('You', 'plain'), ' ', x.disabled && badge('Disabled', 'err')),
    state && h('div', { class: 'hint' }, state),
    h('div', { class: 'actions-row' }, passwordButton, adminButton, removeButton),
  ];
}

// accountDetail is the selected account: who, how they sign in, access,
// profiles, devices, and the destructive actions, set apart.
function accountDetail(a, templates, reload, people) {
  const signsIn = people?.find((x) => x.accountId === a.id);
  const patch = (button, change, done) => busy(button, async () => {
    await api('PATCH', `/admin/api/accounts/${a.id}`, change);
    toast(done);
    reload(a.id);
  });
  // Its access, in a window: its label (without the identity provider's
  // name), its template, when it ends.
  const editAccess = () => {
    const label = h('input', { value: a.label || '', maxlength: '64', placeholder: 'e.g. Bob' });
    const expires = h('input', { type: 'datetime-local', value: toLocalInput(a.expiresAt) });
    const template = h('select', {}, templates.map((t) => h('option', { value: t.id, selected: t.id === a.template.id },
      `${t.name} · ${plural(t.maxStreams, 'stream')}, ${plural(t.maxProfiles, 'profile')}${t.reserveStreams ? ', one each' : ''}`)));
    formModal({ title: `${nameOf(a)}'s access` },
      h('div', { class: 'fields' },
        !a.person && field('Label', label, 'A name for it, until the identity provider gives one.', true),
        field('Template', template, 'What the account may do.', true),
        field('Access ends', expires, 'Empty: never.', true)),
      async () => {
        await api('PATCH', `/admin/api/accounts/${a.id}`, {
          ...(a.person ? {} : { label: label.value }),
          expiresAt: expires.value ? new Date(expires.value).toISOString() : '',
          templateId: template.value,
        });
        toast('Access saved.');
        reload(a.id);
      });
  };

  const profiles = a.profiles.map((p) => {
    const unlock = p.pinLocked && h('button', { type: 'button', class: 'small', onclick: () => busy(unlock, async () => {
      await api('POST', `/admin/api/profiles/${p.id}/unlock-pin`, {});
      toast(`${p.name}'s PIN is unlocked.`);
      reload(a.id);
    }) }, 'Unlock PIN');
    return h('li', {}, avatar(p.name), h('div', {},
      h('div', { class: 'name' }, p.name, ' ', p.owner && badge('Owner', 'plain'), ' ', p.kind === 'kids' && badge('Kids', 'info')),
      h('div', { class: 'hint' }, [
        p.kind === 'kids' && (p.maxAge !== undefined ? `Up to ${p.maxAge} years` : 'Age not chosen yet'),
        p.hasPin ? (p.pinLocked ? 'PIN locked' : 'PIN set') : 'No PIN',
      ].filter(Boolean).join(' · '))), unlock);
  });
  const devices = a.sessions.map((s) => {
    const out = h('button', { type: 'button', class: 'small', onclick: () => busy(out, async () => {
      await api('DELETE', `/admin/api/accounts/${a.id}/sessions/${s.id}`);
      toast('Signed out.');
      reload(a.id);
    }) }, 'Sign out');
    return h('li', {}, h('div', {}, h('div', {}, s.device || 'Unknown device'), h('div', { class: 'hint' }, `${s.profile} · ${ago(s.lastActiveAt)}`)), out);
  });
  const toggleAccess = h('button', { type: 'button', onclick: () => patch(toggleAccess, { disabled: a.status !== 'disabled' },
    a.status === 'disabled' ? 'Account enabled.' : 'Account disabled.') }, a.status === 'disabled' ? 'Enable account' : 'Disable account');
  const remove = h('button', { type: 'button', class: 'danger', onclick: async () => {
    if (!(await ask(`Delete ${nameOf(a)}'s account, its profiles and what they hold? This can't be undone.`, { yes: 'Delete', danger: true }))) return;
    busy(remove, async () => {
      await api('DELETE', `/admin/api/accounts/${a.id}`);
      toast('Account deleted.');
      reload('');
    });
  } }, 'Delete account');

  return [
    h('div', { class: 'detail-head' }, avatar(nameOf(a), true), h('div', {},
      h('h2', {}, nameOf(a)), h('div', { class: 'hint' }, subline(a) || a.person?.email || ''))),
    h('div', {}, badge(statusText[a.status] || a.status, statusKind[a.status]),
      a.person && !a.person.active && [' ', badge('Off at the identity provider', 'err')]),
    h('h3', {}, 'About'),
    h('dl', { class: 'facts' },
      h('dt', {}, 'Since'), h('dd', {}, when(a.createdAt)),
      h('dt', {}, 'Last active'), h('dd', {}, ago(lastActive(a)) || 'never'),
      h('dt', {}, 'Sign-in ID'), h('dd', { class: 'hint' }, a.subject)),
    signsIn && [h('h3', {}, 'Signing in'), h('div', { class: 'box' }, personControls(signsIn, () => reload(a.id)))],
    h('h3', {}, 'Access'),
    h('dl', { class: 'facts' },
      !a.person && [h('dt', {}, 'Label'), h('dd', {}, a.label || '—')],
      h('dt', {}, 'Template'), h('dd', {}, a.template.name),
      h('dt', {}, 'Access ends'), h('dd', {}, a.expiresAt ? when(a.expiresAt) : 'Never')),
    h('div', { class: 'actions-row' }, h('button', { type: 'button', class: 'small', onclick: editAccess }, 'Edit access')),
    h('h3', {}, `Profiles (${a.profiles.length})`), h('ul', { class: 'list' }, profiles),
    a.historyCopiedAt && historyCopy(a, reload),
    h('h3', {}, `Signed-in devices (${a.sessions.length})`),
    devices.length ? h('ul', { class: 'list' }, devices) : h('p', { class: 'hint' }, 'None.'),
    h('div', { class: 'danger-zone' }, toggleAccess, remove),
  ];
}

// historyCopy says when the account brought a history from another server,
// and lets it bring one again: if the wrong one came, or the wrong person
// took someone's.
function historyCopy(a, reload) {
  const again = h('button', { type: 'button', class: 'small', onclick: async () => {
    if (!(await ask('Let this account bring a history again? What was copied stays; the account it came from can be copied again, by anyone with its login.', { yes: 'Allow' }))) return;
    busy(again, async () => {
      await api('DELETE', `/admin/api/accounts/${a.id}/history-copy`);
      toast('This account may bring a history again.');
      reload(a.id);
    });
  } }, 'Allow it again');
  return [h('h3', {}, 'History brought over'),
    h('div', { class: 'box' }, h('div', {}, `From the other server, ${when(a.historyCopiedAt)}.`),
      h('div', { class: 'actions-row' }, again))];
}

// ---- Templates -----------------------------------------------------------------

// templatesView lists the account templates, each editable in place.
// Accounts follow theirs live: saving changes every account on it at once.
async function templatesView() {
  const [{ items }, cats] = await Promise.all([api('GET', '/admin/api/templates'), api('GET', '/admin/api/categories')]);
  const catName = (c) => c.names[cats.defaultLanguage] || Object.values(c.names)[0] || 'Unnamed';
  const catNames = (ids) => (ids || []).map((id) => cats.items.find((c) => c.id === id)).filter(Boolean).map(catName);
  // A template's form, in a window: a new one when t is null.
  const edit = (t) => {
    const name = h('input', { value: t?.name || '', maxlength: '40', placeholder: 'e.g. Family, Friends', required: true });
    const streams = h('input', { type: 'number', min: '1', max: '100', value: String(t?.maxStreams ?? 1) });
    const profiles = h('input', { type: 'number', min: '1', max: '20', value: String(t?.maxProfiles ?? 1) });
    const reserve = h('input', { type: 'checkbox', checked: !!t?.reserveStreams, 'aria-label': 'A stream for each profile' });
    // Only some categories: none ticked is no limit.
    const catBoxes = cats.items.map((c) => {
      const box = h('input', { type: 'checkbox', 'aria-label': `Only: ${catName(c)}` });
      box.checked = (t?.categories || []).includes(c.id);
      box.dataset.v = c.id;
      return h('label', { class: 'inline' }, box, catName(c));
    });
    const picked = () => catBoxes.map((l) => l.querySelector('input')).filter((b) => b.checked).map((b) => b.dataset.v);
    formModal({
      title: t ? `Edit ${t.name}` : 'New template',
      description: t ? `${plural(t.accounts, 'account')} on it${t.accounts ? '; changes apply to them at once' : ''}.` : 'For a group of accounts that may do more, or less.',
      saveLabel: t ? 'Save' : 'Create',
    }, [h('div', { class: 'fields' }, field('Name', name, null, true), field('Streams at once', streams), field('Profiles', profiles, 'The owner\'s included.')),
      setting('A stream for each profile', 'Each profile keeps one of the streams for itself; the rest are shared. Nobody is cut off to make room. No more profiles than streams.', reserve),
      catBoxes.length > 0 && h('div', { class: 'setting stack' }, h('div', {}, h('div', { class: 'label' }, 'Only these categories'),
        h('div', { class: 'hint' }, 'Every profile of its accounts, the owner\'s too, sees only their titles (and none that no library has). None ticked: no limit.')),
      h('div', { class: 'checks' }, catBoxes))],
    async () => {
      const body = { name: name.value, maxStreams: numberIn(streams, 'Streams'), maxProfiles: numberIn(profiles, 'Profiles'),
        reserveStreams: reserve.checked, categories: picked().length ? picked() : null };
      // A template kept to categories (deleted since, maybe: then it sees
      // nothing) loses its limit only when the admin says so.
      if (t?.categories && !picked().length
        && !(await ask(`No category is ticked: every account on "${t.name}" will see every title. Save anyway?`, { yes: 'Save' }))) {
        throw new Error('Not saved: tick the categories it keeps to, or save it without any.');
      }
      await (t ? api('PUT', `/admin/api/templates/${t.id}`, body) : api('POST', '/admin/api/templates', body));
      toast(t ? 'Template saved.' : 'Template created.');
      templatesView();
    });
  };
  const rows = items.map((t) => {
    // Only one no account follows: move them first, in People.
    const remove = !t.default && h('button', { type: 'button', class: 'small danger', disabled: t.accounts > 0,
      title: t.accounts > 0 ? `${plural(t.accounts, 'account')} follow it: move them to another template first, in People.` : undefined,
      onclick: async () => {
        if (!(await ask(`Delete the template "${t.name}"?`, { yes: 'Delete', danger: true }))) return;
        busy(remove, async () => { await api('DELETE', `/admin/api/templates/${t.id}`); toast('Template deleted.'); templatesView(); });
      } }, 'Delete');
    const only = catNames(t.categories);
    return h('tr', {},
      h('td', {}, h('div', { class: 'name' }, t.name, t.default && ' ', t.default && badge('Default', 'info')),
        h('div', { class: 'hint' }, only.length ? `Only ${only.join(', ')}` : 'Every category')),
      h('td', {}, String(t.maxStreams), t.reserveStreams && h('div', { class: 'hint' }, 'one per profile')),
      h('td', {}, String(t.maxProfiles)),
      h('td', {}, String(t.accounts)),
      h('td', { class: 'row-actions' }, h('button', { type: 'button', class: 'small', onclick: () => edit(t) }, 'Edit'), remove));
  });
  show(pageHead('Templates', 'What accounts may do. Every account follows one: Default, unless you choose another for it in People.',
    h('button', { type: 'button', class: 'primary', onclick: () => edit(null) }, 'New template')),
    card({ title: 'Templates' }, h('div', { class: 'table-wrap' }, h('table', {},
      h('thead', {}, h('tr', {}, h('th', {}, 'Template'), h('th', {}, 'Streams'), h('th', {}, 'Profiles'), h('th', {}, 'Accounts'), h('th', {}))),
      h('tbody', {}, rows)))));
}

// ---- Apps ----------------------------------------------------------------------

const appScopeNames = { score: 'Values', stats: 'Watch statistics', availability: 'In the libraries', 'watch-stats': 'Watch statistics' };

// appsView lists the keys other apps (Overtide…) read Overfin's values and
// statistics with: made here, for what the operator ticks, shown once,
// revoked here.
async function appsView() {
  const { items } = await api('GET', '/admin/api/apps');
  // A new key, in a window; then the key itself, once, in another.
  const newKey = () => {
    const name = h('input', { type: 'text', placeholder: 'e.g. Overtide', maxlength: '64', required: true });
    const scopes = Object.fromEntries(['score', 'stats', 'availability'].map((s) => [s, h('input', { type: 'checkbox', checked: true })]));
    formModal({ title: 'New key', description: 'For one app: what it may read.', saveLabel: 'Make the key' },
      [field('For', name, null, true),
        h('div', { class: 'setting stack' }, h('div', {}, h('div', { class: 'label' }, 'It may read')),
          h('div', { class: 'checks' }, Object.entries(scopes).map(([s, box]) => h('label', { class: 'inline' }, box, appScopeNames[s]))))],
      async () => {
        const picked = Object.keys(scopes).filter((s) => scopes[s].checked);
        const k = await api('POST', '/admin/api/apps', { name: name.value, scopes: picked });
        appsView();
        setTimeout(() => {
          const close = modal({ title: `${k.name}'s key` },
            secret('Copy it now: it won\'t be shown again.', k.key,
              'The app sends it as "Authorization: Bearer <key>" to GET /apps/v1/titles on this address.'),
            h('span', { class: 'grow' }), h('button', { type: 'button', class: 'primary', onclick: () => close() }, 'Done'));
        }, 0);
      });
  };
  const rows = items.map((k) => {
    const revoke = h('button', { type: 'button', class: 'small danger', onclick: async () => {
      if (!(await ask(`Revoke ${k.name}'s key? The app stops getting anything.`, { yes: 'Revoke', danger: true }))) return;
      busy(revoke, async () => { await api('DELETE', `/admin/api/apps/${k.id}`); toast('Key revoked.'); appsView(); });
    } }, 'Revoke');
    return h('tr', {},
      h('td', {}, h('div', { class: 'name' }, k.name),
        h('div', { class: 'hint' }, [...new Set(k.scope.split(' ').map((s) => appScopeNames[s] || s))].join(', '))),
      h('td', { class: 'hint' }, day(k.createdAt)),
      h('td', { class: 'hint' }, k.lastUsedAt ? ago(k.lastUsedAt) : 'never'),
      h('td', { class: 'row-actions' }, revoke));
  });
  show(pageHead('Apps', 'Other apps (Overtide…) can read each film\'s and season\'s value, how much it\'s watched (plays, viewers, favorites, the day last played) and whether the libraries have it, as each key allows. Totals per title only, never anything about a person; counts below 2 are given as 0, and so are plays and favorites unless two people account for them.',
    h('button', { type: 'button', class: 'primary', onclick: newKey }, 'New key')),
    card({ title: 'Keys' },
      h('div', { class: 'table-wrap' }, h('table', {},
        h('thead', {}, h('tr', {}, h('th', {}, 'App'), h('th', {}, 'Made'), h('th', {}, 'Last used'), h('th', {}))),
        h('tbody', {}, rows.length ? rows : h('tr', {}, h('td', { colspan: '4', class: 'empty' }, 'No app has a key yet.')))))));
}

// ---- Categories ----------------------------------------------------------------

// categoriesView lists the site's categories in their order, edits them,
// and says whether the site's menu shows the libraries too.
async function categoriesView() {
  const data = await api('GET', '/admin/api/categories');
  const langName = (code) => { try { return new Intl.DisplayNames(['en'], { type: 'language' }).of(code) || code; } catch { return code; } };
  const nameOf = (c) => c.names[data.defaultLanguage] || Object.values(c.names)[0] || 'Unnamed';
  const genreNames = Object.fromEntries(data.genres.map((g) => [g.id, g.name]));
  const libNames = Object.fromEntries(data.libraries.map((l) => [l.id, l.name]));
  const catNames = Object.fromEntries(data.items.map((c) => [c.id, nameOf(c)]));
  const summary = (r) => [
    r.kinds?.length === 1 && (r.kinds[0] === 'movie' ? 'Films' : 'Series'),
    r.genres?.length && r.genres.map((g) => genreNames[g] || g).join(' or '),
    r.languages?.length && `in ${r.languages.map(langName).join(' or ')}`,
    r.libraries?.length && `from ${r.libraries.map((l) => libNames[l] || 'a library').join(' or ')}`,
    r.tags?.length && `tagged ${r.tags.join(' or ')}`,
    r.maxAge !== undefined && r.maxAge !== null && `for ages up to ${r.maxAge}`,
    r.except?.length && `except ${r.except.map((x) => catNames[x] || '?').join(', ')}`,
  ].filter(Boolean).join(' · ') || 'Every film and series';

  const shown = h('input', { type: 'checkbox', 'aria-label': 'Libraries in the menu' });
  shown.checked = data.librariesShown;
  shown.addEventListener('change', () => busy(null, async () => {
    await api('PUT', '/admin/api/categories/libraries', { shown: shown.checked });
    toast(shown.checked ? 'The libraries are in the site\'s menu.' : 'The libraries are out of the site\'s menu.');
  }, (err) => { shown.checked = !shown.checked; toast(err.message, true); }));

  const order = data.items.map((c) => c.id);
  const move = (i, by) => busy(null, async () => {
    [order[i], order[i + by]] = [order[i + by], order[i]];
    await api('PUT', '/admin/api/categories/order', { ids: order });
    categoriesView();
  });
  const rows = data.items.map((c, i) => {
    const del = h('button', { type: 'button', class: 'small danger', onclick: async () => {
      if (!(await ask(`Delete ${nameOf(c)}? Nothing moves on disk: only the category goes.`, { yes: 'Delete', danger: true }))) return;
      busy(del, async () => { await api('DELETE', `/admin/api/categories/${c.id}`); toast('Category deleted.'); categoriesView(); });
    } }, 'Delete');
    return h('tr', {},
      h('td', {}, h('div', { class: 'name' }, nameOf(c)), h('div', { class: 'hint' }, summary(c.rules))),
      h('td', { class: 'hint' }, plural(c.count, 'title')),
      h('td', {}, c.home ? badge('Home row', 'info') : null),
      h('td', { class: 'row-actions' },
        h('button', { type: 'button', class: 'small', disabled: i === 0, 'aria-label': 'Up', onclick: () => move(i, -1) }, '↑'),
        h('button', { type: 'button', class: 'small', disabled: i === data.items.length - 1, 'aria-label': 'Down', onclick: () => move(i, 1) }, '↓'),
        h('button', { type: 'button', class: 'small', onclick: () => categoryEditor(data, c, langName, nameOf) }, 'Edit'), del));
  });

  show(pageHead('Categories', 'Sections of the site, made from rules: Films, Series, Anime, Kids… A title is in every category it fits. Nothing moves on disk.',
    h('button', { class: 'primary', type: 'button', onclick: () => categoryEditor(data, null, langName, nameOf) }, 'New category')),
  card({ title: 'Categories', description: 'In the site\'s menu in this order, each with its page; a category with a home row also shows on the home page.' },
    h('div', { class: 'table-wrap' }, h('table', {},
      h('thead', {}, h('tr', {}, h('th', {}, 'Category'), h('th', {}, 'Titles'), h('th', {}), h('th', {}))),
      h('tbody', {}, rows.length ? rows : h('tr', {}, h('td', { colspan: '4', class: 'empty' }, 'No category yet: the site shows its libraries.')))))),
  card({ title: 'The site\'s menu' }, [setting('Libraries in the menu', 'With categories, you may want them alone: off, the menu shows only the categories.', shown)]));
}

// categoryEditor edits a category (c), or makes one (none given), in a
// window, with a live count of the titles it would have.
function categoryEditor(data, c, langName, nameOf) {
  const r = c?.rules || {};
  // The languages offered, and any other it has a name in (offered before):
  // saving keeps that name instead of dropping it.
  const langs = [...new Set([...data.languages, ...Object.keys(c?.names || {})])];
  const names = Object.fromEntries(langs.map((l) => [l, h('input', { value: c?.names[l] || '', maxlength: '40', 'aria-label': `Name in ${langName(l)}` })]));
  const boxes = (list, picked, label) => list.map(([v, text]) => {
    const box = h('input', { type: 'checkbox', 'aria-label': `${label}: ${text}` });
    box.checked = (picked || []).includes(v);
    box.dataset.v = v;
    return h('label', { class: 'inline' }, box, text);
  });
  const picked = (group) => [...group.querySelectorAll('input:checked')].map((b) => b.dataset.v);
  const kinds = h('div', { class: 'checks' }, boxes([['movie', 'Films'], ['series', 'Series']], r.kinds, 'Kind'));
  const genres = h('div', { class: 'checks' }, boxes(data.genres.map((g) => [String(g.id), g.name]), (r.genres || []).map(String), 'Genre'));
  const languages = h('div', { class: 'checks' }, boxes(data.originalLanguages.map((l) => [l, langName(l)]), r.languages, 'Language'));
  const libraries = h('div', { class: 'checks' }, boxes(data.libraries.map((l) => [l.id, l.name]), r.libraries, 'Library'));
  // Tags can be many (TMDB's keywords, with Jellyfin's default metadata):
  // the ticked first, and a search.
  const tagList = [...data.tags].sort((a, b) => Number((r.tags || []).includes(b)) - Number((r.tags || []).includes(a)));
  const tags = h('div', { class: 'checks' }, boxes(tagList.map((t) => [t, t]), r.tags, 'Tag'));
  const tagSearch = h('input', { type: 'search', placeholder: 'Find a tag', 'aria-label': 'Find a tag', oninput: () => {
    const q = tagSearch.value.trim().toLowerCase();
    for (const label of tags.children) label.hidden = Boolean(q) && !label.textContent.toLowerCase().includes(q) && !label.querySelector('input').checked;
  } });
  const except = h('div', { class: 'checks' }, boxes(data.items.filter((x) => x.id !== c?.id).map((x) => [x.id, nameOf(x)]), r.except, 'Leave out'));
  // For ages up to: the ages kids profiles choose among.
  const maxAge = h('select', { 'aria-label': 'For ages up to' },
    h('option', { value: '' }, 'Any age'), [3, 6, 8, 10, 12, 14, 16].map((a) => h('option', { value: String(a) }, `${a} years`)));
  maxAge.value = r.maxAge !== undefined && r.maxAge !== null ? String(r.maxAge) : '';
  const home = h('input', { type: 'checkbox', 'aria-label': 'A row on the home page' });
  home.checked = Boolean(c?.home);
  const rules = () => ({ kinds: picked(kinds), genres: picked(genres).map(Number), languages: picked(languages), libraries: picked(libraries),
    tags: picked(tags), maxAge: maxAge.value === '' ? undefined : Number(maxAge.value), except: picked(except) });
  const preview = h('div', { class: 'hint' }, '');
  let timer;
  const refresh = () => { clearTimeout(timer); timer = setTimeout(async () => {
    try {
      const p = await api('POST', '/admin/api/categories/preview', { id: c?.id || '', rules: rules() });
      preview.textContent = `${plural(p.count, 'title')}${p.sample.length ? `: ${p.sample.join(', ')}${p.count > p.sample.length ? '…' : ''}` : ''}`;
    } catch (err) { preview.textContent = err.message; }
  }, 300); };
  const group = (label, hint, el) => h('div', { class: 'setting stack' }, h('div', {}, h('div', { class: 'label' }, label), hint && h('div', { class: 'hint' }, hint)), el);
  const fields = h('div', { class: 'modal-fields', onchange: refresh }, [
    Object.entries(names).map(([l, i]) => field(`Name in ${langName(l)}`, i)),
    group('Films or series', null, kinds),
    group('Genres', 'TMDB\'s.', genres),
    group('Original language', null, languages),
    data.libraries.length > 1 && group('Libraries', null, libraries),
    data.tags.length > 0 && group('Tags', 'Jellyfin\'s: often TMDB\'s keywords (christmas, anime, based on novel…).', h('div', {}, data.tags.length > 12 && tagSearch, tags)),
    group('For ages up to', 'From the titles\' ratings in any country (this server\'s country first). A title without any is left out.', maxAge),
    data.items.some((x) => x.id !== c?.id) && group('Leave out', 'Titles of these categories aren\'t in this one: Series except Anime, say.', except),
    setting('A row on the home page', null, home),
    h('div', { class: 'setting' }, h('div', {}, h('div', { class: 'label' }, 'What it holds'), preview)),
  ]);
  formModal({ title: c ? `Edit ${nameOf(c)}` : 'New category', wide: true, saveLabel: c ? 'Save' : 'Create',
    description: 'A title is in it when it fits every rule you set; within a rule, any of what you tick. Nothing ticked: no limit.' },
  fields, async () => {
    const body = { names: Object.fromEntries(Object.entries(names).map(([l, i]) => [l, i.value.trim()])), rules: rules(), home: home.checked };
    await (c ? api('PUT', `/admin/api/categories/${c.id}`, body) : api('POST', '/admin/api/categories', body));
    toast(c ? 'Category saved.' : 'Category created.');
    categoriesView();
  });
  refresh();
}

// ---- Notices -------------------------------------------------------------------

// noticesView sets which account events the operator is told about, and
// where: the admins' email, a webhook, and the person whose access ends.
async function noticesView() {
  const n = await api('GET', '/admin/api/notices');
  const box = (checked, label) => { const i = h('input', { type: 'checkbox', 'aria-label': label }); i.checked = checked; return i; };
  const joined = box(n.joined, 'A new account'), expiring = box(n.expiring, 'Access ends soon'), expired = box(n.expired, 'Access ended');
  const days = h('input', { type: 'number', min: '1', max: '60', value: String(n.days), 'aria-label': 'Days before' });
  const admins = box(n.admins, 'Email the admins'), person = box(n.person, 'Email the person');
  const webhook = h('input', { type: 'url', value: n.webhook, placeholder: 'https://…', autocomplete: 'off', spellcheck: 'false' });
  const noEmail = !n.managed ? 'Only with Overfin\'s own sign-in: with an identity provider, Overfin doesn\'t know people\'s addresses.'
    : !n.mail ? 'Email isn\'t set up yet (Server, Email).' : '';
  if (!n.managed) { admins.disabled = true; person.disabled = true; }
  const values = () => ({ joined: joined.checked, expiring: expiring.checked, expired: expired.checked, days: numberIn(days, 'Days before'),
    admins: admins.checked, webhook: webhook.value.trim(), person: person.checked });
  const st = status('');
  const save = h('button', { class: 'primary', type: 'button', onclick: () => busy(save, async () => {
    await api('PUT', '/admin/api/notices', values());
    toast('Notices saved.');
    noticesView();
  }, (err) => st.set(err.message, 'error')) }, 'Save');
  const test = h('button', { type: 'button', onclick: () => busy(test, async () => {
    st.set('Sending…');
    st.set((await api('POST', '/admin/api/notices/test', values())).summary, 'ok');
  }, (err) => st.set(err.message, 'error')) }, 'Send a test');
  const made = h('div', {});
  const key = h('button', { type: 'button', onclick: async () => {
    if (n.signed && !(await ask('Make a new key? The old one stops matching: whatever checks the signatures needs the new one.', { yes: 'Make a new key' }))) return;
    busy(key, async () => {
      const { secret: k } = await api('POST', '/admin/api/notices/secret', {});
      n.signed = true;
      fill(made, secret('The webhook\'s key. Copy it now: it won\'t be shown again.', k,
        'Each post carries X-Overfin-Signature: "sha256=" and the HMAC-SHA256 of its body with this key, in hex.'));
    });
  } }, n.signed ? 'Make a new key' : 'Sign the posts');
  show(pageHead('Notices', 'Overfin can tell you about accounts: one made, one whose access ends soon or has ended. Never what anyone watches. All off until you switch them on.'),
    card({ title: 'What to tell', badge: (n.joined || n.expiring || n.expired) ? badge('On', 'ok') : badge('Off', 'plain') }, [
      setting('A new account', 'Someone signed in for the first time, invited or not.', joined),
      setting('Access ends soon', 'An account with an end date (People) gets near it.', expiring),
      setting('Days before', 'How long before its access ends an account is noticed, and its person warned.', days, true),
      setting('Access ended', 'An account\'s end date has passed.', expired),
    ]),
    card({ title: 'Where', description: 'Email, a webhook, or both.' }, [
      setting('Email the admins', noEmail || 'Every admin gets them, in the server\'s default language.', admins),
      field('Webhook', webhook, 'Overfin posts JSON to this address: "event", "text" and "content" (the notice in words, as Discord and Slack read it), "account" ({id, name}), "expiresAt", "at". Leave empty for none.', true),
      setting('Signing', n.signed ? 'Posts are signed.' : 'Posts aren\'t signed: make a key if what receives them checks signatures.', key),
      made,
    ]),
    card({ title: 'Warning the person', description: 'Before their access ends, the person gets an email in their language, so they can ask for more time.' }, [
      setting('Email the person', noEmail || 'Sent once per end date, the days set above before it.', person),
    ], [st, test, save]));
}

// ---- Now playing ---------------------------------------------------------------

// Jellyfin's reasons for converting, in words (as the site's player says them).
const convertReasons = {
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
const methodNames = { direct: ['As is', 'ok'], remux: ['Repackaged', 'plain'], transcode: ['Converted', 'warn'] };

function clock(ms) {
  const s = Math.floor(ms / 1000), h = Math.floor(s / 3600), m = Math.floor(s / 60) % 60;
  return `${h ? `${h}:${String(m).padStart(2, '0')}` : m}:${String(s % 60).padStart(2, '0')}`;
}

// playingView shows the playbacks running now, refreshed while it's open,
// and stops one. Nothing of it is kept.
async function playingView() {
  const { items, transcodes, maxTranscodes } = await api('GET', '/admin/api/playing');
  if (location.hash !== '#/playing') return;
  const rows = items.map((pb) => {
    const [method, kind] = methodNames[pb.method] || [pb.method, 'plain'];
    const why = (pb.reasons || []).map((r) => convertReasons[r] || r).join('; ');
    const quality = [pb.height && `${pb.height}p`, pb.bitrateKbps && `${(pb.bitrateKbps / 1000).toFixed(1)} Mb/s`].filter(Boolean).join(', ');
    const stop = h('button', { type: 'button', class: 'small danger', onclick: async () => {
      if (!(await ask(`Stop ${pb.title || 'this playback'} for ${pb.account.name}? Their player says the server's administrator stopped it.`, { yes: 'Stop', danger: true }))) return;
      busy(stop, async () => {
        await api('POST', `/admin/api/playing/${pb.id}/stop`, {}).catch((err) => { if (!/ended/.test(err.message)) throw err; });
        toast('Playback stopped.');
        playingView();
      });
    } }, 'Stop');
    return h('tr', {},
      h('td', {}, h('div', { class: 'name' }, pb.account.name), h('div', { class: 'hint' }, [pb.profile, pb.device].filter(Boolean).join(' · '))),
      h('td', {}, h('div', {}, pb.title || 'Starting…'),
        h('div', { class: 'hint' }, `${pb.paused ? 'Paused at' : 'At'} ${clock(pb.positionMs)}${pb.durationMs ? ` of ${clock(pb.durationMs)}` : ''} · started ${ago(pb.startedAt)}`)),
      h('td', {}, badge(method, kind), quality && h('div', { class: 'hint' }, quality), why && h('div', { class: 'hint' }, why)),
      h('td', { class: 'actions' }, stop));
  });
  const limit = maxTranscodes ? ` of ${maxTranscodes} allowed` : '';
  show(pageHead('Now playing', 'What\'s playing on the server now, refreshed every few seconds. Nothing of it is kept: history stays each profile\'s own.'),
    card({ title: `${plural(items.length, 'playback')}`, description: `${plural(transcodes, 'conversion')}${limit} running.` },
      h('div', { class: 'table-wrap' }, h('table', {},
        h('thead', {}, h('tr', {}, h('th', {}, 'Who'), h('th', {}, 'What'), h('th', {}, 'How'), h('th', {}))),
        h('tbody', {}, rows.length ? rows : h('tr', {}, h('td', { colspan: '4', class: 'empty' }, 'Nothing is playing.')))))));
  // One refresh at a time, however the page was opened again.
  clearTimeout(playingTimer);
  playingTimer = setTimeout(() => { if (location.hash === '#/playing') playingView().catch(fail); }, document.hidden ? 30000 : 5000);
}
let playingTimer;

// ---- Identification ------------------------------------------------------------

// identificationView lists every film and series with the TMDB title it's
// taken for, those that may be wrong first and why. A title is fixed by
// picking the right one on TMDB, confirmed by keeping its own, or given
// back to Jellyfin's.
async function identificationView(selected = '') {
  const { items, suspects } = await api('GET', '/admin/api/identification');
  $('#main').classList.add('wide');
  const search = h('input', { type: 'search', placeholder: 'Search titles, folders, files', 'aria-label': 'Search' });
  const filter = h('select', { 'aria-label': 'Show' },
    h('option', { value: 'suspects' }, `To check (${suspects})`),
    h('option', { value: '' }, `All (${items.length})`),
    h('option', { value: 'fixed' }, 'Fixed here'));
  if (!suspects) filter.value = '';
  const tbody = h('tbody', {});
  const detail = h('aside', { class: 'detail' }, h('div', { class: 'placeholder' }, 'Select a title to check it.'));
  const titleOf = (t) => (t.tmdbName ? `${t.tmdbName}${t.tmdbYear ? ` (${t.tmdbYear})` : ''}` : t.name);

  const select = (t) => {
    selected = t.itemId;
    for (const tr of tbody.children) tr.classList.toggle('selected', tr.dataset.id === t.itemId);
    fill(detail, identificationDetail(t, () => identificationView(t.itemId)));
  };
  const draw = () => {
    const q = search.value.trim().toLowerCase();
    const shown = items.filter((t) => (filter.value !== 'suspects' || t.reasons.length) && (filter.value !== 'fixed' || t.overridden) &&
      (!q || [t.name, t.tmdbName, t.originalName, t.folderName, t.fileName, t.tmdb].some((v) => (v || '').toLowerCase().includes(q))));
    fill(tbody, shown.slice(0, 500).map((t) => h('tr', { 'data-id': t.itemId, onclick: () => select(t) },
      h('td', {}, h('div', { class: 'name' }, titleOf(t)), h('div', { class: 'hint' }, t.folderName || t.fileName || '')),
      h('td', { class: 'hide-narrow' }, t.kind === 'movie' ? 'Film' : 'Series'),
      h('td', {}, t.overridden ? badge('Fixed here', 'ok')
        : t.reasons.length ? badge(t.reasons.length === 1 ? reasonName(t.reasons[0].code) : `${t.reasons.length} doubts`, 'warn') : h('span', { class: 'hint nowrap' }, 'Looks right')))));
    if (!shown.length) tbody.append(h('tr', {}, h('td', { colspan: '3', class: 'empty' }, filter.value === 'suspects' ? 'Nothing to check: every title looks right.' : 'Nothing matches.')));
    if (shown.length > 500) tbody.append(h('tr', {}, h('td', { colspan: '3', class: 'empty' }, `${shown.length - 500} more: search to narrow.`)));
    const current = items.find((t) => t.itemId === selected);
    if (current) select(current);
  };
  search.addEventListener('input', draw);
  filter.addEventListener('change', draw);
  show(
    pageHead('Identification', 'Each film and series, as the TMDB title Jellyfin matched it to; those that may be wrong come first, with why. A fix is kept by Overfin: Jellyfin isn\'t changed, and its scans don\'t undo it.'),
    h('div', { class: 'split' },
      h('div', {}, h('div', { class: 'toolbar' }, search, filter),
        h('div', { class: 'table-wrap' }, h('table', {},
          h('thead', {}, h('tr', {}, h('th', {}, 'Title'), h('th', { class: 'hide-narrow' }, 'Kind'), h('th', {}, 'Check'))), tbody))),
      detail));
  draw();
}

// ---- Valuation ------------------------------------------------------------------

const criteria = {
  watchTime: ['Watch time', 'How much it\'s watched, in full views: two people watching half count as one watching all. Recent viewing counts more, and one account counts for three views at most.'],
  audience: ['Audience', 'How many accounts watched it, even a little.'],
  followed: ['Followed', 'For series: the season people are on and those after it, while they keep watching.'],
  favorites: ['Favorites', 'How many accounts marked it a favorite.'],
  watchlist: ['Watchlist', 'How many accounts keep it to watch later.'],
  requested: ['Requested', 'How many accounts asked for it.'],
  tastes: ['Tastes', 'How well it fits what people here like: what they watch (genres, series or films), and what they said when asked. What someone watches counts more and more over what they said.'],
  rating: ['Rating', 'TMDB\'s rating. One from a few votes counts for little.'],
  newRelease: ['New release', 'How recent the film or the season is: its release, or its first air date.'],
  recentlyAdded: ['Recently added', 'How recently it arrived here (a season: its latest episode), for about two months: what just came isn\'t the least valuable for not being watched yet.'],
  popularity: ['Popularity', 'TMDB\'s popularity: how much people look it up these days.'],
};
const criterionName = (c) => (criteria[c] || [c])[0];
const valuedName = (t) => {
  const name = t.name || 'Untitled';
  return /_s\d+$/.test(t.id) ? `${name} · ${t.season ? `Season ${t.season}` : 'Specials'}` : name;
};

// valuationView sets how much each criterion counts in the titles' values,
// with the most and least valuable titles updated as the sliders move.
async function valuationView() {
  const data = await api('GET', '/admin/api/valuation');
  $('#main').classList.add('wide');
  const weights = Object.fromEntries(data.criteria.map((c) => [c.name, c.weight]));
  const saved = { ...weights };
  const changed = () => data.criteria.some((c) => weights[c.name] !== saved[c.name]);
  const save = h('button', { class: 'primary', disabled: true }, 'Save');
  const reset = h('button', {}, 'Reset to defaults');
  const ask = h('input', { type: 'checkbox', checked: data.askTastes, 'aria-label': 'Ask people what they like' });
  ask.addEventListener('change', () => busy(null, async () => {
    await api('PUT', '/admin/api/valuation', { askTastes: ask.checked });
    toast(ask.checked ? 'People will be asked what they like.' : 'People won\'t be asked any more; what they watch still counts.');
    preview();
  }, (err) => { ask.checked = !ask.checked; toast(err.message, true); }));
  const share = h('input', { type: 'number', min: '0', max: '100', step: '1', value: String(data.discoveryShare), 'aria-label': 'Discovery share, in percent' });
  share.addEventListener('change', () => busy(null, async () => {
    const n = Math.round(numberIn(share, 'Discovery share', '0: the picks alone'));
    if (!(n >= 0 && n <= 100)) throw new Error('The discovery share goes from 0 to 100.');
    await api('PUT', '/admin/api/valuation', { discoveryShare: n });
    share.value = String(n);
    data.discoveryShare = n;
    toast(n ? `${n}% of the suggestions will come from trending and popular titles.` : 'Suggestions come from the picks alone.');
  }, (err) => { share.value = String(data.discoveryShare); toast(err.message, true); }));
  const state = status('');
  const most = h('tbody', {});
  const least = h('tbody', {});
  const found = h('tbody', {});
  const search = h('input', { type: 'search', placeholder: 'Find a title', 'aria-label': 'Find a title' });
  const detail = h('aside', { class: 'detail' }, h('div', { class: 'placeholder' }, 'Select a title to see why it has its value.'));
  let selected = '';

  const why = (t) => {
    selected = t.id;
    for (const tb of [most, least, found]) for (const tr of tb.children) tr.classList.toggle('selected', tr.dataset.id === t.id);
    const rows = data.criteria.filter((c) => c.name in t.parts).map((c) => h('tr', {},
      h('td', {}, criterionName(c.name)),
      h('td', { class: 'num' }, t.parts[c.name] > 0 ? `${Math.round(t.parts[c.name] * 100)} / 100` : 'none'),
      h('td', { class: 'num' }, weights[c.name] ? `× ${weights[c.name]}` : h('span', { class: 'hint' }, 'left out'))));
    fill(detail,
      h('div', { class: 'detail-head' }, h('div', {}, h('h2', {}, valuedName(t)),
        h('div', { class: 'hint' }, t.inLibrary ? 'In the libraries' : 'Not in the libraries'))),
      h('p', {}, h('strong', {}, `Value ${t.score.toFixed(1)}`), ' out of 100.'),
      h('p', { class: 'hint' }, 'Each criterion ranks the title among all the others, from 0 (no sign of it, or the lowest) to 100 (the highest); the value is their average, weighted by the sliders. What people do (watching, favorites, lists, requests, tastes) counts in the value but isn\'t shown title by title.'),
      h('div', { class: 'table-wrap' }, h('table', {},
        h('thead', {}, h('tr', {}, h('th', {}, 'Criterion'), h('th', {}, 'Rank'), h('th', {}, 'Weight'))),
        h('tbody', {}, rows))),
      data.accounts < 10 && h('p', { class: 'hint' }, `With ${data.accounts} account${data.accounts === 1 ? '' : 's'} counted, what people do is pulled toward the middle, so one person's habits don't decide.`));
  };
  const rows = (tbody, list, empty) => {
    fill(tbody, list.map((t) => h('tr', { 'data-id': t.id, class: t.id === selected ? 'selected' : '', onclick: () => why(t) },
      h('td', {}, h('div', { class: 'name' }, valuedName(t)), !t.inLibrary && h('div', { class: 'hint' }, 'Not in the libraries')),
      h('td', { class: 'num' }, t.score.toFixed(1)))));
    if (!list.length) tbody.append(h('tr', {}, h('td', { colspan: '2', class: 'empty' }, empty)));
  };
  let timer = 0;
  let asked = 0;
  const preview = () => {
    clearTimeout(timer);
    timer = setTimeout(async () => {
      const n = ++asked;
      const res = await busy(null, () => api('POST', '/admin/api/valuation/preview', { weights, query: search.value.trim() }));
      if (!res || n !== asked) return; // a newer one is on its way
      rows(most, res.most, 'No titles in the libraries yet.');
      rows(least, res.least, 'No titles in the libraries yet.');
      rows(found, res.found, search.value.trim() ? 'Nothing matches.' : 'Search any title, in the libraries or not.');
      const all = [...res.most, ...res.least, ...res.found];
      const current = all.find((t) => t.id === selected);
      if (current) why(current);
    }, 150);
  };
  const sliders = data.criteria.map((c) => {
    const out = h('output', {}, String(c.weight));
    const input = h('input', { type: 'range', min: '0', max: String(data.max), step: '1', value: String(c.weight), 'aria-label': criterionName(c.name) });
    input.addEventListener('input', () => {
      weights[c.name] = Number(input.value);
      out.textContent = input.value;
      save.disabled = !changed();
      state.set(changed() ? 'Not saved yet.' : '');
      preview();
    });
    input.set = (v) => { input.value = String(v); out.textContent = String(v); };
    return { c, input, row: setting(criterionName(c.name), (criteria[c.name] || [])[1], h('div', { class: 'weight' }, input, out)) };
  });
  save.addEventListener('click', () => busy(save, async () => {
    await api('PUT', '/admin/api/valuation', { weights });
    Object.assign(saved, weights);
    save.disabled = true;
    state.set('Saved.', 'ok');
  }));
  reset.addEventListener('click', () => busy(reset, async () => {
    const res = await api('PUT', '/admin/api/valuation', { reset: true });
    for (const { c, input } of sliders) {
      const w = res.criteria.find((x) => x.name === c.name).weight;
      weights[c.name] = saved[c.name] = w;
      input.set(w);
    }
    save.disabled = true;
    state.set('Back to the defaults.', 'ok');
    preview();
  }));
  search.addEventListener('input', preview);
  const list = (title, tbody) => h('div', { class: 'table-wrap' }, h('table', {},
    h('thead', {}, h('tr', {}, h('th', {}, title), h('th', {}, 'Value'))), tbody));
  show(
    pageHead('Valuation', 'How much each film and each season is worth to the people here, from 0 to 100: what they watch, mark, keep for later and ask for, and what TMDB says of it. Values never depend on files or their size, and say nothing of who did what. Apps you give a key, like Overtide, read them.'),
    h('div', { class: 'split' },
      h('div', {},
        card({ title: 'Criteria', description: `From 0 (left out) to ${data.max} (counts the most). ${plural(data.accounts, 'account')} used in the last six months count${data.accounts === 1 ? 's' : ''}; ${data.titles === 1 ? '1 film or season is' : `${data.titles} films and seasons are`} valued.` },
          h('div', {}, sliders.map((x) => x.row),
            setting('Ask people what they like', 'When they set up their account, and on their account page: series or films, genres, kids\' content. Off, Tastes come from what they watch alone.', ask)),
          [state, reset, save]),
        card({ title: 'Suggestions', description: 'What apps you give a key, like Overtide, are told to add: the titles the libraries lack, the most valuable first.' },
          h('div', {}, setting('Discovery share', 'The part of the suggestions taken from TMDB\'s trending and popular titles whatever their value, so people find new things and the titles they hear about. 0 turns it off.', h('div', { class: 'weight' }, share, h('span', {}, '%'))))),
        h('div', { class: 'columns' },
          h('section', {}, h('h2', {}, 'Most valuable'), list('In the libraries', most)),
          h('section', {}, h('h2', {}, 'Least valuable'), list('In the libraries', least))),
        h('section', {}, h('div', { class: 'toolbar' }, search), list('Title', found))),
      detail));
  preview();
}

const reasonNames = { 'no-tmdb': 'No TMDB title', 'tmdb-tag': 'Tagged', name: 'Name differs', year: 'Year differs', seasons: 'Seasons differ', runtime: 'Length differs' };
const reasonName = (code) => reasonNames[code] || code;

// identificationDetail is the selected title: its files, the TMDB title
// it's taken for, why it may be wrong, and TMDB searched for the right one.
function identificationDetail(t, reload) {
  const act = (button, method, body, done) => busy(button, async () => {
    await api(method, `/admin/api/identification/${t.itemId}`, body);
    toast(done);
    await reload();
  });
  const facts = [
    t.folderName && ['Folder', t.folderName],
    t.fileName && ['File', t.fileName],
    t.runtimeMin && ['Length', `${t.runtimeMin} min`],
    t.episodes && ['Episodes here', String(t.episodes)],
    ['TMDB', t.tmdb ? `${t.tmdb}${t.overridden && t.jellyfinTmdb !== t.tmdb ? ` (Jellyfin said ${t.jellyfinTmdb || 'none'})` : ''}` : 'none'],
    t.originalName && ['Original title', `${t.originalName}${t.language ? ` (${t.language})` : ''}`],
  ].filter(Boolean);

  // TMDB, searched from the folder's name by default.
  const guess = (t.folderName || t.fileName || t.name).replace(/\[[^\]]*\]|\{[^}]*\}/g, '').replace(/\.[a-z0-9]{2,4}$/i, '').replace(/[._]/g, ' ')
    .replace(/^(.+?)\s*\(?\b(?:19|20)\d{2}\b\)?\s*$/, '$1').trim();
  const query = h('input', { type: 'search', value: guess, 'aria-label': 'Search TMDB' });
  const results = h('ul', { class: 'list candidates' });
  const searchButton = h('button', { type: 'submit' }, 'Search');
  const find = (e) => {
    e?.preventDefault();
    busy(searchButton, async () => {
      fill(results, h('li', { class: 'hint' }, 'Searching…'));
      const { items } = await api('POST', '/admin/api/identification/search', { query: query.value, kind: t.kind });
      fill(results, items.map((c) => {
        const use = c.tmdb !== t.tmdb && h('button', { type: 'button', class: 'small', onclick: () => act(use, 'PUT', { tmdb: c.tmdb }, 'Title fixed.') }, 'Use this');
        return h('li', {},
          c.poster ? h('img', { src: c.poster, alt: '', loading: 'lazy' }) : h('span', { class: 'noposter' }),
          h('div', {},
            h('div', { class: 'name' }, `${c.name}${c.year ? ` (${c.year})` : ''}`),
            h('div', { class: 'hint' }, [c.originalName && c.originalName !== c.name ? c.originalName : '', c.language, `TMDB ${c.tmdb}`].filter(Boolean).join(' · ')),
            c.overview && h('div', { class: 'hint clamp' }, c.overview)),
          c.tmdb === t.tmdb ? badge('Current', 'info') : use);
      }));
      if (!items.length) results.append(h('li', { class: 'hint' }, 'TMDB found nothing.'));
    }, (err) => fill(results, h('li', { class: 'error' }, err.message)));
  };
  const right = t.reasons.length && t.tmdb && h('button', { type: 'button', onclick: () => act(right, 'PUT', { tmdb: t.tmdb }, 'Kept as it is.') }, 'It\'s right');
  const back = t.overridden && h('button', { type: 'button', onclick: () => act(back, 'DELETE', undefined, 'Back to Jellyfin\'s title.') }, 'Back to Jellyfin\'s');

  return [
    h('div', { class: 'detail-head' }, h('div', {},
      h('h2', {}, t.tmdbName ? `${t.tmdbName}${t.tmdbYear ? ` (${t.tmdbYear})` : ''}` : 'No TMDB title'),
      h('div', { class: 'hint' }, t.kind === 'movie' ? 'Film' : 'Series'))),
    t.overridden && badge('Fixed here', 'ok'),
    h('h3', {}, 'About'),
    h('dl', { class: 'facts' }, facts.map(([k, v]) => [h('dt', {}, k), h('dd', {}, v)])),
    t.reasons.length > 0 && h('div', { class: 'notice warn spaced' }, h('strong', {}, 'Why it may be wrong'),
      h('ul', {}, t.reasons.map((r) => h('li', {}, r.text)))),
    (right || back) && h('div', { class: 'actions-row' }, right, back),
    h('h3', {}, 'Find the right one on TMDB'),
    h('form', { onsubmit: find, class: 'toolbar' }, query, searchButton),
    results,
  ];
}

// ---- Modules -------------------------------------------------------------------

// moduleView opens a switched-on module's own tab: its panel.js renders into
// the page, with helpers for its admin routes.
async function moduleView(name, description) {
  const root = h('div', {});
  show(pageHead(featureName(name), description), root);
  await mountTab(name, root, {});
}

// mountTab renders a module's tab into root, with what else the page
// gives it.
async function mountTab(name, root, extra) {
  const tab = await import(`/admin/modules/${encodeURIComponent(name)}/panel.js`);
  await tab.default(root, {
    api: (method, path, body) => api(method, `/admin/api/modules/${encodeURIComponent(name)}${path}`, body),
    h,
    // The panel's own building blocks, so a tab looks like the rest.
    ui: { card, field, setting, badge, toast, busy, secret, status, fill, plural, day, ago, modal, formModal, ask },
    ...extra,
  });
}

// ---- Registration ---------------------------------------------------------------

// registrationView: who may make an account here, and how. With Overfin's
// own sign-in: the password rules, and invitations, which need email (the
// person's address is confirmed by a link). With an identity provider:
// invitations, and whether accounts are only by invitation.
async function registrationView() {
  const reg = await api('GET', '/admin/api/registration').catch(() => null);
  const root = h('div', {});
  const cards = [];
  if (reg) {
    const st = status('');
    const min = h('input', { type: 'number', min: '10', max: '64', value: reg.rules.minLength, 'aria-label': 'Minimum length' });
    const digit = h('input', { type: 'checkbox', 'aria-label': 'A digit' });
    const special = h('input', { type: 'checkbox', 'aria-label': 'A special character' });
    digit.checked = reg.rules.digit;
    special.checked = reg.rules.special;
    const save = h('button', { class: 'primary', type: 'button', onclick: () => busy(save, async () => {
      const out = await api('PUT', '/admin/api/registration', { rules: { minLength: numberIn(min, 'Minimum length'), digit: digit.checked, special: special.checked } });
      st.set('Saved: every new password follows them.', 'ok');
      Object.assign(reg, out);
    }, (err) => st.set(err.message, 'error')) }, 'Save');
    cards.push(card({ title: 'Passwords', description: 'What every new password must have: registering, a forgotten one, the account page, and those you set.' },
      [setting('Minimum length', 'From 10 to 64 characters.', min),
        setting('A digit', null, digit),
        setting('A special character', 'Anything but a letter or a digit; a space counts.', special)],
      [st, save]));
    if (!reg.mail) {
      cards.push(h('p', { class: 'notice' }, 'Invitations need email (Server → Email) and the site\'s address (site.url): the person confirms their address by a link sent there. Until then, accounts are made by you, on People.'));
    }
  } else {
    const state = await api('GET', '/admin/api/features');
    const f = state.features;
    const only = h('input', { type: 'checkbox', 'aria-label': 'Accounts only by invitation' });
    only.checked = f.modules?.invites?.required === 'on';
    const st = status('');
    const save = h('button', { class: 'primary', type: 'button', onclick: () => busy(save, async () => {
      f.modules = f.modules || {};
      f.modules.invites = { ...(f.modules.invites || {}), required: only.checked ? 'on' : 'off' };
      await api('PUT', '/admin/api/features', f);
      st.set('Saved.', 'ok');
    }, (err) => st.set(err.message, 'error')) }, 'Save');
    cards.push(card({ title: 'Who gets an account', description: 'People sign in at your identity provider; their account here is made at their first visit.' },
      setting('Accounts only by invitation', 'Without one, nobody gets an account; admins and people bringing a previous account still do.', only),
      [st, save]));
  }
  show(pageHead('Registration', reg
    ? 'Who can make an account here: with an invitation, choosing their password, then confirming their address by email.'
    : 'Invitations, and who gets an account.'), ...cards, root);
  if (!reg || reg.mail) {
    await mountTab('invites', root, {
      // Invitations by email, with Overfin's own sign-in.
      sendInvitation: reg ? (email, link) => api('POST', '/admin/api/registration/send', { email, link }) : null,
    });
  }
}

// ---- Layout --------------------------------------------------------------------

function show(...nodes) {
  fill($('#main'), nodes);
  window.scrollTo(0, 0);
}

const views = {
  server: { label: 'Server', icon: icons.server, view: () => serverView() },
  features: { label: 'Features & limits', icon: icons.sliders, view: () => featuresView() },
  branding: { label: 'Branding', icon: icons.palette, view: () => brandingView() },
  people: { label: 'People', icon: icons.people, view: () => peopleView() },
  registration: { label: 'Registration', icon: icons.mail, view: () => registrationView() },
  templates: { label: 'Templates', icon: icons.layers, view: () => templatesView() },
  libraries: { label: 'Libraries', icon: icons.folder, view: () => librariesView() },
  identification: { label: 'Identification', icon: icons.check, view: () => identificationView() },
  valuation: { label: 'Valuation', icon: icons.scale, view: () => valuationView() },
  categories: { label: 'Categories', icon: icons.grid, view: () => categoriesView() },
  apps: { label: 'Apps', icon: icons.key, view: () => appsView() },
  playing: { label: 'Now playing', icon: icons.play, view: () => playingView() },
  notices: { label: 'Notices', icon: icons.bell, view: () => noticesView() },
};
// Older addresses still lead somewhere.
const aliases = { settings: 'server', users: 'people', 'm/invites': 'registration' };
const moduleIcons = { invites: icons.mail, migration: icons.move };
let modules = [];

function link(tab, label, ic) {
  return h('a', { href: `#/${tab}`, 'data-tab': tab }, icon(ic), h('span', {}, label));
}

async function route() {
  let tab = location.hash.replace(/^#\/?/, '') || 'server';
  tab = aliases[tab] || tab;
  for (const a of document.querySelectorAll('nav a')) a.classList.toggle('active', a.dataset.tab === tab);
  $('#main').classList.remove('wide');
  fill($('#main'), h('p', { class: 'loading' }, 'Loading…'));
  if (tab.startsWith('m/')) {
    const name = decodeURIComponent(tab.slice(2));
    return moduleView(name, modules.find((m) => m.name === name)?.description);
  }
  return (views[tab] || views.server).view();
}

async function start() {
  const who = await api('GET', '/admin/api/session');
  $('#who').textContent = who.name ? `Signed in as ${who.name}` : 'Signed in';
  ({ items: modules } = await api('GET', '/admin/api/modules'));
  // Invitations live on People → Registration.
  const tabs = modules.filter((m) => m.hasTab && m.name !== 'invites');
  fill($('nav'),
    h('div', { class: 'group' }, 'Server'),
    link('server', views.server.label, views.server.icon),
    link('features', views.features.label, views.features.icon),
    link('branding', views.branding.label, views.branding.icon),
    h('div', { class: 'group' }, 'People'),
    link('people', views.people.label, views.people.icon),
    link('registration', views.registration.label, views.registration.icon),
    link('templates', views.templates.label, views.templates.icon),
    link('notices', views.notices.label, views.notices.icon),
    link('playing', views.playing.label, views.playing.icon),
    tabs.map((m) => link(`m/${encodeURIComponent(m.name)}`, featureName(m.name), moduleIcons[m.name] || icons.puzzle)),
    h('div', { class: 'group' }, 'Library'),
    link('libraries', views.libraries.label, views.libraries.icon),
    link('categories', views.categories.label, views.categories.icon),
    link('identification', views.identification.label, views.identification.icon),
    link('valuation', views.valuation.label, views.valuation.icon),
    link('apps', views.apps.label, views.apps.icon));
  $('nav').hidden = false;
  $('.me').hidden = false;
  window.addEventListener('hashchange', () => route().catch(fail));
  await route();
}

function fail(err) {
  if (err.message !== 'signing in') show(h('div', { class: 'notice warn' }, err.message));
}

$('#signout').addEventListener('click', async () => {
  await api('POST', '/admin/api/logout', {}).catch(() => {});
  $('nav').hidden = true;
  $('.me').hidden = true;
  show(h('div', { class: 'center' }, h('h1', {}, 'Signed out'), h('p', { class: 'muted' }, 'You left the control panel.'),
    h('p', {}, h('a', { class: 'button', href: '/admin/login?return=/admin/' }, 'Sign in again'))));
});

start().catch(fail);
