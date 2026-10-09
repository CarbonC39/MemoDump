//go:build windows

package vaultfs

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// errReparsePoint marks an open that reached a Windows reparse point (a
// symlink or directory junction) despite FILE_FLAG_OPEN_REPARSE_POINT. The
// caller re-Lstats and classifies it as blocked.
var errReparsePoint = errors.New("path is a Windows reparse point")

// openNoFollow opens a file without following a reparse point: the handle is
// created with FILE_FLAG_OPEN_REPARSE_POINT so it refers to the link itself,
// never its target, and a handle whose attributes still report a reparse point
// is rejected. Intermediate directories are guarded separately (the scanner's
// ancestor check), and the caller's size/mtime and os.SameFile re-checks close
// the remaining swap race.
func openNoFollow(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := windows.CreateFile(p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(h), path)
	if f == nil {
		windows.CloseHandle(h)
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrInvalid}
	}
	if fi, err := f.Stat(); err != nil {
		f.Close()
		return nil, err
	} else if fi.Mode()&os.ModeSymlink != 0 {
		// The handle is a reparse point (symlink/junction), not the real file.
		f.Close()
		return nil, errReparsePoint
	}
	return f, nil
}
