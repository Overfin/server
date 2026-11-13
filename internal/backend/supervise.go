package backend

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// stopWait is how long Jellyfin gets to stop cleanly before it's killed.
const stopWait = 20 * time.Second

// Supervise runs Jellyfin until ctx ends, starting it again whenever it
// stops: at once after a long run, a little later each time it stops soon
// after starting (up to 30 seconds). When ctx ends, Jellyfin is asked to
// stop, and killed if it hasn't after 20 seconds; Supervise returns once
// it's gone. Jellyfin also ends if the backend is killed outright.
func (j Jellyfin) Supervise(ctx context.Context, logger *slog.Logger) {
	wait := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		cmd := exec.Command(j.Program)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
		if err := cmd.Start(); err != nil {
			logger.Error("Jellyfin couldn't start", "err", err)
		} else {
			logger.Info("Jellyfin started", "pid", cmd.Process.Pid)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				logger.Warn("Jellyfin stopped; starting it again", "err", err)
			case <-ctx.Done():
				logger.Info("stopping Jellyfin")
				_ = cmd.Process.Signal(syscall.SIGTERM)
				select {
				case <-done:
					logger.Info("Jellyfin stopped")
				case <-time.After(stopWait):
					logger.Warn("Jellyfin didn't stop in time: killed")
					_ = cmd.Process.Kill()
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

// WaitHealthy waits until Jellyfin at url is really up: its /health says
// "Healthy". While it starts, Jellyfin first answers from a temporary
// server ("Degraded", even its public information), then "loading" (503):
// anything before "Healthy" can vanish the next second. It returns
// ctx.Err() when ctx ends first.
func WaitHealthy(ctx context.Context, url string, logger *slog.Logger) error {
	client := &http.Client{Timeout: 5 * time.Second}
	said := time.Time{}
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/health", nil)
		if resp, err := client.Do(req); err == nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && strings.TrimSpace(string(b)) == "Healthy" {
				return nil
			}
		}
		if time.Since(said) > time.Minute {
			logger.Info("waiting for Jellyfin to be ready")
			said = time.Now()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
