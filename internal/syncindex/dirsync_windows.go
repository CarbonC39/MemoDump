//go:build windows

package syncindex

// syncDir is a no-op on Windows: there is no portable way to fsync a directory.
// The strongest guarantee available is the FlushFileBuffers call on the temp
// file before its atomic rename (writeFileAtomic); the NTFS rename is atomic.
func syncDir(string) error { return nil }
