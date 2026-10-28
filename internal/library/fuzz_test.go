package library

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	imgs "github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/jellyfin"
)

// Whatever Jellyfin sends about an item, what comes out has well-formed
// IDs, image URLs the image endpoint takes, and valid UTF-8 text with no
// markup.
func FuzzDetail(f *testing.F) {
	f.Add([]byte(hostile))
	f.Add([]byte(`{"Id":"0123456789abcdef0123456789abcdef","Type":"Movie","Name":"A","People":[{"Name":"B","Id":"22222222222222222222222222222222","Type":"Éditeur"}]}`))
	f.Add([]byte(`{"Id":"0123456789abcdef0123456789abcdef","Type":"Audio","AlbumId":"x","ArtistItems":[{"Id":"1","Name":"n"}]}`))
	// More chapters than image IDs can number.
	chapters := strings.Repeat(`{"Name":"c","ImageTag":"`+tag+`"},`, 1001)
	f.Add([]byte(`{"Id":"` + jfID + `","Type":"Movie","Name":"A","Chapters":[` + strings.TrimSuffix(chapters, ",") + `]}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		var raw jellyfin.RawItem
		if json.Unmarshal(body, &raw) != nil {
			return
		}
		it, ok := Detail(&raw)
		if !ok {
			return
		}
		if _, ok := Unwrap(ItemPrefix, it.ID); !ok {
			t.Fatalf("id %q", it.ID)
		}
		out, err := json.Marshal(it)
		if err != nil {
			t.Fatal(err)
		}
		var walk func(v any)
		walk = func(v any) {
			switch v := v.(type) {
			case map[string]any:
				for k, x := range v {
					if s, ok := x.(string); ok && k == "url" {
						id, found := strings.CutPrefix(s, "/api/v1/images/")
						if _, err := imgs.Parse(id); !found || err != nil {
							t.Fatalf("image url %q", s)
						}
					}
					walk(x)
				}
			case []any:
				for _, x := range v {
					walk(x)
				}
			case string:
				if !utf8.ValidString(v) || strings.ContainsRune(v, utf8.RuneError) {
					t.Fatalf("text %q in %s", v, out)
				}
			}
		}
		var tree any
		json.Unmarshal(out, &tree)
		walk(tree)
	})
}
