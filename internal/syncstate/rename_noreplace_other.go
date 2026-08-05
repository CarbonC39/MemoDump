//go:build !linux

package syncstate

import (
	"os"
	"path/filepath"
)

// renameNoClobber renames oldpath to newpath, failing when newpath exists.
// Where renameat2(RENAME_NOREPLACE) is unavailable, a hard link (which fails
// with EEXIST on an existing target) followed by unlink gives no-replace
// semantics. The new link is fsynced through the directory before the old is
// removed so a crash at the power-loss boundary cannot leave neither name
// durable; on platforms without directory fsync (see DirSyncSupported) this is
// the strongest guarantee available. A crash between the two steps leaves both
// names for one inode, which recovery replays idempotently.
func renameNoClobber(oldpath, newpath string) error {
	if err := os.Link(oldpath, newpath); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(newpath)); err != nil {
		return err
	}
	if err := os.Remove(oldpath); err != nil {
		return err
	}
	return syncDir(filepath.Dir(oldpath))
}
