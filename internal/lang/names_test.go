package lang

import "testing"

// List: every language, each followed by its variants, in their names'
// order; the variants files tell apart named with their region.
func TestList(t *testing.T) {
	all := List()
	at := map[string]int{}
	for i, c := range all {
		if _, dup := at[c.Tag]; dup || c.Name == "" {
			t.Fatalf("%d: %+v", i, c)
		}
		at[c.Tag] = i
	}
	if len(all) != len(languages)+len(Choices)+len(variants) {
		t.Errorf("%d entries", len(all))
	}
	if !(at["es"] < at["es-ES"] && at["es-ES"] < at["es-419"] && at["es-419"] < at["es-MX"]) {
		t.Errorf("Spanish: %d %d %d %d", at["es"], at["es-ES"], at["es-419"], at["es-MX"])
	}
	if at["de"] > at["en"] || Name("es-419") != "Español (Latinoamérica)" {
		t.Errorf("order or names: %d %d %q", at["de"], at["en"], Name("es-419"))
	}
}
