# Your brand, and your emails

**Branding** (control panel, Server, Branding) puts your site's name, logo
and banner in place of Overfin's: on the site, its sign-in page and its
emails. Upload them as PNG or JPEG (not SVG, which emails can't show):
Overfin keeps them as PNG, no wider than they're shown (a logo 512 pixels,
a banner 1200); a banner for light pages is optional, for the sign-in page on
light screens (the site's header is dark on every page). With your brand, the
site and emails say "Powered by Overfin", discreetly. The control panel
stays Overfin's. The same page's **Home page** card chooses whether the
site's home page also shows titles no library has, to be wished for (off by
default).

**Emails** are Overfin's own, in each person's language (confirming an
address, choosing a new password, invitations, the panel's test), in HTML
and plain text, with your brand when it's on. To write your own, put
templates with the same names in `mail-templates/`, next to the database
(`/data/mail-templates/` with Docker): any file there replaces Overfin's,
and the rest stay Overfin's.
Overfin's are the place to start, at the release you run:
[internal/emails/templates](../internal/emails/templates).

- `layout.html` and `layout.txt` frame every email; `verify` (confirming
  an address), `reset` (a new password), `exists` (someone registered
  with an address that has an account), `invite`, `setup` (the link to
  choose a password, for someone an admin added), `test`, the notices to
  admins (`joined`, `expiring`, `expired`), the one to someone whose
  access ends (`ending`) and a federation pairing code sent to another
  server's operator (`code`) and the admins' email when another server
  asks to pair or to join a group (`request`) are each email's own (`.html`
  and `.txt`). The
  subject is the `.txt` file's `subject` block.
- `reset.html` replaces it in every language; `reset.fr.html`, in
  French only; `reset.fr-CA.html`, in Canadian French only. Your own
  templates' languages are yours to cover.
- Templates are Go templates, with `.Site`, `.Lang`, `.Subject`, `.Link`,
  `.Name` and `.Date` (a notice's account, and when its access ends; for
  `code`, when the code stops working), `.Invitation` (`joined`'s: the
  invitation the account came from, empty for none), `.Code` and `.Group`
  (`code`'s: the pairing code, and the group it invites to, empty for
  none), `.Asks` (`request`'s: `pair`, `join` or `member`, with `.Name` the
  server asking and `.Group` its group),
  `.Custom` (your brand is on), `.Banner` and `.Overfin` (the sizes, `.Width`
  and `.Height`, of the pictures `cid:banner` and `cid:overfin`; `.Banner` is
  empty for a brand without a banner), `.Light` and
  `.Dark` (the theme's colors, such as `index .Light "accent"`), and `t`
  for Overfin's sentences in the reader's language (`{{t "reset.intro"}}`).
- They're read for every email: no restart. One that fails is reported in
  the log, and Overfin's is used instead. The panel's Emails card says
  whose each one is, and sends you a sample.


---

[← Back to the README](../README.md)
