//go:build !windows

package syncstate

import "os"

// atomicReplace installs new over old with the strongest atomicity the
// platform provides: on POSIX, os.Rename is atomic, and the calling durable
// write fsyncs the containing directory (syncDir) so the replacement is
// durable. A failed rename stops the commit.
func atomicReplace(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}
