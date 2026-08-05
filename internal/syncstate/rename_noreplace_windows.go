//go:build windows

package syncstate

import "golang.org/x/sys/windows"

// renameNoClobber renames oldpath to newpath without replacing an existing
// target: MoveFileEx without MOVEFILE_REPLACE_EXISTING fails when newpath
// exists, and MOVEFILE_WRITE_THROUGH flushes the move to stable storage before
// returning — the strongest durability Windows provides.
func renameNoClobber(oldpath, newpath string) error {
	from, err := windows.UTF16PtrFromString(oldpath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(newpath)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}
