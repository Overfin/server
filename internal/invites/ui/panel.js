// The invites module's tab: invitations, and the invite templates they're
// made from, on People → Registration. An invitation is for a number of
// people or any number, until a date or with no end; with Overfin's own
// sign-in, its link can go by email (panel.sendInvitation). Each list has
// its Edit and Delete buttons; forms open in a window (panel.ui.modal).
// Text from the server is only ever set with textContent.
export default async function render(root, panel) {
  const { h, api } = panel;
  const { card, field, badge, toast, busy, secret, fill, plural, modal, formModal, ask } = panel.ui;
  const [{ items: templates }, { items: invites }, { items: accountTemplates }] = await Promise.all([
    api('GET', '/templates'), api('GET', '/invites'), api('GET', '/account-templates')]);
  const reload = () => render(root, panel);
  const accountName = (id) => accountTemplates.find((t) => t.id === id)?.name || 'a deleted template';
  const days = (n) => plural(n, 'day');

  // Invitations.
  const status = (inv) => (inv.maxUses && inv.uses >= inv.maxUses ? ['Used', '']
    : inv.expiresAt && new Date(inv.expiresAt) <= new Date() ? ['Expired', 'err'] : ['Waiting', 'ok']);
  const uses = (n) => (n ? plural(n, 'use') : 'any number of uses');
  // The link, once made: shown once, in a window, to copy or send.
  const showLink = (out) => {
    const link = out.link.startsWith('/') ? location.origin.replace(/:\d+$/, '') + out.link : out.link;
    const body = [secret(`Send this link to ${out.invite.label || 'them'}, by email, a message, anything. It's shown only now: copy it before closing this window.`, link,
      out.link.startsWith('/') ? 'Check the address in front: it must be your site\'s.' : null)];
    if (panel.sendInvitation) {
      const to = h('input', { type: 'email', placeholder: 'name@example.org', 'aria-label': 'Email' });
      const send = h('button', { type: 'submit' }, 'Send it by email');
      body.push(h('form', { class: 'toolbar', onsubmit: (ev) => {
        ev.preventDefault();
        busy(send, async () => {
          const sent = await panel.sendInvitation(to.value.trim(), link);
          toast(sent.summary);
          to.value = '';
        });
      } }, field('Or send it to', to), send));
    }
    const close = modal({ title: 'The invitation\'s link' }, body, h('span', { class: 'grow' }),
      h('button', { type: 'button', class: 'primary', onclick: () => close() }, 'Done'));
  };
  const newInvite = () => {
    const forWhom = h('input', { maxlength: '64', placeholder: 'e.g. Bob' });
    const from = h('select', {}, templates.map((t) => h('option', { value: t.id }, t.name)));
    formModal({ title: 'New invitation', description: 'A link that lets someone set up an account here, shaped by an invite template.', saveLabel: 'Make the link' },
      h('div', { class: 'fields' }, field('For', forWhom, 'Who it\'s for, to find it again in the list.', true), field('From', from, null, true)),
      async () => {
        const out = await api('POST', '/invites', { template: from.value, label: forWhom.value.trim() });
        reload();
        // Once this window has closed: the link's, on top.
        setTimeout(() => showLink(out), 0);
      });
  };
  const inviteRows = invites.length ? invites.map((inv) => {
    const [text, kind] = status(inv);
    const remove = h('button', { type: 'button', class: 'small danger', onclick: async () => {
      if (!(await ask('Delete this invitation? Its link stops working; accounts made with it stay.', { yes: 'Delete', danger: true }))) return;
      busy(remove, async () => { await api('DELETE', `/invites/${inv.id}`); toast('Invitation deleted.'); reload(); });
    } }, 'Delete');
    return h('tr', {},
      h('td', {}, h('div', { class: 'name' }, inv.label || 'Anyone with the link'), h('div', { class: 'hint' }, inv.maxUses ? `${inv.uses} of ${uses(inv.maxUses)}` : `${inv.uses} used, ${uses(0)}`)),
      h('td', {}, badge(text, kind)),
      h('td', { class: 'hint' }, `${accountName(inv.accountTemplate)}, ${inv.accessDays ? days(inv.accessDays) : 'no end'}`),
      h('td', { class: 'hint' }, inv.expiresAt ? new Date(inv.expiresAt).toLocaleDateString() : 'No end'),
      h('td', { class: 'row-actions' }, remove));
  }) : [h('tr', {}, h('td', { colspan: '5', class: 'empty' }, 'No invitations yet.'))];

  // Invite templates: a form in a window, a new one when t is null.
  const editTemplate = (t) => {
    const name = h('input', { maxlength: '64', placeholder: 'e.g. Friends', value: t?.name || '', required: true });
    const account = h('select', {}, accountTemplates.map((a) => h('option', { value: a.id, selected: a.id === t?.accountTemplate },
      `${a.name} · ${plural(a.maxStreams, 'stream')}, ${plural(a.maxProfiles, 'profile')}`)));
    const access = h('input', { type: 'number', min: '0', max: '3650', value: String(t?.accessDays ?? 0) });
    const valid = h('input', { type: 'number', min: '0', max: '3650', value: String(t?.validDays ?? 7) });
    const maxUses = h('input', { type: 'number', min: '0', max: '100000', value: String(t?.maxUses ?? 1) });
    formModal({ title: t ? `Edit ${t.name}` : 'New invite template', saveLabel: t ? 'Save' : 'Create',
      description: 'What an invitation gives: an account template, how long access lasts, how long the link works and how many people can use it.' },
    h('div', { class: 'fields' },
      field('Name', name, null, true), field('Account template', account, 'What the account may do.', true),
      field('Access lasts', access, 'In days; 0: no end.'), field('Link works', valid, 'In days; 0: no end.'),
      field('Uses', maxUses, 'How many people; 0: any number.')),
    async () => {
      const body = { name: name.value.trim(), accountTemplate: account.value,
        accessDays: Number(access.value), validDays: Number(valid.value), maxUses: Number(maxUses.value) };
      await (t ? api('PUT', `/templates/${t.id}`, body) : api('POST', '/templates', body));
      toast(t ? 'Invite template saved.' : 'Invite template created.');
      reload();
    });
  };
  const tmplRows = templates.length ? templates.map((t) => {
    const remove = h('button', { type: 'button', class: 'small danger', onclick: async () => {
      if (!(await ask(`Delete the invite template ${t.name}? Invitations made from it stay as they are.`, { yes: 'Delete', danger: true }))) return;
      busy(remove, async () => { await api('DELETE', `/templates/${t.id}`); toast('Invite template deleted.'); reload(); });
    } }, 'Delete');
    return h('tr', {},
      h('td', {}, h('div', { class: 'name' }, t.name), h('div', { class: 'hint' }, `Account: ${accountName(t.accountTemplate)}`)),
      h('td', { class: 'hint' }, t.accessDays ? days(t.accessDays) : 'No end'),
      h('td', { class: 'hint' }, `${t.validDays ? days(t.validDays) : 'no end'}, ${uses(t.maxUses)}`),
      h('td', { class: 'row-actions' }, h('button', { type: 'button', class: 'small', onclick: () => editTemplate(t) }, 'Edit'), remove));
  }) : [h('tr', {}, h('td', { colspan: '4', class: 'empty' }, 'No invite templates yet.'))];

  fill(root,
    card({ title: 'Invitations', description: 'A link that lets someone set up an account here, shaped by an invite template.',
      badge: templates.length ? h('button', { type: 'button', class: 'primary', onclick: newInvite }, 'New invitation') : null }, [
      !templates.length && h('p', { class: 'notice' }, 'Create an invite template first, below.'),
      h('div', { class: 'table-wrap' }, h('table', {},
        h('thead', {}, h('tr', {}, h('th', {}, 'For'), h('th', {}, 'Status'), h('th', {}, 'Gives'), h('th', {}, 'Link works until'), h('th', {}))),
        h('tbody', {}, inviteRows))),
    ]),
    card({ title: 'Invite templates', description: 'What an invitation gives: an account template, how long access lasts, how long the link works and how many people can use it.',
      badge: h('button', { type: 'button', class: 'primary', onclick: () => editTemplate(null) }, 'New invite template') },
    h('div', { class: 'table-wrap' }, h('table', {},
      h('thead', {}, h('tr', {}, h('th', {}, 'Name'), h('th', {}, 'Access lasts'), h('th', {}, 'Link works'), h('th', {}))),
      h('tbody', {}, tmplRows)))));
}
