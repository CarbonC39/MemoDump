package syncrun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncstate"
	"memodump/internal/vaultfs"
)

const (
	harnessVaultID  = "11111111-1111-4111-8111-111111111111"
	harnessRepoID   = "33333333-3333-4333-8333-333333333333"
	harnessProfile  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	harnessDevice   = "1a2b3c4d-1111-4222-8333-444455556666"
	harnessReplicaA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	harnessReplicaB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	harnessSyncID   = "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"
)

type rep struct {
	root      string
	stateRoot string
	idx       *syncindex.Store
	co        *Coordinator
	lock      *syncstate.Lock
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, md := range files {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(md), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func newReplica(t *testing.T, root, stateRoot, replicaID, vaultID string, remote cloudsync.RemoteStore) *rep {
	t.Helper()
	repo, err := vaultfs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := syncindex.Load(root)
	if err != nil {
		t.Fatalf("load index at %s: %v", root, err)
	}
	snaps, err := syncstate.NewSnapshotStore(stateRoot, vaultID, replicaID)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := syncstate.NewRecoveryStore(stateRoot, vaultID, replicaID)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := syncstate.AcquireReplicaLock(stateRoot, vaultID, replicaID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	co := New(repo, idx, snaps, rec, remote, Config{
		VaultID: vaultID, ReplicaID: replicaID, RepoID: harnessRepoID,
		Profile: harnessProfile, UpdatedBy: harnessDevice, Clock: func() int64 { return 1785800000000 },
	})
	return &rep{root: root, stateRoot: stateRoot, idx: idx, co: co, lock: lock}
}

func (r *rep) readNote(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func (r *rep) noteExists(path string) bool {
	_, err := os.Stat(filepath.Join(r.root, filepath.FromSlash(path)))
	return err == nil
}

func hasWork(st *Status) bool {
	for _, d := range st.Decisions {
		switch d.Kind {
		case cloudsync.DecisionNoop, cloudsync.DecisionBlock, cloudsync.DecisionRetry, cloudsync.DecisionRepairIndex:
			continue
		default:
			return true
		}
	}
	return false
}

func (r *rep) runQuiescent(t *testing.T, ctx context.Context) {
	t.Helper()
	for i := 0; i < 20; i++ {
		st, err := r.co.Run(ctx)
		if err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
		if !hasWork(st) {
			return
		}
	}
	t.Fatal("replica did not converge within 20 cycles")
}

func entityInRemote(t *testing.T, remote *cloudsync.MemoryStore, syncID string) *cloudsync.Entity {
	t.Helper()
	data, _, err := remote.Read(context.Background(), cloudsync.EntityKeyPrefix+syncID+".json")
	if err != nil {
		t.Fatalf("remote read %s: %v", syncID, err)
	}
	ent, err := cloudsync.ParseEntity(data)
	if err != nil {
		t.Fatalf("remote entity %s: %v", syncID, err)
	}
	return ent
}

func TestCoordinatorFirstLocalUpload(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"idea.md": "# Local\n"})
	idx, err := syncindex.Enable(root)
	if err != nil {
		t.Fatal(err)
	}
	syncID, ok := idx.FindByPath("idea.md")
	if !ok {
		t.Fatal("note not indexed at enable")
	}
	remote := cloudsync.NewMemoryStore()
	r := newReplica(t, root, t.TempDir(), harnessReplicaA, harnessVaultID, remote)
	r.runQuiescent(t, context.Background())

	ent := entityInRemote(t, remote, syncID)
	if ent.Deleted || ent.Name != "idea" || !strings.Contains(ent.Markdown, "# Local") {
		t.Fatalf("remote entity = %+v", ent)
	}
}

func TestCoordinatorFirstRemoteDownload(t *testing.T) {
	root := t.TempDir()
	remote := cloudsync.NewMemoryStore()
	remoteEnt := mkEntity(harnessSyncID, "idea", "", "# Remote\n")
	data, err := remoteEnt.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Create(context.Background(), cloudsync.EntityKeyPrefix+harnessSyncID+".json", data); err != nil {
		t.Fatal(err)
	}
	if _, err := syncindex.Create(root, harnessVaultID); err != nil {
		t.Fatal(err)
	}
	r := newReplica(t, root, t.TempDir(), harnessReplicaA, harnessVaultID, remote)
	r.runQuiescent(t, context.Background())

	got := r.readNote(t, "idea.md")
	if !strings.Contains(got, "# Remote") {
		t.Fatalf("pulled note = %q", got)
	}
	if _, ok := r.idx.FindBySyncID(harnessSyncID); !ok {
		t.Fatal("pulled entity not indexed")
	}
}

func TestCoordinatorLocalDeletePushesTombstone(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"idea.md": "# v1\n"})
	idx, err := syncindex.Enable(root)
	if err != nil {
		t.Fatal(err)
	}
	syncID, _ := idx.FindByPath("idea.md")

	remote := cloudsync.NewMemoryStore()
	v1 := mkEntity(syncID, "idea", "", "# v1\n")
	remoteData, _ := v1.Serialize()
	version, err := remote.Create(context.Background(), cloudsync.EntityKeyPrefix+syncID+".json", remoteData)
	if err != nil {
		t.Fatal(err)
	}

	// Seed a matching baseline at the replica's state root so the engine knows
	// the note existed.
	stateRoot := t.TempDir()
	snaps, _ := syncstate.NewSnapshotStore(stateRoot, harnessVaultID, harnessReplicaA)
	snap := &syncstate.Snapshot{
		SchemaVersion: syncstate.SnapshotSchemaVersion, VaultID: harnessVaultID,
		ReplicaID: harnessReplicaA, RepositoryID: harnessRepoID, ProviderProfile: harnessProfile,
		Entities: map[string]syncstate.SnapshotEntity{
			syncID: {ContentHash: v1.ContentHash, RemoteVersion: version},
		},
	}
	if err := snaps.Replace(snap); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "idea.md")); err != nil {
		t.Fatal(err)
	}
	r := newReplica(t, root, stateRoot, harnessReplicaA, harnessVaultID, remote)
	r.runQuiescent(t, context.Background())

	ent := entityInRemote(t, remote, syncID)
	if !ent.Deleted {
		t.Fatalf("expected a remote tombstone, got %+v", ent)
	}
}

func TestCoordinatorRemoteTombstoneRecoversThenDeletes(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"idea.md": "# v1\n"})
	idx, err := syncindex.Enable(root)
	if err != nil {
		t.Fatal(err)
	}
	syncID, _ := idx.FindByPath("idea.md")

	remote := cloudsync.NewMemoryStore()
	v1 := mkEntity(syncID, "idea", "", "# v1\n")
	remoteData, _ := v1.Serialize()
	version, err := remote.Create(context.Background(), cloudsync.EntityKeyPrefix+syncID+".json", remoteData)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the live record with a tombstone remotely.
	tomb := mkEntity(syncID, "idea", "", "# v1\n")
	tomb.Deleted = true
	tombData, _ := tomb.Serialize()
	if _, err := remote.Replace(context.Background(), cloudsync.EntityKeyPrefix+syncID+".json", tombData, version); err != nil {
		t.Fatal(err)
	}

	stateRoot := t.TempDir()
	snaps, _ := syncstate.NewSnapshotStore(stateRoot, harnessVaultID, harnessReplicaA)
	snap := &syncstate.Snapshot{
		SchemaVersion: syncstate.SnapshotSchemaVersion, VaultID: harnessVaultID,
		ReplicaID: harnessReplicaA, RepositoryID: harnessRepoID, ProviderProfile: harnessProfile,
		Entities: map[string]syncstate.SnapshotEntity{
			syncID: {ContentHash: v1.ContentHash, RemoteVersion: version},
		},
	}
	if err := snaps.Replace(snap); err != nil {
		t.Fatal(err)
	}

	r := newReplica(t, root, stateRoot, harnessReplicaA, harnessVaultID, remote)
	r.runQuiescent(t, context.Background())

	if r.noteExists("idea.md") {
		t.Fatal("remote tombstone did not delete the local note")
	}
	// The recovery copy was written before the delete.
	rec, err := syncstate.NewRecoveryStore(stateRoot, harnessVaultID, harnessReplicaA)
	if err != nil {
		t.Fatal(err)
	}
	md, ok, err := rec.Read(syncID, cloudsync.StateHash(v1.ContentHash, false))
	if err != nil || !ok {
		t.Fatalf("recovery missing: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(md, "# v1") {
		t.Fatalf("recovered markdown = %q", md)
	}
}

func TestCoordinatorDivergentEditsKeepBoth(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"idea.md": "# local edit\n"})
	idx, err := syncindex.Enable(root)
	if err != nil {
		t.Fatal(err)
	}
	syncID, _ := idx.FindByPath("idea.md")

	remote := cloudsync.NewMemoryStore()
	base := mkEntity(syncID, "idea", "", "# base\n")
	remoteEdit := mkEntity(syncID, "idea", "", "# remote edit\n")
	baseData, _ := base.Serialize()
	version, err := remote.Create(context.Background(), cloudsync.EntityKeyPrefix+syncID+".json", baseData)
	if err != nil {
		t.Fatal(err)
	}
	remoteEditData, _ := remoteEdit.Serialize()
	if _, err := remote.Replace(context.Background(), cloudsync.EntityKeyPrefix+syncID+".json", remoteEditData, version); err != nil {
		t.Fatal(err)
	}

	stateRoot := t.TempDir()
	snaps, _ := syncstate.NewSnapshotStore(stateRoot, harnessVaultID, harnessReplicaA)
	snap := &syncstate.Snapshot{
		SchemaVersion: syncstate.SnapshotSchemaVersion, VaultID: harnessVaultID,
		ReplicaID: harnessReplicaA, RepositoryID: harnessRepoID, ProviderProfile: harnessProfile,
		Entities: map[string]syncstate.SnapshotEntity{
			syncID: {ContentHash: base.ContentHash, RemoteVersion: version},
		},
	}
	if err := snaps.Replace(snap); err != nil {
		t.Fatal(err)
	}

	r := newReplica(t, root, stateRoot, harnessReplicaA, harnessVaultID, remote)
	r.runQuiescent(t, context.Background())

	// The original accepts the remote edit; the local edit survives as a
	// deterministic conflict copy.
	original := r.readNote(t, "idea.md")
	if !strings.Contains(original, "# remote edit") {
		t.Fatalf("original note = %q, want the remote edit", original)
	}
	// Exactly one conflict copy exists locally and remotely with the local edit.
	conflictCount := 0
	var conflictPath string
	for id, e := range r.idx.Index.Entities {
		if id != syncID {
			conflictCount++
			conflictPath = e.Path
		}
	}
	if conflictCount != 1 {
		t.Fatalf("index conflict count = %d, want 1", conflictCount)
	}
	if !strings.Contains(r.readNote(t, conflictPath), "# local edit") {
		t.Fatalf("conflict copy lost the local edit: %q", r.readNote(t, conflictPath))
	}
}

func mkEntity(syncID, name, parent, markdown string) *cloudsync.Entity {
	e := &cloudsync.Entity{
		SchemaVersion: cloudsync.SchemaVersion, SyncID: syncID, Kind: cloudsync.KindNote,
		ParentID: parent, Name: name, Markdown: markdown,
		UpdatedBy: harnessDevice, UpdatedAt: 1785800000000,
	}
	e.ContentHash = e.ComputeContentHash()
	return e
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func copyIndex(t *testing.T, srcRoot, dstRoot string) {
	t.Helper()
	copyFile(t, filepath.Join(srcRoot, syncindex.DirName, syncindex.IndexName),
		filepath.Join(dstRoot, syncindex.DirName, syncindex.IndexName))
	copyFile(t, filepath.Join(srcRoot, syncindex.DirName, syncindex.BackupName),
		filepath.Join(dstRoot, syncindex.DirName, syncindex.BackupName))
}

// convergeTwo alternates single cycles on both replicas until a full round
// produces no work, proving two filesystem replicas converge through one shared
// remote.
func convergeTwo(t *testing.T, a, b *rep, ctx context.Context) {
	t.Helper()
	for i := 0; i < 15; i++ {
		sa, err := a.co.Run(ctx)
		if err != nil {
			t.Fatalf("replica A cycle %d: %v", i, err)
		}
		sb, err := b.co.Run(ctx)
		if err != nil {
			t.Fatalf("replica B cycle %d: %v", i, err)
		}
		if !hasWork(sa) && !hasWork(sb) {
			return
		}
	}
	t.Fatal("two replicas did not converge within 15 rounds")
}

func TestTwoReplicasConvergeOnCreate(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	writeFiles(t, rootA, map[string]string{"idea.md": "# shared\n"})
	idxA, err := syncindex.Enable(rootA)
	if err != nil {
		t.Fatal(err)
	}
	copyIndex(t, rootA, rootB)
	vaultID := idxA.Index.VaultID
	syncID := syncIDAtPath(t, idxA, "idea.md")
	remote := cloudsync.NewMemoryStore()
	a := newReplica(t, rootA, t.TempDir(), harnessReplicaA, vaultID, remote)
	b := newReplica(t, rootB, t.TempDir(), harnessReplicaB, vaultID, remote)

	convergeTwo(t, a, b, context.Background())

	if !b.noteExists("idea.md") {
		t.Fatal("replica B did not receive the note")
	}
	if !strings.Contains(b.readNote(t, "idea.md"), "# shared") {
		t.Fatalf("replica B note = %q", b.readNote(t, "idea.md"))
	}
	ent := entityInRemote(t, remote, syncID)
	if ent.Deleted || !strings.Contains(ent.Markdown, "# shared") {
		t.Fatalf("remote entity = %+v", ent)
	}
}

func TestTwoReplicasPropagateDeletion(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	writeFiles(t, rootA, map[string]string{"idea.md": "# shared\n"})
	idxA, err := syncindex.Enable(rootA)
	if err != nil {
		t.Fatal(err)
	}
	copyIndex(t, rootA, rootB)
	vaultID := idxA.Index.VaultID
	syncID := syncIDAtPath(t, idxA, "idea.md")
	remote := cloudsync.NewMemoryStore()
	a := newReplica(t, rootA, t.TempDir(), harnessReplicaA, vaultID, remote)
	b := newReplica(t, rootB, t.TempDir(), harnessReplicaB, vaultID, remote)

	convergeTwo(t, a, b, context.Background())

	// Replica A deletes the note; the tombstone must reach B (with a recovery
	// copy on B) and both converge to a deleted baseline.
	if err := os.Remove(filepath.Join(rootA, "idea.md")); err != nil {
		t.Fatal(err)
	}
	convergeTwo(t, a, b, context.Background())

	if b.noteExists("idea.md") {
		t.Fatal("replica B kept the note after the deletion propagated")
	}
	ent := entityInRemote(t, remote, syncID)
	if !ent.Deleted {
		t.Fatalf("remote tombstone not established: %+v", ent)
	}
}

// TestCoordinatorRestartConvergesNoWAL proves a coordinator rebuilt over the
// durable state (new index/snapshot handles) converges without any WAL or
// pending queue.
func TestCoordinatorRestartConvergesNoWAL(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"idea.md": "# v1\n"})
	idx, err := syncindex.Enable(root)
	if err != nil {
		t.Fatal(err)
	}
	vaultID := idx.Index.VaultID
	syncID, _ := idx.FindByPath("idea.md")
	remote := cloudsync.NewMemoryStore()

	// First process: seed a remote edit so a pull is required.
	remoteEdit := mkEntity(syncID, "idea", "", "# v2\n")
	remoteEditData, _ := remoteEdit.Serialize()
	if _, err := remote.Create(context.Background(), cloudsync.EntityKeyPrefix+syncID+".json", remoteEditData); err != nil {
		t.Fatal(err)
	}
	stateRoot := t.TempDir()

	// Process 1: run one cycle, then abandon the handles (simulated kill),
	// releasing the replica lock so a fresh process can take over.
	r1 := newReplica(t, root, stateRoot, harnessReplicaA, vaultID, remote)
	if _, err := r1.co.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r1.lock.Close(); err != nil {
		t.Fatal(err)
	}

	// Process 2: a fresh coordinator over the same durable state.
	r2 := newReplica(t, root, stateRoot, harnessReplicaA, vaultID, remote)
	r2.runQuiescent(t, context.Background())

	if !strings.Contains(r2.readNote(t, "idea.md"), "# v2") {
		t.Fatalf("restarted coordinator did not pull the remote edit: %q", r2.readNote(t, "idea.md"))
	}
}

func TestTwoReplicasPropagateEdit(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	writeFiles(t, rootA, map[string]string{"idea.md": "# v1\n"})
	idxA, err := syncindex.Enable(rootA)
	if err != nil {
		t.Fatal(err)
	}
	copyIndex(t, rootA, rootB)
	vaultID := idxA.Index.VaultID
	remote := cloudsync.NewMemoryStore()
	a := newReplica(t, rootA, t.TempDir(), harnessReplicaA, vaultID, remote)
	b := newReplica(t, rootB, t.TempDir(), harnessReplicaB, vaultID, remote)

	convergeTwo(t, a, b, context.Background())

	// Replica A edits the note; the change must reach B without a conflict.
	writeFiles(t, rootA, map[string]string{"idea.md": "# v2\n"})
	convergeTwo(t, a, b, context.Background())

	if !strings.Contains(b.readNote(t, "idea.md"), "# v2") {
		t.Fatalf("replica B did not receive the edit: %q", b.readNote(t, "idea.md"))
	}
	// No conflict copies were produced by a one-sided edit.
	conflicts := 0
	for id := range a.idx.Index.Entities {
		if !cloudsync.IsUUIDv4(id) {
			conflicts++
		}
	}
	if conflicts != 0 {
		t.Fatalf("one-sided edit produced %d conflict copies", conflicts)
	}
}

func TestTwoReplicasConvergeEmptyFolder(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	writeFiles(t, rootA, nil)
	if err := os.MkdirAll(filepath.Join(rootA, "docs"), 0755); err != nil {
		t.Fatal(err)
	}
	idxA, err := syncindex.Enable(rootA)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := idxA.FindByPath("docs"); !ok {
		t.Fatal("empty folder not indexed at enable")
	}
	copyIndex(t, rootA, rootB)
	vaultID := idxA.Index.VaultID
	remote := cloudsync.NewMemoryStore()
	a := newReplica(t, rootA, t.TempDir(), harnessReplicaA, vaultID, remote)
	b := newReplica(t, rootB, t.TempDir(), harnessReplicaB, vaultID, remote)

	convergeTwo(t, a, b, context.Background())

	if !b.noteExists("docs") {
		t.Fatal("replica B did not receive the empty folder")
	}
	fi, err := os.Stat(filepath.Join(rootB, "docs"))
	if err != nil || !fi.IsDir() {
		t.Fatalf("replica B docs is not a directory: %v", err)
	}
}

func syncIDAtPath(t *testing.T, idx *syncindex.Store, path string) string {
	t.Helper()
	id, ok := idx.FindByPath(path)
	if !ok {
		t.Fatalf("%s not indexed", path)
	}
	return id
}

var _ = fmt.Sprintf
