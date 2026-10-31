# Useful to know when running Overfin

- **`overfin -check`** tests the configuration and what it points to (the
  secret key, Jellyfin, the identity provider) and says in plain words
  what's wrong, changing nothing.
- **`SIGHUP`** reloads the configuration without dropping playbacks.
- **Slow requests**: API requests that take over 300 ms (streams aside) are
  logged (`a slow request`, with the route's pattern and the time only,
  never who asked or for what), to see where speed is lost.
- **`-recover-tokens`** gets you going again if the secret key is lost
  (PINs and logins are reset, history is kept; TMDB's token, Seerr's key
  and the SMTP password set in the panel are given again there, and
  pending registrations start over). It asks you to hand Overfin's Jellyfin
  over again, with an administrator's login, which nobody has once Overfin
  took it: Jellyfin's own "forgot password" gives one back, from its
  network. `POST /Users/ForgotPassword` with
  `{"EnteredUsername":"overfin-admin"}` writes a PIN to a
  `passwordreset*.json` file in Jellyfin's config folder;
  `POST /Users/ForgotPassword/Pin` with `{"Pin":"…"}` makes that PIN the
  password. `-recover-tokens` prints these steps too.
- **Locked out?** `overfin -set-password you@example.org` asks for a new
  password and ends that account's other logins (with Docker: `docker exec
  -it overfin /overfin -set-password you@example.org`).

---

[← Back to the README](../README.md)
