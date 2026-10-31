# Notices

Overfin can tell you about accounts: one made, one whose access ends soon,
one whose access ended. Never what anyone watches. All off until you
switch them on in the control panel (People, Notices): tick the events,
how many days before an end date to tell (7 by default), and where they
go: the admins' email, a webhook, or both. "Email the person" also warns
someone, in their language, before their access ends. Emails need
Overfin's own sign-in and an email server (Server, Email). "Send a test"
sends one where the page says, saved or not. End dates are checked every
hour; a notice about one is sent once per end date, and one that fails is
logged, not sent again.

The webhook gets a JSON `POST` (no redirects followed, 10 seconds at most),
with the event in an `X-Overfin-Event` header too:

```json
{"event": "account.expiring",
 "content": "…", "text": "…", "allowed_mentions": {"parse": []},
 "account": {"id": "acct_…", "name": "…"},
 "expiresAt": "2026-11-30T00:00:00Z", "at": "2026-11-23T14:00:00Z"}
```

`event` is `account.joined`, `account.expiring`, `account.expired`, or
`test` (with no `account`). `content` and `text` are the notice in words,
what Discord's and Slack's webhooks show, so their webhook addresses work
as they are; `allowed_mentions` keeps Discord from pinging anyone.
`account.name` is the label you gave the account, else the person's email
(with Overfin's own sign-in), else its owner profile's name. `expiresAt`
is there when the account has an end date.

**Signing**: "Sign the posts" makes a key, shown once. Each post then
carries `X-Overfin-Signature: sha256=` and the HMAC-SHA256 of the body, in
hex, with that key (the bytes its hex stands for). Check it against the
body exactly as received. In a shell, with the body in `body.json`:

```sh
KEY=…   # the key the panel showed
echo "sha256=$(openssl dgst -sha256 -mac HMAC -macopt hexkey:$KEY < body.json | sed 's/.*= //')"
```

In Go:

```go
func signed(body []byte, header, key string) bool {
	k, err := hex.DecodeString(key)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, k)
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(header), []byte(want))
}
```

"Make a new key" replaces it: the old one stops matching at once.


---

[← Back to the README](../README.md)
