package api

import "testing"

func TestOperations(t *testing.T) {
	ops := Operations()
	if len(ops) < 80 {
		t.Fatalf("only %d operations read", len(ops))
	}
	want := map[string]bool{"GET /api/v1/health": false, "POST /api/v1/signin": false, "GET /api/v1/stream/{ticket}/{name}": false, "POST /api/v1/federation/titles": false}
	for _, op := range ops {
		if _, ok := want[op.Method+" "+op.Path]; ok {
			want[op.Method+" "+op.Path] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("%s not read", k)
		}
	}
}

func TestPublic(t *testing.T) {
	public := map[string]bool{}
	for _, op := range Operations() {
		public[op.Method+" "+op.Path] = op.Public
	}
	for k, want := range map[string]bool{"GET /api/v1/health": true, "POST /api/v1/signin": true, "GET /api/v1/me": false, "POST /api/v1/playback": false} {
		if public[k] != want {
			t.Errorf("%s: public %v", k, public[k])
		}
	}
}
