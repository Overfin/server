package federation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
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
	b, ok := decode64(rest, -1)
	fixed := ed25519.PublicKeySize + SecretSize + 8
	if !ok || len(b) <= fixed || binary.BigEndian.Uint64(b[fixed-8:fixed]) > maxInteger {
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

// checkAddress accepts a site's address in MSFP's one form (section 5.2):
// "http" or "https" in lower case, "://", a host, an optional port, and
// nothing after (no path, not even "/", no "?", "#" or "@"); at most
// maxAddress bytes. The host is a name (labels of a-z 0-9 -, 1 to 63
// characters, not starting or ending with -, at most 253 in all), an IPv4
// address as four numbers without leading zeros (a host whose last label
// is all digits must be one), or a bracketed IPv6 address as RFC 5952
// writes it. The port is 1 to 65535 without leading zeros, never the
// scheme's default. Plain http is accepted because every request and
// answer is signed either way, so a server can't be impersonated over it;
// the panel says what it exposes. One server, one way of writing its
// address.
func checkAddress(address string) error {
	bad := errors.New("federation: an address is http(s)://host[:port], with nothing after it")
	scheme, rest, ok := strings.Cut(address, "://")
	if len(address) > maxAddress || !ok || (scheme != "https" && scheme != "http") || rest == "" ||
		strings.ContainsAny(rest, "/?#@") {
		return bad
	}
	host, port, hasPort := rest, "", false
	if strings.HasPrefix(rest, "[") {
		end := strings.Index(rest, "]")
		if end < 0 {
			return bad
		}
		host = rest[1:end]
		if after := rest[end+1:]; after != "" {
			if port, hasPort = strings.CutPrefix(after, ":"); !hasPort {
				return bad
			}
		}
		if a, err := netip.ParseAddr(host); err != nil || !a.Is6() || a.Zone() != "" || a.String() != host {
			return bad
		}
	} else {
		host, port, hasPort = strings.Cut(rest, ":")
		labels := strings.Split(host, ".")
		if last := labels[len(labels)-1]; last != "" && strings.Trim(last, "0123456789") == "" {
			if a, err := netip.ParseAddr(host); err != nil || !a.Is4() || a.String() != host {
				return bad
			}
		} else {
			if len(host) > 253 {
				return bad
			}
			for _, l := range labels {
				if !labelRe.MatchString(l) {
					return bad
				}
			}
		}
	}
	if hasPort {
		n, err := strconv.Atoi(port)
		if !portRe.MatchString(port) || err != nil || n > 65535 ||
			(scheme == "https" && n == 443) || (scheme == "http" && n == 80) {
			return bad
		}
	}
	return nil
}

var (
	labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	portRe  = regexp.MustCompile(`^[1-9][0-9]{0,4}$`)
)

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
