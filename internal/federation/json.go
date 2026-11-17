package federation

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// Federation bodies are I-JSON (RFC 7493), read as MSFP section 2.1 says,
// both ways: the requests peers send here and the answers they give.
// encoding/json alone is more lenient: it keeps the last of two members
// of the same name, replaces invalid UTF-8 and lone surrogates with
// U+FFFD, accepts any number a field can hold, and matches member names
// without regard to case. So a body is first read token by token, which
// refuses the first three, then given to encoding/json with only the
// members whose names are exactly a field's: "State" is an unknown member,
// ignored, never "state".
//
// null is read as the member left out, as version 0 says a receiver does
// (Overfin itself used to send it for empty lists).

// ErrJSON is returned for a body that isn't I-JSON, or doesn't fit.
var ErrJSON = errors.New("federation: not a valid body")

// maxDepth bounds how deeply a body nests: MSFP's bodies are shallow.
const maxDepth = 64

// DecodeJSON reads a federation body into v (a pointer), as MSFP section
// 2.1 says.
func DecodeJSON(body []byte, v any) error {
	tree, err := strictTree(body)
	if err != nil {
		return err
	}
	exact, err := json.Marshal(keepExact(tree, reflect.TypeOf(v)))
	if err != nil {
		return ErrJSON
	}
	if err := json.Unmarshal(exact, v); err != nil {
		return ErrJSON
	}
	return nil
}

// strictTree reads a body as I-JSON: UTF-8 without a byte order mark; no
// surrogates (escaped or not) and no noncharacters; no object with the
// same member name twice, compared once their escapes are decoded;
// integers only, without fraction, exponent or -0, within ±(2^53 − 1).
// Objects become map[string]any, numbers json.Number.
func strictTree(body []byte) (any, error) {
	if bytes.HasPrefix(body, []byte("\xef\xbb\xbf")) || !utf8.Valid(body) || !escapesOK(body) {
		return nil, ErrJSON
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	v, err := readValue(dec, 0)
	if err != nil {
		return nil, ErrJSON
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, ErrJSON // something after the value
	}
	return v, nil
}

// escapesOK checks a body's characters and \u escapes: no surrogate
// unless an escaped pair makes one character of it, no noncharacter.
// Unescaped surrogates are invalid UTF-8, refused before. A backslash
// only ever starts an escape: outside a string, the body isn't JSON
// anyway.
func escapesOK(b []byte) bool {
	for i := 0; i < len(b); {
		switch c := b[i]; {
		case c == '\\':
			if i+1 >= len(b) {
				return false
			}
			if b[i+1] != 'u' {
				i += 2
				continue
			}
			r, ok := hex4(b[i+2:])
			if !ok {
				return false
			}
			i += 6
			switch {
			case r >= 0xdc00 && r <= 0xdfff:
				return false // a low surrogate alone
			case r >= 0xd800 && r <= 0xdbff:
				if i+6 > len(b) || b[i] != '\\' || b[i+1] != 'u' {
					return false
				}
				lo, ok := hex4(b[i+2:])
				if !ok || lo < 0xdc00 || lo > 0xdfff {
					return false
				}
				i += 6
				r = 0x10000 + (r-0xd800)<<10 + (lo - 0xdc00)
			}
			if noncharacter(r) {
				return false
			}
		case c < utf8.RuneSelf:
			i++
		default:
			r, n := utf8.DecodeRune(b[i:])
			if noncharacter(r) {
				return false
			}
			i += n
		}
	}
	return true
}

func hex4(b []byte) (rune, bool) {
	if len(b) < 4 {
		return 0, false
	}
	n, err := strconv.ParseUint(string(b[:4]), 16, 32)
	return rune(n), err == nil
}

// noncharacter: U+FDD0 to U+FDEF, and the last two code points of every
// plane.
func noncharacter(r rune) bool {
	return r >= 0xfdd0 && r <= 0xfdef || r&0xfffe == 0xfffe
}

// integerRe is a JSON integer: no fraction, no exponent, no leading zero.
var integerRe = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,15})$`)

// readValue reads one value, token by token.
func readValue(dec *json.Decoder, depth int) (any, error) {
	if depth > maxDepth {
		return nil, ErrJSON
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := map[string]any{}
			for dec.More() {
				name, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := name.(string)
				if _, dup := obj[key]; !ok || dup {
					return nil, ErrJSON // the same member twice
				}
				if obj[key], err = readValue(dec, depth+1); err != nil {
					return nil, err
				}
			}
			_, err := dec.Token() // }
			return obj, err
		case '[':
			list := []any{}
			for dec.More() {
				v, err := readValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			_, err := dec.Token() // ]
			return list, err
		}
		return nil, ErrJSON
	case json.Number:
		s := string(t)
		if !integerRe.MatchString(s) || s == "-0" {
			return nil, ErrJSON
		}
		n, err := strconv.ParseInt(strings.TrimPrefix(s, "-"), 10, 64)
		if err != nil || n > maxInteger {
			return nil, ErrJSON
		}
		return t, nil
	default: // string, bool, nil
		return t, nil
	}
}

var unmarshaler = reflect.TypeFor[json.Unmarshaler]()

// keepExact keeps, of each object read for a struct, only the members
// whose names are exactly one of its fields' (the others are unknown
// members, ignored anyway), and goes on into the fields, lists and maps
// the type holds.
func keepExact(v any, t reflect.Type) any {
	if t == nil {
		return v
	}
	for t.Kind() == reflect.Pointer {
		if t.Implements(unmarshaler) {
			return v
		}
		t = t.Elem()
	}
	if reflect.PointerTo(t).Implements(unmarshaler) {
		return v // reads itself
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return v
		}
		fields := fieldsOf(t)
		out := make(map[string]any, len(obj))
		for name, value := range obj {
			if ft, ok := fields[name]; ok {
				out[name] = keepExact(value, ft)
			}
		}
		return out
	case reflect.Map:
		obj, ok := v.(map[string]any)
		if !ok {
			return v
		}
		out := make(map[string]any, len(obj))
		for name, value := range obj {
			out[name] = keepExact(value, t.Elem())
		}
		return out
	case reflect.Slice, reflect.Array:
		list, ok := v.([]any)
		if !ok {
			return v
		}
		out := make([]any, len(list))
		for i, value := range list {
			out[i] = keepExact(value, t.Elem())
		}
		return out
	}
	return v
}

// fields holds each struct type's JSON member names, as encoding/json
// reads them, with their types.
var fields sync.Map // reflect.Type → map[string]reflect.Type

func fieldsOf(t reflect.Type) map[string]reflect.Type {
	if m, ok := fields.Load(t); ok {
		return m.(map[string]reflect.Type)
	}
	m := map[string]reflect.Type{}
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		ft := f.Type
		if f.Anonymous && name == "" {
			// An embedded struct's fields count as the outer one's.
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				for n, t := range fieldsOf(ft) {
					if _, ok := m[n]; !ok {
						m[n] = t
					}
				}
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		m[name] = ft
	}
	fields.Store(t, m)
	return m
}
