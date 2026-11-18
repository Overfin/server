package msfp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// Federation bodies are I-JSON (RFC 7493), read as MSFP core section 2.3 says,
// both ways: the requests peers send here and the answers they give.
// encoding/json alone is more lenient: it keeps the last of two members
// of the same name, replaces invalid UTF-8 and lone surrogates with
// U+FFFD, accepts any number a field can hold, and matches member names
// without regard to case. So a body is first read token by token, which
// refuses the first three, then given to encoding/json with only the
// members whose names are exactly a field's: "State" is an unknown member,
// ignored, never "state".
//
// null is never a valid value (core section 2.3). A request that holds
// one, anywhere, is refused (DecodeJSON). In an answer, a member of the
// wrong type fails the whole answer at its top, and only counts as absent,
// or skips its entry, inside it: DecodeAnswer refuses null at the top,
// and reads it as the member left out inside; DecodeEntry reads an entry
// of an answer's list, null anywhere as left out. Marshal writes bodies
// without any.

// ErrJSON is returned for a body that isn't I-JSON, or doesn't fit.
var ErrJSON = errors.New("federation: not a valid body")

// maxDepth bounds how deeply a body nests: MSFP's bodies are shallow.
const maxDepth = 64

// DecodeJSON reads a federation request, a JSON object, into v (a
// pointer), as MSFP core section 2.3 says: null refused in every member
// v knows, ignored, like any other value, in the members it doesn't.
func DecodeJSON(body []byte, v any) error {
	return decodeRequest(body, v, nil, nil)
}

// DecodeAnswer reads a federation answer into v, as DecodeJSON does a
// request, but for null inside it, which reads as the member left out:
// the caller checks each member's type, as each call's section says.
func DecodeAnswer(body []byte, v any) error {
	return decode(body, v, true, true)
}

// DecodeEntry reads an entry of an answer's list (a title, a library),
// raw, into v: null anywhere reads as the member left out.
func DecodeEntry(raw []byte, v any) error {
	return decode(raw, v, true, false)
}

// decodeRequest reads a request into v. null is never a valid value in
// it (core section 2.3), but only in the members v knows: an unknown
// member is ignored whatever it holds, null included. The members named
// ignored, at the top, are dropped first, whatever their value: a
// hello's knownKey and rotation, from a server that can be called
// (section 9.2). Those named lenient are v's
// own, refused when null themselves, but their reader judges what they
// hold: a hello's version lists ignore entries that aren't integers,
// null among them, and its parts the parts it doesn't know, whatever
// their value (section 10.2).
func decodeRequest(body []byte, v any, ignored, lenient []string) error {
	tree, err := strictTree(body, true)
	if err != nil {
		return err
	}
	obj, ok := tree.(map[string]any)
	if !ok {
		return ErrJSON
	}
	for _, name := range ignored {
		delete(obj, name)
	}
	known, ok := keepExact(obj, reflect.TypeOf(v)).(map[string]any)
	if !ok {
		known = obj // v reads itself: all of it counts
	}
	for name, value := range known {
		if value == nil || !slices.Contains(lenient, name) && holdsNull(value) {
			return ErrJSON
		}
	}
	return decodeTree(obj, v)
}

// holdsNull says whether v holds null anywhere.
func holdsNull(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case map[string]any:
		for _, value := range t {
			if holdsNull(value) {
				return true
			}
		}
	case []any:
		for _, value := range t {
			if holdsNull(value) {
				return true
			}
		}
	}
	return false
}

// decode reads body into v: with nulls, null is read as the member left
// out, except at the top with topNull refused.
func decode(body []byte, v any, nulls, topNull bool) error {
	tree, err := strictTree(body, nulls)
	if err != nil {
		return err
	}
	// Every MSFP body, request or answer, is an object (core section
	// 8.1): "null", a list or a string isn't one.
	obj, ok := tree.(map[string]any)
	if !ok {
		return ErrJSON
	}
	if topNull {
		for _, value := range obj {
			if value == nil {
				return ErrJSON
			}
		}
	}
	return decodeTree(obj, v)
}

// decodeTree gives a body already read (strictTree) to encoding/json,
// with only the members whose names are exactly v's fields'.
func decodeTree(tree map[string]any, v any) error {
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
// numbers as JSON has them, an integer field's checked by keepExact.
// Objects become map[string]any, numbers json.Number.
// With nulls, null is a value; otherwise it is refused.
func strictTree(body []byte, nulls bool) (any, error) {
	if bytes.HasPrefix(body, []byte("\xef\xbb\xbf")) || !utf8.Valid(body) || !escapesOK(body) {
		return nil, ErrJSON
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	v, err := readValue(dec, 0, nulls)
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

// isInteger says whether a JSON number is an integer as MSFP core
// section 2.3 says: no fraction, no exponent, not -0, within
// ±(2^53 − 1).
func isInteger(s string) bool {
	if !integerRe.MatchString(s) || s == "-0" {
		return false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "-"), 10, 64)
	return err == nil && n <= maxInteger
}

// notInteger stands, in a body read for a struct, for a number that isn't
// an integer where an integer field is: it can't be written, so the body
// is refused (a member of the wrong type, core section 2.3).
type notInteger struct{}

func (notInteger) MarshalJSON() ([]byte, error) {
	return nil, errors.New("federation: not an integer")
}

// readValue reads one value, token by token.
func readValue(dec *json.Decoder, depth int, nulls bool) (any, error) {
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
				if obj[key], err = readValue(dec, depth+1, nulls); err != nil {
					return nil, err
				}
			}
			_, err := dec.Token() // }
			return obj, err
		case '[':
			list := []any{}
			for dec.More() {
				v, err := readValue(dec, depth+1, nulls)
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
		// Any I-JSON number: whether it is an integer is the field's
		// question (keepExact), since a version list ignores entries
		// that aren't (section 10.2) and unknown members are ignored
		// whatever they hold.
		return t, nil
	case nil:
		if !nulls {
			return nil, ErrJSON // null is never a valid value
		}
		return nil, nil
	default: // string, bool
		return t, nil
	}
}

// Marshal writes v as a federation body: JSON, with no null anywhere
// (core section 2.3). A member that would be null (a nil list, map or
// pointer) is left out, as omitempty would; null inside a list is an
// error, since leaving it out would change the list.
func Marshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	tree, err = dropNulls(tree)
	if err != nil {
		return nil, err
	}
	return json.Marshal(tree)
}

// dropNulls removes the null members of every object in v.
func dropNulls(v any) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		for name, value := range t {
			if value == nil {
				delete(t, name)
				continue
			}
			kept, err := dropNulls(value)
			if err != nil {
				return nil, err
			}
			t[name] = kept
		}
	case []any:
		for i, value := range t {
			if value == nil {
				return nil, errors.New("federation: null in a list")
			}
			kept, err := dropNulls(value)
			if err != nil {
				return nil, err
			}
			t[i] = kept
		}
	}
	return v, nil
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
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if n, ok := v.(json.Number); ok && !isInteger(string(n)) {
			return notInteger{}
		}
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
