//go:build !windows

package syncindex

import (
	"os"

	"golang.org/x/sys/unix"
)

// syncDir flushes a directory-entry change (a rename) to stable storage by
// opening the directory and fsyncing it. On POSIX this makes an atomic rename
// durable. Filesystems that reject directory fsync with EINVAL/ENOTSUP simply
// lack the capability; that is the strongest guarantee available on them, so
// the write proceeds rather than failing. Any other fsync error propagates and
// stops the commit.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := unix.Fsync(int(f.Fd())); err != nil {
		if err == unix.EINVAL || err == unix.ENOTSUP {
			return nil
		}
		return err
	}
	return nil
}
