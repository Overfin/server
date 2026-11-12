package federation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// A pairing code is what an operator sends another, however they like, to
// pair their two servers. It carries everything the other server needs to
// reach this one and to know it's really this one:
//
//   - the address of this server's site, where its federation API answers;
//   - this server's public key, so the other checks every answer against it
//     (and its operator can compare the fingerprint, to rule out a code
//     someone changed on the way);
//   - a secret, used once, which proves the server redeeming the code is
//     the one it was sent to;
//   - when the code stops working (a day after it was made).
//
// The code is text, one line, easy to paste: "overfin-pair-1." then the
// rest in URL-safe base64.
type Code struct {
	Address string
	Key     ed25519.PublicKey
	Secret  []byte
	Expires time.Time
}

// codePrefix names the format and its version, so a code from a later
// Overfin is told apart instead of misread.
const codePrefix = "overfin-pair-1."

// SecretSize is the secret's length: 32 random bytes, never guessed.
const SecretSize = 32

// CodeLifetime is how long a pairing code works.
const CodeLifetime = 24 * time.Hour

// maxAddress caps a code's address, so a code can't be made huge.
const maxAddress = 512

// ErrCode is returned for any text that isn't a valid pairing code.
var ErrCode = errors.New("federation: not a valid pairing code")

// NewCode makes a pairing code for this server at address, with a new
// secret, valid from now for CodeLifetime.
func NewCode(address string, public ed25519.PublicKey, now time.Time) (Code, error) {
	if err := checkAddress(address); err != nil {
		return Code{}, err
	}
	secret := make([]byte, SecretSize)
	rand.Read(secret)
	return Code{Address: address, Key: public, Secret: secret, Expires: now.Add(CodeLifetime).Truncate(time.Second)}, nil
}

// String is the code as text, to show and copy.
//
// Its bytes: the public key (32), the secret (32), when it expires (8, Unix
// seconds, big-endian), then the address.
func (c Code) String() string {
	b := make([]byte, 0, ed25519.PublicKeySize+SecretSize+8+len(c.Address))
	b = append(b, c.Key...)
	b = append(b, c.Secret...)
	b = binary.BigEndian.AppendUint64(b, uint64(c.Expires.Unix()))
	b = append(b, c.Address...)
	return codePrefix + base64.RawURLEncoding.EncodeToString(b)
}

// ParseCode reads a pairing code pasted by an operator. Spaces and line
// breaks around it (or inside it, from an email) are ignored. It doesn't
// check whether the code has expired: the server that made it does, and its
// answer says so.
func ParseCode(text string) (Code, error) {
	text = strings.Join(strings.Fields(text), "")
	rest, ok := strings.CutPrefix(text, codePrefix)
	if !ok || len(rest) > 2*(maxAddress+ed25519.PublicKeySize+SecretSize+8) {
		return Code{}, ErrCode
	}
	b, err := base64.RawURLEncoding.DecodeString(rest)
	fixed := ed25519.PublicKeySize + SecretSize + 8
	if err != nil || len(b) <= fixed {
		return Code{}, ErrCode
	}
	c := Code{
		Key:     ed25519.PublicKey(append([]byte(nil), b[:ed25519.PublicKeySize]...)),
		Secret:  append([]byte(nil), b[ed25519.PublicKeySize:ed25519.PublicKeySize+SecretSize]...),
		Expires: time.Unix(int64(binary.BigEndian.Uint64(b[fixed-8:fixed])), 0),
		Address: string(b[fixed:]),
	}
	if checkAddress(c.Address) != nil {
		return Code{}, ErrCode
	}
	return c, nil
}

// checkAddress accepts a site's address: http or https, a host, nothing
// else (no user, no query, no fragment), and short. Plain http is accepted
// because every request and answer is signed either way, so a server can't
// be impersonated over it; the panel says what it exposes. The host is a
// plain name, IPv4 or bracketed IPv6 address, with an optional port, in
// lower case, without its scheme's default port: one server, one way of
// writing its address.
func checkAddress(address string) error {
	u, err := url.Parse(address)
	if err != nil || len(address) > maxAddress || (u.Scheme != "https" && u.Scheme != "http") ||
		u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" ||
		(u.Path != "" && u.Path != "/") || !hostRe.MatchString(u.Host) || u.Host != strings.ToLower(u.Host) ||
		(u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		return errors.New("federation: an address is http(s)://host, with nothing after it")
	}
	return nil
}

// hostRe is a host as an address may name it: a name or an IPv4 address
// (letters, digits, dots, hyphens), or a bracketed IPv6 address, with an
// optional port.
var hostRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*|\[[0-9a-f:.]+\])(:[0-9]{1,5})?$`)

// Origin is an address as browsers name its origin: the host in lower case,
// without its scheme's default port, nor a trailing slash; "" for one
// that doesn't parse.
func Origin(address string) string {
	u, err := url.Parse(address)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Host)
	if (u.Scheme == "https" && strings.HasSuffix(host, ":443")) || (u.Scheme == "http" && strings.HasSuffix(host, ":80")) {
		host = host[:strings.LastIndex(host, ":")]
	}
	return strings.ToLower(u.Scheme) + "://" + host + strings.TrimSuffix(u.EscapedPath(), "/")
}
