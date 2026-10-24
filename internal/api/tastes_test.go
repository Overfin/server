package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// What the account likes: asked only when the operator chose to, the owner
// profile's to answer, genres among those Overfin knows; saying nothing
// still records it was asked; /me tells the site whether to ask, and the
// account's export includes the answers.
func TestTastes(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	ctx := context.Background()
	if err := st.PutTitles(ctx, []store.Title{{Kind: "movie", TMDB: "1", Name: "Drama"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "1", Name: "Drama",
		Genres: []store.TitleGenre{{ID: 18, Name: "Drama"}, {ID: 35, Name: "Comedy"}}}, []byte(`{}`), time.Now()); err != nil {
		t.Fatal(err)
	}

	// Not asked: nothing to see, nothing to say.
	if _, body := getAs(t, srv, subject, "/api/v1/me"); !strings.Contains(body, `"tastes":{"asked":false,"answered":false}`) {
		t.Errorf("me, not asked: %s", body)
	}
	if code, _ := getAs(t, srv, subject, "/api/v1/me/tastes"); code != 404 {
		t.Errorf("GET, not asked: %d", code)
	}

	if err := st.SetTastesAsked(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, body := getAs(t, srv, subject, "/api/v1/me"); !strings.Contains(body, `"tastes":{"asked":true,"answered":false}`) {
		t.Errorf("me, asked: %s", body)
	}
	code, body := getAs(t, srv, subject, "/api/v1/me/tastes")
	if code != 200 || !strings.Contains(body, `"answered":false`) || !strings.Contains(body, `{"id":35,"name":"Comedy"}`) ||
		!strings.Contains(body, `"kidsProfile":false`) {
		t.Fatalf("GET: %d %s", code, body)
	}
	for _, bad := range []string{`{"prefer":"anime"}`, `{"genres":[99]}`, `{"other":1}`} {
		if code, _ := put(t, srv, "PUT", "/api/v1/me/tastes", bad); code != 400 {
			t.Errorf("PUT %s: %d", bad, code)
		}
	}
	if code, body := put(t, srv, "PUT", "/api/v1/me/tastes", `{"prefer":"series","genres":[35,35],"kids":true}`); code != 200 ||
		!strings.Contains(body, `"prefer":"series","genres":[35],"kids":true`) {
		t.Fatalf("PUT: %d %s", code, body)
	}
	if _, body := getAs(t, srv, subject, "/api/v1/me"); !strings.Contains(body, `"answered":true`) {
		t.Errorf("me, answered: %s", body)
	}

	// Saying nothing: asked, answered, nothing kept.
	if code, body := put(t, srv, "PUT", "/api/v1/me/tastes", `{}`); code != 200 || !strings.Contains(body, `"genres":[]`) ||
		strings.Contains(body, `"prefer"`) {
		t.Errorf("PUT nothing: %d %s", code, body)
	}

	// Another profile can't see or change them.
	_, body = post(t, srv, "/api/v1/profiles", `{"name":"Partner"}`)
	partner := between(body, `"id":"`, `"`)
	post(t, srv, "/api/v1/profiles/"+partner+"/activate", `{}`)
	if code, _ := getAs(t, srv, subject, "/api/v1/me/tastes"); code != 403 {
		t.Errorf("another profile reads them: %d", code)
	}
	if code, _ := put(t, srv, "PUT", "/api/v1/me/tastes", `{"prefer":"movies"}`); code != 403 {
		t.Errorf("another profile changes them: %d", code)
	}
}
