package msfp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// marshal writes a body the engine sends: JSON, its members in the
// order of v's fields (the order of the specification's tables, as the
// vectors write them), and never a null (MSFP core section 2.3): a field
// that could be null is omitempty, or always set, and a null that slips
// through is an error, not a body.
func marshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return b, nil
		}
		if err != nil {
			return nil, err
		}
		if tok == nil {
			return nil, errors.New("msfp: a null in a body")
		}
	}
}
