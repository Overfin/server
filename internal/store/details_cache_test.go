package store

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The cache decodes as json.Unmarshal does; what one caller changes in
// its cast or seasons (the translation does) isn't the next caller's; new
// details are decoded again.
func TestDetailsCache(t *testing.T) {
	var c detailsCache
	raw := []byte(`{"genres":["Drame"],"cast":[{"name":"A","character":"Him"}],"seasons":[{"number":1,"name":"Saison 1"}],"runtime":50,"names":["X"]}`)
	var want, got TitleDetails
	json.Unmarshal(raw, &want)
	if err := c.decode("series/1", raw, &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("first: %+v, %v", got, err)
	}
	got.Cast[0].Character, got.Seasons[0].Name = "Lui", "Season 1"
	var again TitleDetails
	if c.decode("series/1", raw, &again); !reflect.DeepEqual(again, want) {
		t.Errorf("a caller's change kept: %+v", again)
	}
	var changed TitleDetails
	if c.decode("series/1", []byte(`{"runtime":51}`), &changed); changed.Runtime != 51 || changed.Cast != nil {
		t.Errorf("new details: %+v", changed)
	}
	if err := c.decode("series/1", []byte(`{`), &changed); err == nil {
		t.Error("bad JSON decoded")
	}
}
