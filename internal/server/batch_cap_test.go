package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An answer larger than a batch holds is left out, its status saying so;
// the others come whole.
func TestBatchAnswerCapped(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/huge" {
			w.Write([]byte(`"` + strings.Repeat("x", maxBatchAnswer) + `"`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/batch", strings.NewReader(`{"requests":["/api/v1/huge","/api/v1/small"]}`))
	batch(api).ServeHTTP(rec, req)
	var out struct {
		Responses []struct {
			Status int
			Body   json.RawMessage
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Responses) != 2 {
		t.Fatalf("%v %s", err, rec.Body.String()[:min(200, rec.Body.Len())])
	}
	if r := out.Responses[0]; r.Status != http.StatusInsufficientStorage || len(r.Body) != 0 {
		t.Errorf("huge: %d, %d bytes", r.Status, len(r.Body))
	}
	if r := out.Responses[1]; r.Status != 200 || string(r.Body) != `{"ok":true}` {
		t.Errorf("small: %d %s", r.Status, r.Body)
	}
}
