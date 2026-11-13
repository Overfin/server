# Sign-in: identity providers and the control panel

## Signing in through your identity provider

Instead of Overfin's own sign-in, people can sign in through an OpenID
Connect provider (`identity.mode = "oidc"`), such as Authentik or Pocket
ID, or a reverse proxy's forward auth (`"forwardauth"`).

By hand: make a confidential OpenID client at your provider with the
redirect URL `https://media.example.org/api/v1/auth/callback`, allowed to
issue refresh tokens (usually the `offline_access` scope), then:

```toml
[identity]
mode = "oidc"
trusted_proxies = ["198.51.100.7"]   # your reverse proxy

[oidc]
issuer = "https://auth.example.org/application/o/site/"
client_id = "overfin"
redirect_url = "https://media.example.org/api/v1/auth/callback"
```

with the client secret in `OVERFIN_OIDC_CLIENT_SECRET`. Anyone who can log
in at your provider gets an account, unless you set
`identity.required_group`, or make invitations required.

With a required group or a panel admin group, let your provider send an ID
token with the groups claim on refresh too: Overfin takes a claim that's
missing to mean no groups, and without an ID token logins end within 12 hours.

**Leaving your identity provider for Overfin's own sign-in?** While it's
still configured (with its `[connector]`), run `overfin -import-people`
once: everyone it has, with an email, can then sign in with that email,
keeping their account, and the panel's admins stay admins. Then switch to
`identity.mode = "managed"`. Imported people have no password yet: they
choose one with "Forgot your password?", or you set one in the panel.

**Locked out?** `overfin -set-password you@example.org` asks for a new
password and ends that account's other logins (with Docker: `docker exec
-it overfin-backend /overfin-backend -set-password you@example.org`).

## Signing a TV in with a code

With Overfin's own sign-in, a screen that's hard to type on signs in from
another device. On the sign-in page, "Sign in with another device" shows an
address (`/link` on your site), a code of 8 letters and a QR code of that
address with the code in it. Someone signed in on a phone or a computer opens
it, sees what it will do, and confirms; the screen is then signed in with
their account, with a login of its own. A code lasts 10 minutes and works
once, and only the screen that asked for it can use it. There's nothing to
set up, and it's in [SECURITY.md](../SECURITY.md) as rules 116 to 118.

With an identity provider or forward auth, there are no codes for now.

## The control panel

The control panel is the backend's, on its own port (8081), whatever sign-in
the site uses, and answers only private addresses (your LAN, a VPN): never
the internet, and never through the gateway. It has its own login, a
username and a password made in the setup wizard, apart from the site's
accounts: an admin on the site, or a stolen site account, doesn't open it.
A forgotten password:

```sh
docker exec -it overfin-backend /overfin-backend -set-panel-password
```

For real names, add a `[connector]`. With Pocket ID (`kind = "pocketid"`,
`url = "https://auth.example.org"`), set `OVERFIN_CONNECTOR_TOKEN` to its
`STATIC_API_KEY`: Overfin then also sets up its own login client and the
admin group there, and invitation links lead to a one-person sign-up link
(Pocket ID needs `ALLOW_USER_SIGNUPS=withToken`). With Authentik
(`kind = "authentik"`), set it to an Authentik token of intent **API Token**
(not an app password), for a user allowed only to **view users**.


---

[← Back to the README](../README.md)
