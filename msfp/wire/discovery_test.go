package wire

import (
	"errors"
	"reflect"
	"testing"
)

// Base paths (vectors/v1/strict.json, bases): absolute, plain segments,
// ending in /api/v1/federation/, 256 bytes at most.
func TestBaseVectors(t *testing.T) {
	var v struct {
		Bases []struct {
			Text   string
			Valid  bool
			Reason string
		}
	}
	readVectors(t, "strict.json", &v)
	if len(v.Bases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range v.Bases {
		if err := CheckBase(c.Text); (err == nil) != c.Valid {
			t.Errorf("%q (%s): %v", c.Text, c.Reason, err)
		}
	}
}

func TestReadDiscovery(t *testing.T) {
	d, err := ReadDiscovery([]byte(`{"core":[1],"parts":{"catalog":[1],"video":[1]},"base":"/srv/media/api/v1/federation/"}`))
	if err != nil || d.Base != "/srv/media/api/v1/federation/" || Prefix(d.Base) != "/srv/media" ||
		!reflect.DeepEqual(d.Versions, Versions{Core: []int{1}, Parts: map[string][]int{"catalog": {1}, "video": {1}}}) {
		t.Errorf("%+v %v", d, err)
	}
	if d, err := ReadDiscovery([]byte(`{}`)); err != nil || d.Base != DefaultBase || Prefix(d.Base) != "" {
		t.Errorf("empty: %+v %v", d, err)
	}
	for _, body := range []string{``, `[]`, `"x"`, `{"base":5}`, `{"base":"/x/"}`, `{"base":null}`, `{"core":"1"}`,
		`{"parts":[1]}`, `{"base":"/srv/../api/v1/federation/"}`, `{"base":"/a"} x`} {
		if _, err := ReadDiscovery([]byte(body)); !errors.Is(err, ErrJSON) && !errors.Is(err, ErrBase) {
			t.Errorf("%q: %v", body, err)
		}
	}
}
