package syncstate

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestLockAcquireAndRelease(t *testing.T) {
	stateRoot := t.TempDir()
	vaultID, replicaID := mustVaultID(t), mustVaultID(t)

	l, err := AcquireReplicaLock(stateRoot, vaultID, replicaID)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	// After release a new acquisition succeeds.
	l2, err := AcquireReplicaLock(stateRoot, vaultID, replicaID)
	if err != nil {
		t.Fatalf("reacquire after release failed: %v", err)
	}
	if err := l2.Close(); err != nil {
		t.Fatal(err)
	}
	// The state directory now exists on disk.
	if _, err := os.Stat(StateDir(stateRoot, vaultID, replicaID)); err != nil {
		t.Fatalf("state dir missing: %v", err)
	}
}

func TestLockSecondHandleIsRejected(t *testing.T) {
	// flock treats each open file description independently, and LockFileEx
	// rejects an overlapping range from a second handle, so a second handle —
	// even in this process — is a faithful stand-in for a second process.
	stateRoot := t.TempDir()
	vaultID, replicaID := mustVaultID(t), mustVaultID(t)

	l, err := AcquireReplicaLock(stateRoot, vaultID, replicaID)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	if _, err := AcquireReplicaLock(stateRoot, vaultID, replicaID); !errors.Is(err, ErrLocked) {
		t.Fatalf("second acquire = %v, want ErrLocked", err)
	}
}

func TestLockRejectsPathTraversalIDs(t *testing.T) {
	if _, err := AcquireReplicaLock(t.TempDir(), "../evil", "x"); err == nil {
		t.Fatal("non-UUID vaultId accepted")
	}
	if _, err := AcquireReplicaLock(t.TempDir(), mustVaultID(t), "x"); err == nil {
		t.Fatal("non-UUID replicaId accepted")
	}
}

// TestLockTwoProcessesCannotOwnOneStateDir is the authoritative cross-process
// test: a child process acquires the replica lock, then this process must be
// refused. The child releases on kill.
func TestLockTwoProcessesCannotOwnOneStateDir(t *testing.T) {
	stateRoot := t.TempDir()
	vaultID, replicaID := mustVaultID(t), mustVaultID(t)
	lockPath := LockPath(stateRoot, vaultID, replicaID)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		t.Fatal(err)
	}

	ready := filepath.Join(t.TempDir(), "ready")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=TestLockHelperChild")
	cmd.Env = append(os.Environ(),
		"MEMODUMP_LOCK_HELPER=1",
		"MEMODUMP_LOCK_PATH="+lockPath,
		"MEMODUMP_LOCK_READY="+ready,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	waitForFile(t, ready, 5*time.Second)

	if _, err := AcquireReplicaLock(stateRoot, vaultID, replicaID); !errors.Is(err, ErrLocked) {
		t.Fatalf("second process acquire = %v, want ErrLocked", err)
	}
}

// TestRegistryLockCrossProcess proves two first-starting processes agree on one
// Replica ID. The child acquires the registry lock, signals it holds it, then
// commits a registry entry and waits to be told to release. This process starts
// Resolve only after seeing "locked", so without the cross-process lock it
// would read the empty registry and mint a different Replica ID. With the lock
// it must block until the child's commit is durable and return that Replica ID.
func TestRegistryLockCrossProcess(t *testing.T) {
	stateRoot := t.TempDir()
	vault := t.TempDir()
	vaultID := mustVaultID(t)
	childReplica := ReplicaID(mustVaultID(t))

	locked := filepath.Join(t.TempDir(), "locked")
	release := filepath.Join(t.TempDir(), "release")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=TestRegistryLockHelperChild")
	cmd.Env = append(os.Environ(),
		"MEMODUMP_REGISTRY_HELPER=1",
		"MEMODUMP_REGISTRY_ROOT="+stateRoot,
		"MEMODUMP_REGISTRY_VAULT="+vault,
		"MEMODUMP_REGISTRY_VAULT_ID="+vaultID,
		"MEMODUMP_REGISTRY_REPLICA="+string(childReplica),
		"MEMODUMP_REGISTRY_LOCKED="+locked,
		"MEMODUMP_REGISTRY_RELEASE="+release,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	waitForFile(t, locked, 5*time.Second)

	// Resolve must block on the registry lock while the child holds it. A
	// Resolve takes microseconds, so an unbounded-resolve within a 200 ms window
	// proves the cross-process lock is not honored.
	done := make(chan ReplicaID, 1)
	go func() {
		_, rep, err := Resolve(stateRoot, vault, vaultID)
		if err != nil {
			t.Errorf("resolve: %v", err)
			done <- ""
			return
		}
		done <- rep
	}()
	select {
	case rep := <-done:
		t.Fatalf("Resolve completed (%s) while the registry lock was held", rep)
	case <-time.After(200 * time.Millisecond):
		// blocked as expected
	}

	// Release the child, then Resolve must observe its committed replica.
	if err := os.WriteFile(release, []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case rep := <-done:
		if rep != childReplica {
			t.Fatalf("parent resolved replica %s, want child's %s (registry RMW not serialized)", rep, childReplica)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Resolve never completed after the lock was released")
	}
}

// TestRegistryLockHelperChild is only meaningful when re-executed by
// TestRegistryLockCrossProcess. It holds the registry lock, commits a registry
// entry, and releases only when told to.
func TestRegistryLockHelperChild(t *testing.T) {
	if os.Getenv("MEMODUMP_REGISTRY_HELPER") != "1" {
		return
	}
	l, err := acquireRegistryLock(os.Getenv("MEMODUMP_REGISTRY_ROOT"))
	if err != nil {
		os.Exit(1)
	}
	defer l.Close()

	key, err := canonicalVaultKey(os.Getenv("MEMODUMP_REGISTRY_VAULT"))
	if err != nil {
		os.Exit(2)
	}
	reg := newRegistry()
	reg.Replicas[key] = registryEntry{
		VaultID:   os.Getenv("MEMODUMP_REGISTRY_VAULT_ID"),
		ReplicaID: ReplicaID(os.Getenv("MEMODUMP_REGISTRY_REPLICA")),
	}
	if err := reg.save(os.Getenv("MEMODUMP_REGISTRY_ROOT")); err != nil {
		os.Exit(3)
	}
	// Signal that we hold the lock. The parent starts Resolve only now; the
	// flock (not this file) is what orders the parent's read after our commit.
	if err := os.WriteFile(os.Getenv("MEMODUMP_REGISTRY_LOCKED"), []byte("ok"), 0600); err != nil {
		os.Exit(4)
	}
	// Hold until the parent asks for release.
	release := os.Getenv("MEMODUMP_REGISTRY_RELEASE")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(release); err == nil {
			return // defer releases the lock
		}
		if time.Now().After(deadline) {
			os.Exit(5)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLockHelperChild is only meaningful when re-executed by the two-process
// test: it acquires the lock, signals readiness, and holds it until killed.
func TestLockHelperChild(t *testing.T) {
	if os.Getenv("MEMODUMP_LOCK_HELPER") != "1" {
		return
	}
	f, err := os.OpenFile(os.Getenv("MEMODUMP_LOCK_PATH"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		os.Exit(1)
	}
	if err := tryLock(f); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(os.Getenv("MEMODUMP_LOCK_READY"), []byte("ok"), 0600); err != nil {
		os.Exit(3)
	}
	select {} // hold until killed
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
