//go:build windows

package syncstate

import "golang.org/x/sys/windows"

// atomicReplace installs new over old with the strongest atomicity Windows
// provides: MoveFileEx with MOVEFILE_REPLACE_EXISTING and MOVEFILE_WRITE_THROUGH,
// so the move replaces an existing target and is flushed to stable storage
// before returning. There is no portable directory fsync (see
// DirSyncSupported), so the strongest durability is the temp-file
// FlushFileBuffers plus this write-through rename; callers must not assume more.
func atomicReplace(oldpath, newpath string) error {
	from, err := windows.UTF16PtrFromString(oldpath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(newpath)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
