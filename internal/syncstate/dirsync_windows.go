//go:build windows

package syncstate

// syncDir is a no-op on Windows: there is no portable way to fsync a directory.
// The strongest guarantee available is the FlushFileBuffers on the temp file
// before its atomic rename; the NTFS rename is atomic.
func syncDir(string) error { return nil }
