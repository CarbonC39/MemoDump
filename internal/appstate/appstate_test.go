package appstate

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeEnv(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseEnvFile(t *testing.T) {
	path := writeEnv(t, strings.Join([]string{
		"# comment",
		"",
		"KEY=value",
		"  SPACED  =  trimmed  ",
		"EQUAL=a=b=c",
		"QUOTED=\"hello world\"",
		"SINGLE='single value'",
		"COMMENT=hello # trailing",
		"NOCOMMENT=#fff",
		"EMPTY=",
		"NOTAGS",
	}, "\n"))

	got := ParseEnvFile(path)
	want := map[string]string{
		"KEY":       "value",
		"SPACED":    "trimmed",
		"EQUAL":     "a=b=c",
		"QUOTED":    "hello world",
		"SINGLE":    "single value",
		"COMMENT":   "hello",
		"NOCOMMENT": "#fff",
		"EMPTY":     "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseEnvFile = %#v, want %#v", got, want)
	}
}

func TestParseEnvFileMissingFile(t *testing.T) {
	if got := ParseEnvFile(filepath.Join(t.TempDir(), "nope.env")); len(got) != 0 {
		t.Fatalf("ParseEnvFile(missing) = %#v, want empty", got)
	}
}

func TestParseEnvFileCRLFAndQuoteQuirk(t *testing.T) {
	// CRLF line endings are trimmed; a value that is quoted but has a trailing
	// comment keeps its quotes (it is not a pure quoted string, so the inline
	// comment is stripped and the quotes are left verbatim).
	path := writeEnv(t, "WINDOWS=ok\r\nMIXED=\"a\" # note")
	got := ParseEnvFile(path)
	if got["WINDOWS"] != "ok" {
		t.Fatalf("WINDOWS = %q, want %q", got["WINDOWS"], "ok")
	}
	if got["MIXED"] != `"a"` {
		t.Fatalf("MIXED = %q, want %q", got["MIXED"], `"a"`)
	}
}
