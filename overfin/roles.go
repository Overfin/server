package overfin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Overfin's image carries Overfin, its Jellyfin and Overfin's plugin for
// it, at versions made for each other. It starts in one of three roles
// (OVERFIN_ROLE):
//   - gateway (the default): Overfin alone, talking to a Jellyfin elsewhere;
//   - all: Overfin and its Jellyfin together, Jellyfin listening only
//     inside the container;
//   - jellyfin: Jellyfin and the plugin alone, for a gateway on another
//     machine.

const (
	roleGateway  = "gateway"
	roleAll      = "all"
	roleJellyfin = "jellyfin"
)

// role is the role asked for, checked.
func role(getenv func(string) string) (string, error) {
	switch r := strings.TrimSpace(getenv("OVERFIN_ROLE")); r {
	case "", roleGateway:
		return roleGateway, nil
	case roleAll, roleJellyfin:
		return r, nil
	default:
		return "", fmt.Errorf("OVERFIN_ROLE: %q isn't a role (gateway, all or jellyfin)", r)
	}
}

// Where the image keeps Jellyfin and the plugin (the Dockerfile's).
const (
	jellyfinProgram = "/jellyfin/jellyfin"
	pluginShipped   = "/usr/lib/overfin/jellyfin-plugin"
)

// jellyfinDirs are Jellyfin's data and configuration folders, as its image
// sets them.
func jellyfinDirs(getenv func(string) string) (data, conf string) {
	data, conf = getenv("JELLYFIN_DATA_DIR"), getenv("JELLYFIN_CONFIG_DIR")
	if data == "" {
		data = "/config"
	}
	if conf == "" {
		conf = filepath.Join(data, "config")
	}
	return data, conf
}

// installPlugin puts the plugin the image ships among Jellyfin's plugins,
// as the only version of it there: Jellyfin loads it at its next start.
// shipped holds one folder per version, named after it.
func installPlugin(shipped, dataDir string) (string, error) {
	versions, err := os.ReadDir(shipped)
	if err != nil {
		return "", fmt.Errorf("the plugin the image ships: %w", err)
	}
	if len(versions) != 1 || !versions[0].IsDir() || !versionRe.MatchString(versions[0].Name()) {
		return "", errors.New("the image ships no plugin, or more than one")
	}
	version := versions[0].Name()
	plugins := filepath.Join(dataDir, "plugins")
	if err := os.MkdirAll(plugins, 0o700); err != nil {
		return "", err
	}
	// Other versions of it go: Jellyfin would load the newest.
	others, _ := filepath.Glob(filepath.Join(plugins, "Overfin_*"))
	target := filepath.Join(plugins, "Overfin_"+version+".0")
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
	files, err := os.ReadDir(filepath.Join(shipped, version))
	if err != nil {
		return "", err
	}
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if err := copyFile(filepath.Join(shipped, version, f.Name()), filepath.Join(target, f.Name())); err != nil {
			return "", err
		}
	}
	return version, nil
}

var versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

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

// loopbackOnly has Jellyfin listen on 127.0.0.1 only, in its network
// settings, before it starts: in the all role, nothing outside the
// container reaches it. With open (OVERFIN_JELLYFIN_LISTEN=all, while an
// admin looks inside Jellyfin), it listens on the container's addresses,
// for its port to be published. A Jellyfin set up for the first time gets
// the settings file with only that.
func loopbackOnly(confDir string, open bool) error {
	path := filepath.Join(confDir, "network.xml")
	bind := `<LocalNetworkAddresses><string>127.0.0.1</string></LocalNetworkAddresses>`
	if open {
		bind = `<LocalNetworkAddresses />`
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(confDir, 0o700); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(`<?xml version="1.0" encoding="utf-8"?>
<NetworkConfiguration xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema">
  `+bind+`
</NetworkConfiguration>
`), 0o600)
	}
	if err != nil {
		return err
	}
	s := string(b)
	switch {
	case bindEmptyRe.MatchString(s):
		s = bindEmptyRe.ReplaceAllLiteralString(s, bind)
	case bindListRe.MatchString(s):
		s = bindListRe.ReplaceAllLiteralString(s, bind)
	case strings.Contains(s, "</NetworkConfiguration>"):
		s = strings.Replace(s, "</NetworkConfiguration>", "  "+bind+"\n</NetworkConfiguration>", 1)
	default:
		return fmt.Errorf("%s isn't Jellyfin's network settings", path)
	}
	if s == string(b) {
		return nil
	}
	return os.WriteFile(path, []byte(s), 0o600)
}

// prepareJellyfin puts the plugin in place and, for the all role, keeps
// Jellyfin to the container.
func prepareJellyfin(r string, getenv func(string) string, logger *slog.Logger) error {
	data, conf := jellyfinDirs(getenv)
	version, err := installPlugin(pluginShipped, data)
	if err != nil {
		return err
	}
	logger.Info("Overfin's plugin is in place for Jellyfin", "version", version)
	if r == roleAll {
		open := strings.TrimSpace(getenv("OVERFIN_JELLYFIN_LISTEN")) == "all"
		if open {
			logger.Warn("Jellyfin listens on the container's addresses (OVERFIN_JELLYFIN_LISTEN=all): " +
				"remove the setting once you're done looking inside it")
		}
		return loopbackOnly(conf, open)
	}
	return nil
}

// runJellyfinOnly is the jellyfin role: Jellyfin, with the plugin in
// place, in Overfin's stead (the container runs as long as it does).
func runJellyfinOnly(logger *slog.Logger) error {
	if err := prepareJellyfin(roleJellyfin, os.Getenv, logger); err != nil {
		return err
	}
	return syscall.Exec(jellyfinProgram, []string{jellyfinProgram}, os.Environ())
}

// superviseJellyfin runs Jellyfin beside Overfin (the all role) until ctx
// ends, starting it again when it stops, a little later each time it
// stops soon after starting. Jellyfin ends with Overfin, even if Overfin
// is killed.
func superviseJellyfin(ctx context.Context, logger *slog.Logger) {
	wait := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		cmd := exec.Command(jellyfinProgram)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
		if err := cmd.Start(); err != nil {
			logger.Error("Jellyfin couldn't start", "err", err)
		} else {
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				logger.Warn("Jellyfin stopped; starting it again", "err", err)
			case <-ctx.Done():
				cmd.Process.Signal(syscall.SIGTERM)
				select {
				case <-done:
				case <-time.After(20 * time.Second):
					cmd.Process.Kill()
					<-done
				}
				return
			}
		}
		if time.Since(started) > time.Minute {
			wait = time.Second
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
		wait = min(2*wait, 30*time.Second)
	}
}
