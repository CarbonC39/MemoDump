//go:build !windows

package syncstate

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLock takes a non-blocking exclusive flock. flock treats each open file
// description independently, so a second handle — even in the same process —
// reports ErrLocked, matching the cross-process contract.
func tryLock(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return ErrLocked
		}
		return err
	}
	return nil
}

func unlock(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }

// lockBlocking takes a blocking exclusive flock. Used by the short-lived
// registry lock, where the correct behavior is to wait for the other process
// rather than fail.
func lockBlocking(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX) }
