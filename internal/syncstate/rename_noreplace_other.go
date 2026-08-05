//go:build !linux

package syncstate

import "os"

// renameNoClobber renames oldpath to newpath, failing when newpath exists.
// Where renameat2(RENAME_NOREPLACE) is unavailable, a hard link (which fails
// with EEXIST on an existing target) followed by unlink gives no-replace
// semantics. A crash between the two steps leaves both names for one inode,
// which recovery replays idempotently.
func renameNoClobber(oldpath, newpath string) error {
	if err := os.Link(oldpath, newpath); err != nil {
		return err
	}
	return os.Remove(oldpath)
}
