//go:build unix

package jsonfile

import (
	"errors"
	"os"
	"syscall"
)

// lock takes an exclusive lock on f, or fails at once when another
// process holds it.
func lock(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errLocked
	}
	return err
}
