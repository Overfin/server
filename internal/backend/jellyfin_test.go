package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFromEnv(t *testing.T) {
	j := FromEnv(func(string) string { return "" })
	if j.DataDir != "/config" || j.ConfigDir != "/config/config" {
		t.Errorf("defaults: %+v", j)
	}
	env := map[string]string{"JELLYFIN_DATA_DIR": "/d", "JELLYFIN_CONFIG_DIR": "/c"}
	if j := FromEnv(func(k string) string { return env[k] }); j.DataDir != "/d" || j.ConfigDir != "/c" {
		t.Errorf("from the environment: %+v", j)
	}
}

// The plugin the image ships becomes the only version of it among
// Jellyfin's plugins; other plugins, and Jellyfin's own file in the
// plugin's folder, stay.
func TestInstallPlugin(t *testing.T) {
	j := Jellyfin{PluginShipped: t.TempDir(), DataDir: t.TempDir()}
	os.MkdirAll(filepath.Join(j.PluginShipped, "0.2.0"), 0o700)
	os.WriteFile(filepath.Join(j.PluginShipped, "0.2.0", "Overfin.Jellyfin.dll"), []byte("new"), 0o600)
	plugins := filepath.Join(j.DataDir, "plugins")
	os.MkdirAll(filepath.Join(plugins, "Overfin_0.1.0.0"), 0o700)
	os.MkdirAll(filepath.Join(plugins, "Intro Skipper_12.0.5.0"), 0o700)
	v, err := j.InstallPlugin()
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
	// Jellyfin's meta.json stays across starts.
	os.WriteFile(filepath.Join(plugins, "Overfin_0.2.0.0", "meta.json"), []byte("{}"), 0o600)
	if v, err := j.InstallPlugin(); err != nil || v != "0.2.0" {
		t.Fatalf("again: %q %v", v, err)
	}
	if _, err := os.Stat(filepath.Join(plugins, "Overfin_0.2.0.0", "meta.json")); err != nil {
		t.Error("Jellyfin's meta.json removed")
	}
	// Two versions shipped: refused.
	os.MkdirAll(filepath.Join(j.PluginShipped, "0.3.0"), 0o700)
	if _, err := j.InstallPlugin(); err == nil {
		t.Error("two versions shipped, accepted")
	}
}

// Jellyfin listens on 127.0.0.1 only, whatever its network settings said;
// its other settings stay.
func TestLoopbackOnly(t *testing.T) {
	j := Jellyfin{ConfigDir: t.TempDir()}
	if err := j.LoopbackOnly(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(j.ConfigDir, "network.xml")); !strings.Contains(string(b), loopbackBind) {
		t.Fatalf("a new Jellyfin's: %s", b)
	}
	for name, in := range map[string]string{
		"empty":   "<NetworkConfiguration>\n  <InternalHttpPort>8097</InternalHttpPort>\n  <LocalNetworkAddresses />\n</NetworkConfiguration>",
		"a list":  "<NetworkConfiguration>\n  <InternalHttpPort>8097</InternalHttpPort>\n  <LocalNetworkAddresses>\n    <string>0.0.0.0</string>\n  </LocalNetworkAddresses>\n</NetworkConfiguration>",
		"missing": "<NetworkConfiguration>\n  <InternalHttpPort>8097</InternalHttpPort>\n</NetworkConfiguration>",
	} {
		j := Jellyfin{ConfigDir: t.TempDir()}
		path := filepath.Join(j.ConfigDir, "network.xml")
		os.WriteFile(path, []byte(in), 0o600)
		if err := j.LoopbackOnly(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := os.ReadFile(path)
		if s := string(b); !strings.Contains(s, loopbackBind) || strings.Count(s, "LocalNetworkAddresses>") != 2 ||
			!strings.Contains(s, "<InternalHttpPort>8097</InternalHttpPort>") || strings.Contains(s, "0.0.0.0") {
			t.Errorf("%s: %s", name, s)
		}
	}
	bad := Jellyfin{ConfigDir: t.TempDir()}
	os.WriteFile(filepath.Join(bad.ConfigDir, "network.xml"), []byte("<Something/>"), 0o600)
	if err := bad.LoopbackOnly(); err == nil {
		t.Error("a file that isn't Jellyfin's network settings, changed")
	}
}
