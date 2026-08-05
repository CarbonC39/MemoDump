//go:build linux

package syncstate

import "golang.org/x/sys/unix"

// renameNoClobber atomically renames oldpath to newpath, failing with EEXIST
// (errors.Is(err, os.ErrExist)) when newpath exists. Linux renameat2 provides
// RENAME_NOREPLACE directly; a frozen generation is never overwritten.
func renameNoClobber(oldpath, newpath string) error {
	return unix.Renameat2(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_NOREPLACE)
}
