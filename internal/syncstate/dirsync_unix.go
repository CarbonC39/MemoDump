//go:build !windows

package syncstate

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// DirSyncSupported reports whether this platform can fsync a directory (the
// capability that makes an atomic rename fully durable). On POSIX it is true;
// on Windows there is no portable directory fsync, so callers must not assume
// a stronger guarantee than file fsync plus atomic replace.
func DirSyncSupported() bool { return true }

// syncDir flushes a directory-entry change (a rename) to stable storage. On
// POSIX the directory is opened and fsynced. Filesystems that reject directory
// fsync (EINVAL/ENOTSUP) lack the capability; that is the strongest guarantee
// available there, so the write proceeds. Other fsync errors propagate.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := unix.Fsync(int(f.Fd())); err != nil {
		if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) {
			return nil
		}
		return err
	}
	return nil
}
