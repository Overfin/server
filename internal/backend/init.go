package backend

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// childEnv marks the backend started by its own init, so it doesn't start
// another.
const childEnv = "OVERFIN_BACKEND_CHILD"

// Init is what the backend does as the container's first process: the
// processes Jellyfin leaves behind (an ffmpeg after a crash) become that
// process's children, and it must reap them. So it starts the backend
// itself as its only child, passes it the signals that stop it, reaps
// every child that ends, and exits with the backend's status. It returns
// false, at once, when the backend isn't the first process or is that
// child already: the caller then runs the backend.
func Init() (exitCode int, ran bool) {
	if os.Getpid() != 1 || os.Getenv(childEnv) != "" {
		return 0, false
	}
	self, err := os.Executable()
	if err != nil {
		return 1, true
	}
	stops := make(chan os.Signal, 4)
	signal.Notify(stops, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT)
	children := make(chan os.Signal, 1)
	signal.Notify(children, syscall.SIGCHLD)

	cmd := exec.Command(self, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), childEnv+"=1")
	if err := cmd.Start(); err != nil {
		return 1, true
	}
	backend := cmd.Process.Pid
	for {
		select {
		case s := <-stops:
			_ = cmd.Process.Signal(s)
		case <-children:
		}
		// Every child that ended, without waiting for the others.
		for {
			var st syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &st, syscall.WNOHANG, nil)
			if pid <= 0 || err != nil {
				break
			}
			if pid == backend {
				switch {
				case st.Exited():
					return st.ExitStatus(), true
				case st.Signaled():
					return 128 + int(st.Signal()), true
				default:
					return 1, true
				}
			}
		}
	}
}
