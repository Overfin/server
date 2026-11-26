package connector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// pocketFake is Pocket ID's client, secrets and groups, kept between calls.
type pocketFake struct {
	mu      sync.Mutex
	client  map[string]any // nil until made
	secrets []string       // prefixes of the active secrets
	groups  []string
	calls   []string
}

func (f *pocketFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != http.MethodGet {
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	}
	var body map[string]any
	b, _ := io.ReadAll(r.Body)
	json.Unmarshal(b, &body)
	switch {
	case r.URL.Path == "/api/oidc/clients/overfin" && r.Method == http.MethodGet:
		if f.client == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(f.client)
	case r.URL.Path == "/api/oidc/clients" && r.Method == http.MethodPost:
		f.client = body
		f.secrets = append(f.secrets, "generat")
		io.WriteString(w, `{"id":"overfin","createdSecret":{"id":"gen1"}}`)
	case r.URL.Path == "/api/oidc/clients/overfin/secrets/gen1" && r.Method == http.MethodDelete:
		f.secrets = f.secrets[:0]
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/api/oidc/clients/overfin" && r.Method == http.MethodPut:
		body["id"] = "overfin"
		f.client = body
		io.WriteString(w, `{}`)
	case r.URL.Path == "/api/oidc/clients/overfin/secrets" && r.Method == http.MethodGet:
		var list []map[string]any
		for _, p := range f.secrets {
			list = append(list, map[string]any{"prefix": p, "isActive": true})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": list})
	case r.URL.Path == "/api/oidc/clients/overfin/secrets" && r.Method == http.MethodPost:
		f.secrets = append(f.secrets, body["secret"].(string)[:7])
		io.WriteString(w, `{}`)
	case r.URL.Path == "/api/user-groups" && r.Method == http.MethodGet:
		var data []map[string]string
		for i, g := range f.groups {
			if strings.Contains(g, r.URL.Query().Get("search")) {
				data = append(data, map[string]string{"id": "g" + string(rune('0'+i)), "name": g})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	case r.URL.Path == "/api/user-groups" && r.Method == http.MethodPost:
		f.groups = append(f.groups, body["name"].(string))
		io.WriteString(w, `{}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// Setup at every start: the first makes the client (Pocket ID's own
// secret removed, the app's added) and the groups; the next only updates
// the client, adding no secret and no group twice; a group whose name
// only contains another's is still made.
func TestPocketIDSetup(t *testing.T) {
	f := &pocketFake{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	p := &PocketID{BaseURL: srv.URL, Key: "good-key-1234567", HTTP: srv.Client()}
	c := Client{ID: "overfin", Name: "Overfin", Secret: "the-apps-own-secret-123", CallbackURLs: []string{"https://example.org/cb"}, LaunchURL: "https://example.org"}
	ctx := context.Background()
	if err := p.Setup(ctx, c, []string{"overfin-users", "overfin-admins"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	first := strings.Join(f.calls, ", ")
	if f.client == nil || f.client["pkceEnabled"] != true || f.client["isPublic"] != false || f.client["launchURL"] != "https://example.org" {
		t.Errorf("the client made: %v", f.client)
	}
	if len(f.secrets) != 1 || f.secrets[0] != "the-app" || len(f.groups) != 2 {
		t.Errorf("after the first: secrets %v, groups %v", f.secrets, f.groups)
	}
	f.calls = nil
	f.mu.Unlock()
	want := "POST /api/oidc/clients, DELETE /api/oidc/clients/overfin/secrets/gen1, POST /api/oidc/clients/overfin/secrets, POST /api/user-groups, POST /api/user-groups"
	if first != want {
		t.Errorf("first setup:\n got %s\nwant %s", first, want)
	}

	c.Name = "Overfin (renamed)"
	if err := p.Setup(ctx, c, []string{"overfin-users", "overfin-admins", "overfin"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if got := strings.Join(f.calls, ", "); got != "PUT /api/oidc/clients/overfin, POST /api/user-groups" {
		t.Errorf("second setup: %s", got)
	}
	if f.client["name"] != "Overfin (renamed)" || len(f.secrets) != 1 || len(f.groups) != 3 {
		t.Errorf("after the second: %v, secrets %v, groups %v", f.client, f.secrets, f.groups)
	}
}
