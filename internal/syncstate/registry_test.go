package syncstate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func mustVaultID(t *testing.T) string {
	t.Helper()
	return uuid.NewString()
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			copyTree(t, s, d)
		} else {
			data, err := os.ReadFile(s)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(d, data, 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestResolveIsStableAcrossRestart(t *testing.T) {
	stateRoot := t.TempDir()
	vault := t.TempDir()
	vaultID := mustVaultID(t)

	dev1, rep1, err := Resolve(stateRoot, vault, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	dev2, rep2, err := Resolve(stateRoot, vault, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	if dev1 != dev2 {
		t.Fatalf("device changed across restart: %s -> %s", dev1, dev2)
	}
	if rep1 != rep2 {
		t.Fatalf("replica changed across restart: %s -> %s", rep1, rep2)
	}
	// device.json exists and is durable.
	if _, err := os.Stat(filepath.Join(stateRoot, "device.json")); err != nil {
		t.Fatalf("device.json missing: %v", err)
	}
}

func TestCopiedVaultGetsNewReplica(t *testing.T) {
	stateRoot := t.TempDir()
	vaultA := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultA, "a.md"), []byte("# A"), 0644); err != nil {
		t.Fatal(err)
	}
	vaultB := filepath.Join(t.TempDir(), "copy")
	copyTree(t, vaultA, vaultB)

	vaultID := mustVaultID(t)
	_, repA, err := Resolve(stateRoot, vaultA, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	// A second live path with the same Vault ID must never share device state.
	_, repB, err := Resolve(stateRoot, vaultB, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	if repA == repB {
		t.Fatalf("copied vault shares the replica %s", repA)
	}
	// Both stay stable on their own reopen.
	if _, repA2, err := Resolve(stateRoot, vaultA, vaultID); err != nil || repA2 != repA {
		t.Fatalf("original replica not stable: %s (%v)", repA2, err)
	}
	if _, repB2, err := Resolve(stateRoot, vaultB, vaultID); err != nil || repB2 != repB {
		t.Fatalf("copy replica not stable: %s (%v)", repB2, err)
	}
}

func TestUnambiguousMoveReassociatesReplica(t *testing.T) {
	stateRoot := t.TempDir()
	vaultA := t.TempDir()
	vaultID := mustVaultID(t)

	_, repA, err := Resolve(stateRoot, vaultA, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	vaultB := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(vaultA, vaultB); err != nil {
		t.Fatal(err)
	}
	// The old path no longer exists: the single missing registration is a move,
	// so the replica is re-associated, not duplicated.
	_, repB, err := Resolve(stateRoot, vaultB, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	if repA != repB {
		t.Fatalf("move created a new replica %s instead of re-associating %s", repB, repA)
	}
}

func TestAmbiguousMoveGetsNewReplica(t *testing.T) {
	stateRoot := t.TempDir()
	vaultA := t.TempDir()
	vaultB := t.TempDir()
	vaultC := t.TempDir()
	vaultID := mustVaultID(t)

	_, repA, err := Resolve(stateRoot, vaultA, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	_, repB, err := Resolve(stateRoot, vaultB, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	// Both original copies disappear (e.g. deleted independently). Opening a
	// third path is ambiguous — two missing candidates — so it must get a fresh
	// replica rather than arbitrarily reusing one.
	if err := os.RemoveAll(vaultA); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(vaultB); err != nil {
		t.Fatal(err)
	}
	_, repC, err := Resolve(stateRoot, vaultC, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	if repC == repA || repC == repB {
		t.Fatalf("ambiguous move reused replica %s", repC)
	}
}

func TestConcurrentResolveIsDeterministic(t *testing.T) {
	stateRoot := t.TempDir()
	vault := t.TempDir()
	vaultID := mustVaultID(t)

	const n = 8
	type result struct {
		dev DeviceID
		rep ReplicaID
	}
	results := make(chan result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dev, rep, err := Resolve(stateRoot, vault, vaultID)
			if err != nil {
				t.Errorf("resolve: %v", err)
				return
			}
			results <- result{dev, rep}
		}()
	}
	wg.Wait()
	close(results)

	first := <-results
	for r := range results {
		if r.dev != first.dev || r.rep != first.rep {
			t.Fatalf("concurrent resolves disagreed: %+v vs %+v", r, first)
		}
	}
}

func TestLiveCopyAtSecondPathIsNotAMove(t *testing.T) {
	stateRoot := t.TempDir()
	vaultA := t.TempDir()
	vaultB := t.TempDir()
	vaultID := mustVaultID(t)

	_, repA, err := Resolve(stateRoot, vaultA, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	// vaultA still exists, so opening vaultB is a copy, never a move.
	_, repB, err := Resolve(stateRoot, vaultB, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	if repA == repB {
		t.Fatalf("second live copy reused the original replica")
	}
}

func TestMissingAppDataCreatesFreshNonDestructiveReplica(t *testing.T) {
	vault := t.TempDir()
	vaultID := mustVaultID(t)
	stateRoot := t.TempDir() // fresh: no device.json, no registry

	dev, rep, err := Resolve(stateRoot, vault, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	if string(dev) == "" || string(rep) == "" {
		t.Fatal("empty identity from missing AppData")
	}
	// The vault must remain untouched — no .memodump, no note changes.
	if _, err := os.Stat(filepath.Join(vault, ".memodump")); !os.IsNotExist(err) {
		t.Fatalf("missing-AppData resolution created vault metadata")
	}
}

func TestDifferentVaultAtKnownPathGetsNewReplica(t *testing.T) {
	stateRoot := t.TempDir()
	vault := t.TempDir()

	_, repA, err := Resolve(stateRoot, vault, mustVaultID(t))
	if err != nil {
		t.Fatal(err)
	}
	_, repB, err := Resolve(stateRoot, vault, mustVaultID(t))
	if err != nil {
		t.Fatal(err)
	}
	if repA == repB {
		t.Fatal("two different vaults at one path must not share a replica")
	}
}

func TestSymlinkedVaultPathMapsToSameReplica(t *testing.T) {
	stateRoot := t.TempDir()
	vault := t.TempDir()
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "vault")
	if err := os.Symlink(vault, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	vaultID := mustVaultID(t)

	_, repReal, err := Resolve(stateRoot, vault, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	// The symlinked spelling must resolve to the same physical replica.
	_, repLink, err := Resolve(stateRoot, link, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	if repReal != repLink {
		t.Fatalf("symlinked path got a separate replica %s", repLink)
	}
}

func TestRegistryReplicasNullDegradesToEmpty(t *testing.T) {
	stateRoot := t.TempDir()
	vault := t.TempDir()
	if err := os.MkdirAll(stateRoot, 0755); err != nil {
		t.Fatal(err)
	}
	// A registry document with replicas:null must not panic resolve; it degrades
	// to an empty registry and assigns a fresh replica.
	doc := fmt.Sprintf(`{"version":1,"replicas":null,"vaultId":"%s"}`, mustVaultID(t))
	if err := os.WriteFile(filepath.Join(stateRoot, "registry.json"), []byte(doc), 0600); err != nil {
		t.Fatal(err)
	}
	if _, rep, err := Resolve(stateRoot, vault, mustVaultID(t)); err != nil || string(rep) == "" {
		t.Fatalf("resolve with replicas:null = %q (%v)", rep, err)
	}
}

func TestCorruptRegistryStartsFresh(t *testing.T) {
	stateRoot := t.TempDir()
	vault := t.TempDir()
	if err := os.MkdirAll(stateRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateRoot, "registry.json"), []byte("{corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	dev, rep, err := Resolve(stateRoot, vault, mustVaultID(t))
	if err != nil {
		t.Fatalf("corrupt registry must not block sync enablement: %v", err)
	}
	if string(dev) == "" || string(rep) == "" {
		t.Fatal("corrupt registry produced an empty identity")
	}
}

func TestValidRegistryEntriesSurviveTampering(t *testing.T) {
	stateRoot := t.TempDir()
	vault := t.TempDir()
	vaultID := mustVaultID(t)
	_, rep, err := Resolve(stateRoot, vault, vaultID)
	if err != nil {
		t.Fatal(err)
	}

	// Tamper: add an invalid entry. Resolution of the valid vault must reuse its
	// replica; only the invalid entry is dropped.
	reg, err := loadRegistry(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	reg.Replicas["/not/absolute"] = registryEntry{VaultID: "junk", ReplicaID: "junk"}
	if err := reg.save(stateRoot); err != nil {
		t.Fatal(err)
	}

	dev2, rep2, err := Resolve(stateRoot, vault, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	if dev2 == "" || rep2 != rep {
		t.Fatalf("tampered registry changed the valid replica: %s -> %s", rep, rep2)
	}
}

func TestResolveRejectsBadVaultID(t *testing.T) {
	if _, _, err := Resolve(t.TempDir(), t.TempDir(), "not-a-uuid"); err == nil {
		t.Fatal("non-UUID vaultId accepted")
	}
}

func TestDefaultStateRoot(t *testing.T) {
	dir, err := DefaultStateRoot()
	if err != nil {
		t.Skipf("no user config dir: %v", err)
	}
	if dir == "" || !filepath.IsAbs(dir) {
		t.Fatalf("DefaultStateRoot = %q", dir)
	}
	if !strings.HasSuffix(dir, filepath.Join("memodump", "sync")) {
		t.Fatalf("DefaultStateRoot = %q, want <config>/memodump/sync", dir)
	}
}
