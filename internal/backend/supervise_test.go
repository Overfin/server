package backend

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeJellyfin is a program standing in for Jellyfin: it notes each start
// in a file, then runs script.
func fakeJellyfin(t *testing.T, script string) (Jellyfin, string) {
	t.Helper()
	dir := t.TempDir()
	starts := filepath.Join(dir, "starts")
	prog := filepath.Join(dir, "jellyfin")
	os.WriteFile(prog, []byte("#!/bin/sh\necho start >> "+starts+"\n"+script+"\n"), 0o700)
	return Jellyfin{Program: prog}, starts
}

func count(path string) int {
	b, _ := os.ReadFile(path)
	return strings.Count(string(b), "start")
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// A Jellyfin that stops is started again.
func TestSuperviseRestarts(t *testing.T) {
	j, starts := fakeJellyfin(t, "exit 3")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { j.Supervise(ctx, quiet); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for count(starts) < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if n := count(starts); n < 2 {
		t.Fatalf("started %d times", n)
	}
}

// When the backend stops, Jellyfin is asked to stop and Supervise waits
// for it.
func TestSuperviseStopsCleanly(t *testing.T) {
	dir := t.TempDir()
	stopped := filepath.Join(dir, "stopped")
	j, starts := fakeJellyfin(t, "trap 'echo yes > "+stopped+"; exit 0' TERM\nwhile :; do sleep 0.1; done")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { j.Supervise(ctx, quiet); close(done) }()
	for count(starts) < 1 {
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // the trap is set
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Supervise didn't return")
	}
	if b, _ := os.ReadFile(stopped); strings.TrimSpace(string(b)) != "yes" {
		t.Error("Jellyfin wasn't asked to stop")
	}
	if n := count(starts); n != 1 {
		t.Errorf("started %d times", n)
	}
}
