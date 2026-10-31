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

**Leaving your identity provider for Overfin's own sign-in?** While it's
still configured (with its `[connector]`), run `overfin -import-people`
once: everyone it has, with an email, can then sign in with that email,
keeping their account, and the panel's admins stay admins. Then switch to
`identity.mode = "managed"`. Imported people have no password yet: they
choose one with "Forgot your password?", or you set one in the panel.

**Locked out?** `overfin -set-password you@example.org` asks for a new
password and ends that account's other logins (with Docker: `docker exec
-it overfin /overfin -set-password you@example.org`).

## The control panel

With Overfin's own sign-in, it's at `/admin/` on the site, for admins, who
confirm it's them before going in. `[admin] private = true` keeps it to
private addresses (LAN, VPN); `[admin] listen = ":8081"` takes it off the
site entirely, onto a listener of its own for private addresses only
(publish that port on your LAN address only), where admins sign in at
`http://<that address>:8081/admin/`.

With an identity provider, it has a listener of its own instead, never on
the internet: publish its port on the server's `127.0.0.1` only, and reach
it through an SSH tunnel (`ssh -L 8081:localhost:8081 server`, then
`http://localhost:8081`), or on your LAN or VPN over HTTPS:

```toml
[admin]
listen = ":8081"
group = "overfin-admins"   # who may sign in, at your identity provider
redirect_url = "http://localhost:8081/admin/auth/callback"
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
