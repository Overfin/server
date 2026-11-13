// Package backend is Overfin's backend: the part that runs next to the
// media, in the overfin-backend image, with Jellyfin and Overfin's plugin
// for it. This file is how the backend runs that Jellyfin: the plugin in
// place, Jellyfin listening inside the container only, started again when
// it stops, stopped cleanly with the backend.
package backend

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Jellyfin is where the image keeps Jellyfin and the plugin, and where
// Jellyfin keeps its data. The zero value isn't usable: see FromEnv.
type Jellyfin struct {
	Program       string // Jellyfin's program
	PluginShipped string // the plugin the image ships, one folder per version
	DataDir       string // Jellyfin's data folder (its plugins are in it)
	ConfigDir     string // Jellyfin's settings folder (network.xml)
}

// FromEnv is the image's Jellyfin: its program and plugin where the
// Dockerfile puts them, its folders as Jellyfin's own image sets them.
func FromEnv(getenv func(string) string) Jellyfin {
	data, conf := getenv("JELLYFIN_DATA_DIR"), getenv("JELLYFIN_CONFIG_DIR")
	if data == "" {
		data = "/config"
	}
	if conf == "" {
		conf = filepath.Join(data, "config")
	}
	return Jellyfin{
		Program:       "/jellyfin/jellyfin",
		PluginShipped: "/usr/lib/overfin/jellyfin-plugin",
		DataDir:       data,
		ConfigDir:     conf,
	}
}

var versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// InstallPlugin puts the plugin the image ships among Jellyfin's plugins,
// as the only version of it there (Jellyfin would load the newest of
// several), and says which version it is. Jellyfin loads it at its next
// start. Jellyfin's own files in the plugin's folder (its meta.json) stay.
func (j Jellyfin) InstallPlugin() (string, error) {
	versions, err := os.ReadDir(j.PluginShipped)
	if err != nil {
		return "", fmt.Errorf("the plugin the image ships: %w", err)
	}
	if len(versions) != 1 || !versions[0].IsDir() || !versionRe.MatchString(versions[0].Name()) {
		return "", errors.New("the image ships no plugin, or more than one")
	}
	version := versions[0].Name()
	plugins := filepath.Join(j.DataDir, "plugins")
	if err := os.MkdirAll(plugins, 0o700); err != nil {
		return "", err
	}
	target := filepath.Join(plugins, "Overfin_"+version+".0")
	others, _ := filepath.Glob(filepath.Join(plugins, "Overfin_*"))
	for _, o := range others {
		if o != target {
			if err := os.RemoveAll(o); err != nil {
				return "", err
			}
		}
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return "", err
	}
	files, err := os.ReadDir(filepath.Join(j.PluginShipped, version))
	if err != nil {
		return "", err
	}
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if err := copyFile(filepath.Join(j.PluginShipped, version, f.Name()), filepath.Join(target, f.Name())); err != nil {
			return "", err
		}
	}
	return version, nil
}

// copyFile copies a file whole, through a temporary file renamed at the
// end, so Jellyfin never finds half of it.
func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := to + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, to)
}

var (
	bindEmptyRe = regexp.MustCompile(`<LocalNetworkAddresses\s*/>`)
	bindListRe  = regexp.MustCompile(`(?s)<LocalNetworkAddresses>.*?</LocalNetworkAddresses>`)
)

const loopbackBind = `<LocalNetworkAddresses><string>127.0.0.1</string></LocalNetworkAddresses>`

// LoopbackOnly has Jellyfin listen on 127.0.0.1 only, in its network
// settings, before it starts: only the backend, in the same container,
// reaches it. Its other network settings (its port among them) stay. A
// Jellyfin set up for the first time gets a settings file with only that.
func (j Jellyfin) LoopbackOnly() error {
	path := filepath.Join(j.ConfigDir, "network.xml")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(j.ConfigDir, 0o700); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(`<?xml version="1.0" encoding="utf-8"?>
<NetworkConfiguration xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema">
  `+loopbackBind+`
</NetworkConfiguration>
`), 0o600)
	}
	if err != nil {
		return err
	}
	s := string(b)
	switch {
	case bindEmptyRe.MatchString(s):
		s = bindEmptyRe.ReplaceAllLiteralString(s, loopbackBind)
	case bindListRe.MatchString(s):
		s = bindListRe.ReplaceAllLiteralString(s, loopbackBind)
	case strings.Contains(s, "</NetworkConfiguration>"):
		s = strings.Replace(s, "</NetworkConfiguration>", "  "+loopbackBind+"\n</NetworkConfiguration>", 1)
	default:
		return fmt.Errorf("%s isn't Jellyfin's network settings", path)
	}
	if s == string(b) {
		return nil
	}
	return os.WriteFile(path, []byte(s), 0o600)
}

// Version is the backend's version, set at build time.
var Version = "dev"
