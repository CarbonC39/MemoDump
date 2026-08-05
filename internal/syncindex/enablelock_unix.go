//go:build !windows

package syncindex

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockExclusive takes a blocking exclusive flock; the enable lock is short-lived.
func lockExclusive(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX) }

func unlockLock(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }
