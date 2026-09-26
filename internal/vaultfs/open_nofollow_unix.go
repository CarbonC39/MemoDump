//go:build !windows

package vaultfs

import (
	"os"

	"golang.org/x/sys/unix"
)

// openNoFollow opens a file without ever following a symlink at the final path
// component. If the path became a symlink after the caller's Lstat, the open
// fails with ELOOP instead of reading through it; intermediate directories are
// guarded separately (Scan's ancestor check). On Unix the opened file's
// metadata is still re-checked against the stat-pass values, so a path swapped
// to a different file is also rejected.
func openNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
