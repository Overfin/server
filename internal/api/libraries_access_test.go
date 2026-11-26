package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/overfin/server/internal/store"
)

// The libraries a profile sees: all of this server's and those taken from
// paired servers; a profile limited to some libraries sees those only,
// and never a paired server's (rule 42).
func TestLibrariesPerProfile(t *testing.T) {
	x := pairForPlay(t)
	ctx := context.Background()
	films := fmt.Sprintf("%032x", 0xf1)
	x.stA.SetLibraries(ctx, []store.Library{{ID: films, Item: []byte(`{"Id":"` + films + `","Name":"Films","Type":"CollectionFolder","CollectionType":"movies"}`)},
		{ID: fmt.Sprintf("%032x", 0xf2), Item: []byte(`{"Id":"` + fmt.Sprintf("%032x", 0xf2) + `","Name":"Songs","Type":"CollectionFolder","CollectionType":"music"}`)}})
	names := func() []string {
		t.Helper()
		code, body := getAs(t, x.srvA, subject, "/api/v1/libraries")
		if code != 200 {
			t.Fatalf("libraries: %d %s", code, body)
		}
		var out struct{ Items []struct{ Name string } }
		json.Unmarshal([]byte(body), &out)
		var n []string
		for _, l := range out.Items {
			n = append(n, l.Name)
		}
		slices.Sort(n)
		return n
	}
	// A paired server's library looks like one of this server's (its name
	// says whose).
	if got := names(); !slices.Equal(got, []string{"Films", "Films (B)", "Songs"}) {
		t.Fatalf("the owner: %v", got)
	}
	limitedProfile(t, x.srvA, films)
	if got := names(); !slices.Equal(got, []string{"Films"}) {
		t.Errorf("a profile limited to Films: %v", got)
	}
	if code, _ := getAs(t, x.srvA, subject, "/api/v1/libraries?x=1"); code != 400 {
		t.Errorf("an unknown parameter: %d", code)
	}
}
