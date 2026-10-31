# Your brand, and your emails

**Branding** (control panel, Server, Branding) puts your site's name, logo
and banner in place of Overfin's: on the site, its sign-in page and its
emails. They're kept as you upload them, PNG or JPEG (not SVG, which emails
can't show); a banner for light pages is optional, for the sign-in page on
light screens (the site's header is dark on every page). With your brand, the
site and emails say "Powered by Overfin", discreetly. The control panel
stays Overfin's. The same page's **Home page** card chooses whether the
site's home page also shows titles no library has, to be requested (off by
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
  with an address that has an account), `invite`, `test`, the notices to
  admins (`joined`, `expiring`, `expired`) and the one to someone whose
  access ends (`ending`) are each email's own (`.html` and `.txt`). The
  subject is the `.txt` file's `subject` block.
- `reset.html` replaces it in every language; `reset.fr.html`, in
  French only; `reset.fr-CA.html`, in Canadian French only. Your own
  templates' languages are yours to cover.
- Templates are Go templates, with `.Site`, `.Lang`, `.Subject`, `.Link`,
  `.Name` and `.Date` (a notice's account, and when its access ends),
  `.Custom` (your brand is on), `.Banner` (`cid:banner`), `.Overfin`
  (Overfin's small logo, `cid:overfin`, with your brand on), `.Light` and
  `.Dark` (the theme's colors, such as `index .Light "accent"`), and `t`
  for Overfin's sentences in the reader's language (`{{t "reset.intro"}}`).
- They're read for every email: no restart. One that fails is reported in
  the log, and Overfin's is used instead. The panel's Emails card says
  whose each one is, and sends you a sample.


---

[← Back to the README](../README.md)
