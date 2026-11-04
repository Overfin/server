package api_test

import (
	"io"
	"testing"

	"github.com/overfin/server/internal/store"
)

// Anyone may ask what this server is, and learns nothing else: no software
// version, nothing about what's behind it.
func TestInfo(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	get := func() string {
		res, err := srv.Client().Get(srv.URL + "/api/v1/info")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("status %d", res.StatusCode)
		}
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	if body := get(); body != `{"name":"Overfin","apiVersion":"1.0","features":[]}`+"\n" {
		t.Errorf("Overfin's: %s", body)
	}
	st.SetBranding(t.Context(), true, "Riverside")
	if body := get(); body != `{"name":"Riverside","apiVersion":"1.0","features":[]}`+"\n" {
		t.Errorf("the operator's name: %s", body)
	}
}
