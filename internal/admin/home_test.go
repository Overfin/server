package admin

import (
	"context"
	"testing"
)

func TestHome(t *testing.T) {
	p, b, _, _ := signedInPanel(t)
	if rec := b.do("GET", "/admin/api/home", "", nil); rec.Code != 200 || rec.Body.String() != `{"missingTitles":false}`+"\n" {
		t.Errorf("before: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("PUT", "/admin/api/home", `{"missingTitles":true,"other":1}`, nil); rec.Code != 400 {
		t.Errorf("unknown field: %d", rec.Code)
	}
	if rec := b.do("PUT", "/admin/api/home", `{"missingTitles":true}`, nil); rec.Code != 200 || rec.Body.String() != `{"missingTitles":true}`+"\n" {
		t.Errorf("on: %d %s", rec.Code, rec.Body)
	}
	if on, err := p.Store.HomeMissing(context.Background()); err != nil || !on {
		t.Errorf("stored: %v %v", on, err)
	}
}
