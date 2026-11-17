package msfp

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// hello is the shape of a hello message (section 9.2), for decoding
// the strict vectors into a struct as well as into a map.
type hello struct {
	State        string   `json:"state"`
	Certificates []string `json:"certificates,omitempty"`
}

// The json and integers cases of MSFP's strict vectors
// (testdata/v0-strict.json), through DecodeJSON: valid ones decode,
// invalid ones are refused.
func TestStrictJSONVectors(t *testing.T) {
	b, err := os.ReadFile("testdata/v0-strict.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		JSON []struct {
			Name, Text, Hex string
			Valid           bool
		}
		Integers []struct {
			Text  string
			Valid bool
		}
	}
	if err := json.Unmarshal(b, &v); err != nil || len(v.JSON) == 0 || len(v.Integers) == 0 {
		t.Fatal("no cases", err)
	}
	for _, c := range v.JSON {
		body := []byte(c.Text)
		if c.Hex != "" {
			body, _ = hex.DecodeString(c.Hex)
		}
		var msg hello
		if err := DecodeJSON(body, &msg); (err == nil) != c.Valid {
			t.Errorf("json %s: %v", c.Name, err)
		}
		var any map[string]any
		if err := DecodeJSON(body, &any); (err == nil) != c.Valid {
			t.Errorf("json %s, into a map: %v", c.Name, err)
		}
	}
	for _, c := range v.Integers {
		var n struct {
			N int64 `json:"n"`
		}
		if err := DecodeJSON([]byte(`{"n":`+c.Text+`}`), &n); (err == nil) != c.Valid {
			t.Errorf("integer %s: %v", c.Text, err)
		}
	}
}
