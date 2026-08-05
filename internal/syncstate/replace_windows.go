//go:build windows

package syncstate

import "os"

// atomicReplace installs new over old with the strongest atomicity Windows
// provides: os.Rename uses MoveFileEx with MOVEFILE_REPLACE_EXISTING. There is
// no portable directory fsync (see DirSyncSupported), so the strongest
// durability is the FlushFileBuffers on the temp file before this rename plus
// the replace-existing rename itself; callers must not assume more.
func atomicReplace(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}
