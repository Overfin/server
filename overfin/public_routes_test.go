package overfin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/api"
	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/keys"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// The gateway's allowlist takes which routes need a session from the API
// specification (security: []); the backend decides it in its code. A
// route public in one and not the other is either refused to those who may
// use it (a paired server's relay, which carries no cookie) or let past the
// gateway's first check: the two must agree.
func TestPublicRoutesMatchSpecification(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "overfin.db")
	cfgPath := filepath.Join(dir, "overfin.toml")
	content := `
listen = ":8080"
[jellyfin]
url = "http://192.0.2.10:8096"
[identity]
mode = "managed"
[site]
url = "https://media.example.org"
[storage]
path = "` + dbPath + `"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath, env("p"))
	if err != nil {
		t.Fatal(err)
	}
	k, _ := keys.Derive(cfg.Secrets.Key)
	box, _ := seal.New(k.TokenEncryption)
	db, err := store.Open(context.Background(), dbPath, box)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	jf, _ := jellyfin.New(cfg.Jellyfin.URL, "test")
	sh := &shared{logger: logger, db: db, keys: k, jf: jf, images: images.New(jf, 1<<20), tickets: playback.NewTickets(time.Hour)}
	if _, err := newGateway(cfgPath, cfg, sh); err != nil {
		t.Fatal(err)
	}
	served := map[string]bool{}
	sc := bufio.NewScanner(&logs)
	for sc.Scan() {
		var l struct {
			Msg     string `json:"msg"`
			Pattern string `json:"pattern"`
			Public  bool   `json:"public"`
		}
		if json.Unmarshal(sc.Bytes(), &l) == nil && l.Msg == "route" {
			served[normal(l.Pattern)] = l.Public
		}
	}
	if len(served) < 50 {
		t.Fatalf("only %d routes logged", len(served))
	}
	compared := 0
	for _, op := range api.Operations() {
		public, ok := served[normal(op.Method+" "+op.Path)]
		if !ok {
			continue
		}
		compared++
		if public != op.Public {
			t.Errorf("%s %s: public in the code %v, in the specification %v", op.Method, op.Path, public, op.Public)
		}
	}
	if compared < 50 {
		t.Errorf("only %d routes compared", compared)
	}
}

// normal drops the names of a pattern's wildcards ({id} and {titleId}
// alike).
func normal(pattern string) string {
	var b strings.Builder
	in := false
	for _, r := range pattern {
		switch {
		case r == '{':
			in = true
			b.WriteString("{}")
		case r == '}':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}
