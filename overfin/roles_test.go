package overfin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRole(t *testing.T) {
	for in, want := range map[string]string{"": "gateway", "gateway": "gateway", " all ": "all", "jellyfin": "jellyfin"} {
		if got, err := role(func(string) string { return in }); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	if _, err := role(func(string) string { return "both" }); err == nil {
		t.Error("an unknown role accepted")
	}
}

// The plugin the image ships becomes the only version of it among
// Jellyfin's plugins; other plugins stay.
func TestInstallPlugin(t *testing.T) {
	shipped, data := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(shipped, "0.2.0"), 0o700)
	os.WriteFile(filepath.Join(shipped, "0.2.0", "Overfin.Jellyfin.dll"), []byte("new"), 0o600)
	plugins := filepath.Join(data, "plugins")
	os.MkdirAll(filepath.Join(plugins, "Overfin_0.1.0.0"), 0o700)
	os.MkdirAll(filepath.Join(plugins, "Intro Skipper_12.0.5.0"), 0o700)
	v, err := installPlugin(shipped, data)
	if err != nil || v != "0.2.0" {
		t.Fatalf("%q %v", v, err)
	}
	if b, _ := os.ReadFile(filepath.Join(plugins, "Overfin_0.2.0.0", "Overfin.Jellyfin.dll")); string(b) != "new" {
		t.Errorf("installed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(plugins, "Overfin_0.1.0.0")); err == nil {
		t.Error("the former version stayed")
	}
	if _, err := os.Stat(filepath.Join(plugins, "Intro Skipper_12.0.5.0")); err != nil {
		t.Error("another plugin was removed")
	}
	// Again: nothing changes.
	if v, err := installPlugin(shipped, data); err != nil || v != "0.2.0" {
		t.Fatalf("again: %q %v", v, err)
	}
	// Two versions shipped: refused.
	os.MkdirAll(filepath.Join(shipped, "0.3.0"), 0o700)
	if _, err := installPlugin(shipped, data); err == nil {
		t.Error("two versions shipped, accepted")
	}
}

// In the all role, Jellyfin listens on 127.0.0.1 only, whatever its
// network settings said; its other settings stay.
func TestLoopbackOnly(t *testing.T) {
	const bind = "<LocalNetworkAddresses><string>127.0.0.1</string></LocalNetworkAddresses>"
	dir := t.TempDir()
	if err := loopbackOnly(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "network.xml")); !strings.Contains(string(b), bind) {
		t.Fatalf("a new Jellyfin's: %s", b)
	}
	for name, in := range map[string]string{
		"empty":   "<NetworkConfiguration>\n  <EnableUPnP>false</EnableUPnP>\n  <LocalNetworkAddresses />\n</NetworkConfiguration>",
		"a list":  "<NetworkConfiguration>\n  <EnableUPnP>false</EnableUPnP>\n  <LocalNetworkAddresses>\n    <string>0.0.0.0</string>\n  </LocalNetworkAddresses>\n</NetworkConfiguration>",
		"missing": "<NetworkConfiguration>\n  <EnableUPnP>false</EnableUPnP>\n</NetworkConfiguration>",
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.xml")
		os.WriteFile(path, []byte(in), 0o600)
		if err := loopbackOnly(dir); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := os.ReadFile(path)
		if s := string(b); !strings.Contains(s, bind) || strings.Count(s, "LocalNetworkAddresses>") != 2 || !strings.Contains(s, "<EnableUPnP>false</EnableUPnP>") || strings.Contains(s, "0.0.0.0") {
			t.Errorf("%s: %s", name, s)
		}
	}
	bad := t.TempDir()
	os.WriteFile(filepath.Join(bad, "network.xml"), []byte("<Something/>"), 0o600)
	if err := loopbackOnly(bad); err == nil {
		t.Error("a file that isn't Jellyfin's network settings, changed")
	}
}
