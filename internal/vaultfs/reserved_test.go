package vaultfs

import (
	"errors"
	"testing"
)

func TestNoteWritesRejectReservedDirectories(t *testing.T) {
	r := newTestRepo(t)
	for _, folder := range []string{ReservedVaultDir, SyncMetadataDir, "sub/" + SyncMetadataDir} {
		if _, err := r.Create(CreateOptions{Name: "a", Folder: folder, Content: "x"}); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Create folder %q: err = %v, want ErrInvalidPath", folder, err)
		}
		if _, err := r.CreateMarkdown("a", folder, "# x"); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("CreateMarkdown folder %q: err = %v, want ErrInvalidPath", folder, err)
		}
	}
}

func TestMoveDuplicateRejectReservedDirectories(t *testing.T) {
	r := newTestRepo(t)
	if _, err := r.Create(CreateOptions{Name: "a", Content: "x"}); err != nil {
		t.Fatal(err)
	}
	for _, dst := range []string{ReservedVaultDir, SyncMetadataDir} {
		if _, err := r.Move("a.md", dst); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Move to %q: err = %v, want ErrInvalidPath", dst, err)
		}
		if _, err := r.CreateMarkdown("b", dst, "# b"); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("CreateMarkdown into %q: err = %v, want ErrInvalidPath", dst, err)
		}
	}
	// A note duplicated from a normal path still works.
	if _, err := r.Duplicate("a.md"); err != nil {
		t.Fatalf("duplicate of a normal note failed: %v", err)
	}
}

func TestUpdateRejectsReservedDestination(t *testing.T) {
	r := newTestRepo(t)
	n, err := r.Create(CreateOptions{Name: "a", Content: "x"})
	if err != nil {
		t.Fatal(err)
	}
	memodump := SyncMetadataDir
	if _, err := r.Update("a.md", UpdateOptions{Destination: &memodump, BaseRevision: n.Revision}); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("update into .memodump: err = %v, want ErrInvalidPath", err)
	}
	// Moving an existing note into .memodump via rename is also blocked.
	images := ReservedVaultDir
	if _, err := r.Update("a.md", UpdateOptions{Rename: strPtr("b"), Destination: &images}); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("rename into .images: err = %v, want ErrInvalidPath", err)
	}
}

func TestReadDeleteApplyRejectReserved(t *testing.T) {
	r := newTestRepo(t)
	reserved := SyncMetadataDir + "/x.md"
	if _, err := r.Get(reserved, true); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("Get(%q): err = %v, want ErrInvalidPath", reserved, err)
	}
	if _, err := r.ListNotes(SyncMetadataDir); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("ListNotes(.memodump): err = %v, want ErrInvalidPath", err)
	}
	if err := r.Delete(reserved, ""); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("Delete(%q): err = %v, want ErrInvalidPath", reserved, err)
	}
	if _, err := r.Apply(reserved, "# x", ""); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("Apply(%q): err = %v, want ErrInvalidPath", reserved, err)
	}
	if err := r.DeleteFolder(SyncMetadataDir); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("DeleteFolder(.memodump): err = %v, want ErrInvalidPath", err)
	}
}

func TestFolderOpsRejectReservedDirectories(t *testing.T) {
	r := newTestRepo(t)
	for _, name := range []string{SyncMetadataDir, ReservedVaultDir} {
		if err := r.CreateFolder(name); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("CreateFolder(%q): err = %v, want ErrInvalidPath", name, err)
		}
	}
	// Existing valid folder tree unaffected.
	if err := r.CreateFolder("ok"); err != nil {
		t.Fatal(err)
	}
	if err := r.RenameFolder("ok", "fine"); err != nil {
		t.Fatal(err)
	}
	if err := r.MoveFolder("fine", "sub"); err != nil {
		t.Fatal(err)
	}
}

func TestIsSyncMetadataDir(t *testing.T) {
	if !IsSyncMetadataDir(SyncMetadataDir) {
		t.Fatal("IsSyncMetadataDir(.memodump) = false")
	}
	// Case variants must match: on Windows/default macOS they are the same dir.
	for _, variant := range []string{".MEMODUMP", ".Memodump", ".MEMODUMP"} {
		if !IsSyncMetadataDir(variant) {
			t.Fatalf("IsSyncMetadataDir(%q) = false, want true", variant)
		}
	}
	if IsSyncMetadataDir("memodump") || IsSyncMetadataDir(".memodump.txt") || IsSyncMetadataDir("") {
		t.Fatal("IsSyncMetadataDir matched a non-exact name")
	}
	// The images vault is not sync metadata.
	if IsSyncMetadataDir(".images") {
		t.Fatal("IsSyncMetadataDir(.images) = true")
	}
}

func TestContainsReservedSegmentIncludesSyncMetadata(t *testing.T) {
	if !ContainsReservedSegment(SyncMetadataDir) {
		t.Fatal(".memodump is not reserved")
	}
	if !ContainsReservedSegment("x/" + SyncMetadataDir + "/y") {
		t.Fatal("nested .memodump is not reserved")
	}
	if !ContainsReservedSegment(ReservedVaultDir) {
		t.Fatal(".images is not reserved")
	}
	// Case variants must not bypass the reserved check.
	for _, variant := range []string{".MEMODUMP", "x/.Memodump/note.md", ".IMAGES"} {
		if !ContainsReservedSegment(variant) {
			t.Fatalf("case variant %q bypassed the reserved check", variant)
		}
	}
	if ContainsReservedSegment("memodump/x.md") || ContainsReservedSegment("") {
		t.Fatal("non-reserved segment rejected")
	}
}
