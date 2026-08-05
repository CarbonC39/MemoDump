package vaultfs

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ReservedVaultDir is the dot-prefixed directory inside the data dir that the
// note repository treats as reserved (the Feature 1 image vault). User folders
// can never shadow it, and listings hide dot-prefixed entries.
const ReservedVaultDir = ".images"

// SafePath prevents directory traversal attacks by confining the resolved path
// to base. base is expected to be absolute (the repository root is made
// absolute at construction), so the returned path is absolute too.
//
// Symlinks are deliberately not resolved here: filepath.EvalSymlinks fails for
// paths that don't exist yet (every note/folder creation hits that case), and it
// would also reject a perfectly valid data dir that itself sits behind a symlink
// (e.g. macOS /tmp -> /private/tmp). The repository never creates symlinks, so
// the only way one could appear inside the data dir is via direct filesystem
// access, which is already a trusted boundary. A lexical containment check is
// what matters for blocking "../" traversal.
func SafePath(base string, userPath string) (string, error) {
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("failed to resolve base path: %w", err)
	}

	// filepath.Join cleans the result and neutralizes a leading "/" in userPath.
	absFull := filepath.Join(absBase, filepath.FromSlash(userPath))

	rel, err := filepath.Rel(absBase, absFull)
	if err != nil {
		return "", fmt.Errorf("path out of bounds: %s", userPath)
	}
	// rel == ".." means the parent; a "../" prefix means it escaped base. The
	// trailing separator avoids a false positive on a real file named "..foo".
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path out of bounds: %s", userPath)
	}

	return absFull, nil
}

// ContainsReservedSegment reports whether any path segment is a reserved
// repository directory name (e.g. the image vault), so user folders can never
// shadow it.
func ContainsReservedSegment(rel string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == ReservedVaultDir {
			return true
		}
	}
	return false
}

// truncateRunes returns s clamped to at most max runes, never splitting a
// multi-byte UTF-8 character mid-rune (a plain s[:max] byte slice can).
func truncateRunes(s string, max int) (string, bool) {
	if max <= 0 {
		return "", false
	}
	if utf8.RuneCountInString(s) <= max {
		return s, false
	}
	count := 0
	for byteIdx := range s {
		if count == max {
			return s[:byteIdx], true
		}
		count++
	}
	return s, false
}

var windowsReservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// SanitizeName makes a user-supplied note name safe on Windows and POSIX,
// limited to 200 code points, with one ".md" suffix applied by the caller.
// It returns "" for a name that sanitizes to nothing.
func SanitizeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20, r == 0x7F,
			r == '/', r == '\\', r == ':',
			r == '*', r == '?', r == '"',
			r == '<', r == '>', r == '|':
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}

	result, _ := truncateRunes(b.String(), 200)
	result = strings.Trim(result, " .")
	if result == "" {
		return ""
	}

	ext := filepath.Ext(result)
	baseWithoutExt := strings.ToUpper(strings.TrimSuffix(result, ext))
	if windowsReservedNames[baseWithoutExt] {
		result = "_" + result
	}
	return result
}
