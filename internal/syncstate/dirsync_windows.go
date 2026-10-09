//go:build windows

package syncstate

// DirSyncSupported reports whether this platform can fsync a directory. On
// Windows there is no portable way to do so, so the capability is explicit
// (false) rather than silently simulated.
func DirSyncSupported() bool { return false }

// syncDir is a no-op on Windows: there is no portable way to fsync a directory.
// The strongest guarantee available is the FlushFileBuffers on the temp file
// before its atomic rename; the NTFS rename is atomic. DirSyncSupported()
// reports this capability explicitly.
func syncDir(string) error { return nil }
