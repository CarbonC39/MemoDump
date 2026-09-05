//go:build windows

package syncindex

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockExclusive takes a blocking exclusive byte-range lock; the enable lock is
// short-lived.
func lockExclusive(f *os.File) error {
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, 1, 0, new(windows.Overlapped))
}

func unlockLock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}
