package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/overfin/server/internal/store"
)

// Categories in the panel: made, checked, previewed, ordered, deleted; the
// libraries taken out of the site's menu.
func TestCategoriesPanel(t *testing.T) {
	ctx := context.Background()
	_, b, db, _ := signedInPanel(t)
	// A new server, its library not read yet: every list is a list, as the
	// panel's editor reads them.
	if rec := b.do("GET", "/admin/api/categories", "", nil); rec.Code != 200 || strings.Contains(rec.Body.String(), "null") {
		t.Errorf("empty server: %d %s", rec.Code, rec.Body)
	}
	db.PutCatalog(ctx, []store.CatalogItem{
		{ID: strings.Repeat("0", 31) + "a", Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "1"},
		{ID: strings.Repeat("0", 31) + "b", Kind: "series", Name: "Bluey", SortName: "bluey", TMDB: "2"},
	}, 1)

	for _, bad := range []string{
		`{"names":{},"rules":{}}`,
		`{"names":{"en":"<b>x</b>"},"rules":{}}`,
		`{"names":{"en":"X"},"rules":{"kinds":["music"]}}`,
		`{"names":{"en":"X"},"rules":{"except":["cat_aaaaaaaaaaaaaaaaaaaaaaaaaa"]}}`,
		`{"names":{"en":"X"},"rules":{"maxAge":30}}`,
		`{"names":{"en":"X"},"rules":{},"x":1}`,
	} {
		if rec := b.do("POST", "/admin/api/categories", bad, nil); rec.Code != 400 {
			t.Errorf("%s: %d %s", bad, rec.Code, rec.Body)
		}
	}
	make := func(body string) string {
		rec := b.do("POST", "/admin/api/categories", body, nil)
		var out struct{ ID string }
		json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != 201 {
			t.Fatalf("make: %d %s", rec.Code, rec.Body)
		}
		return out.ID
	}
	films := make(`{"names":{"en":"Films","fr":"Films"},"rules":{"kinds":["movie"]},"home":true}`)
	series := make(`{"names":{"en":"Series"},"rules":{"kinds":["series"]}}`)

	rec := b.do("POST", "/admin/api/categories/preview", `{"rules":{"kinds":["series"]}}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"count":1`) || !strings.Contains(rec.Body.String(), `"sample":["Bluey"]`) {
		t.Errorf("preview: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("PUT", "/admin/api/categories/"+series, `{"names":{"en":"Shows"},"rules":{"except":["`+series+`"]}}`, nil); rec.Code != 400 {
		t.Errorf("leaving itself out: %d", rec.Code)
	}
	if rec := b.do("PUT", "/admin/api/categories/"+series, `{"names":{"en":"Shows"},"rules":{"except":["`+films+`"]}}`, nil); rec.Code != 204 {
		t.Errorf("change: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("PUT", "/admin/api/categories/order", `{"ids":["`+series+`","`+films+`"]}`, nil); rec.Code != 204 {
		t.Errorf("order: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("PUT", "/admin/api/categories/order", `{"ids":["`+series+`"]}`, nil); rec.Code != 400 {
		t.Errorf("partial order: %d", rec.Code)
	}
	if rec := b.do("PUT", "/admin/api/categories/libraries", `{"shown":false}`, nil); rec.Code != 204 {
		t.Errorf("libraries: %d", rec.Code)
	}
	body := b.do("GET", "/admin/api/categories", "", nil).Body.String()
	var list struct {
		Items []struct {
			ID    string
			Names map[string]string
			Count int
		}
		LibrariesShown bool
	}
	json.Unmarshal([]byte(body), &list)
	if len(list.Items) != 2 || list.Items[0].ID != series || list.Items[0].Names["en"] != "Shows" || list.Items[0].Count != 1 ||
		list.Items[1].Count != 1 || list.LibrariesShown {
		t.Errorf("list: %s", body)
	}
	if rec := b.do("DELETE", "/admin/api/categories/"+films, "", nil); rec.Code != 204 {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec := b.do("DELETE", "/admin/api/categories/"+films, "", nil); rec.Code != 404 {
		t.Errorf("delete again: %d", rec.Code)
	}
}
