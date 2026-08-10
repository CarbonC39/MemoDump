package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"memodump/internal/cloudsync"
	"memodump/internal/syncstate"
)

// TestSyncAttemptClassification covers the scheduler classification of the one
// shared attempt boundary: disabled, locked, retryable (transient), and
// permanent (fatal) outcomes.
func TestSyncAttemptClassification(t *testing.T) {
	// Disabled: a never-enabled vault is a refusal, not an attempt.
	t.Run("disabled", func(t *testing.T) {
		dir, state := t.TempDir(), t.TempDir()
		setSyncEnv(t, dir, state, nil)
		att := runSyncAttempt(context.Background(), triggerStartup)
		if !att.disabled {
			t.Fatalf("attempt = %+v, want disabled", att)
		}
	})

	// Locked: cross-process replica-lock contention is "locked", never generic.
	t.Run("locked", func(t *testing.T) {
		dir, state := t.TempDir(), t.TempDir()
		setSyncEnv(t, dir, state, nil)
		doJSON(t, "POST", "/api/sync/enable", nil)
		vaultID, replicaID, stateRoot, err := syncIdentity()
		if err != nil {
			t.Fatal(err)
		}
		lock, err := syncstate.AcquireReplicaLock(stateRoot, vaultID, replicaID)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()

		att := runSyncAttempt(context.Background(), triggerManual)
		if !att.locked {
			t.Fatalf("attempt = %+v, want locked", att)
		}
		if att.Result == nil || att.Result.LastError != "locked" {
			t.Fatalf("attempt result = %+v, want the locked label", att.Result)
		}
	})

	// Retryable: a transient write failure defers a note (Retry > 0).
	t.Run("retryable", func(t *testing.T) {
		dir, state := t.TempDir(), t.TempDir()
		store := cloudsync.NewMemoryStore()
		setSyncEnv(t, dir, state, func() (cloudsync.RemoteStore, error) { return store, nil })
		doJSON(t, "POST", "/api/sync/enable", nil)
		if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("# one\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if att := runSyncAttempt(context.Background(), triggerManual); att.disabled || att.Result == nil || !att.Result.Synced {
			t.Fatalf("baseline attempt = %+v", att.Result)
		}
		if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("# two\n"), 0644); err != nil {
			t.Fatal(err)
		}
		store.ArmFault("replace", &cloudsync.StoreError{Kind: cloudsync.ErrRetryableTransport, Message: "flaky"})
		att := runSyncAttempt(context.Background(), triggerPeriodic)
		if !att.retryable || att.permanent {
			t.Fatalf("attempt = %+v, want retryable (not permanent)", att)
		}
		if att.Result == nil || att.Result.Retry == 0 {
			t.Fatalf("attempt result = %+v, want Retry > 0", att.Result)
		}
	})

	// Permanent: a fatal provider error pauses automatic sync.
	t.Run("permanent", func(t *testing.T) {
		dir, state := t.TempDir(), t.TempDir()
		store := cloudsync.NewMemoryStore()
		setSyncEnv(t, dir, state, func() (cloudsync.RemoteStore, error) { return store, nil })
		doJSON(t, "POST", "/api/sync/enable", nil)
		if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("# A\n"), 0644); err != nil {
			t.Fatal(err)
		}
		store.ArmFault("list", &cloudsync.StoreError{Kind: cloudsync.ErrPermission, Message: "denied"})
		att := runSyncAttempt(context.Background(), triggerStartup)
		if !att.permanent || att.retryable {
			t.Fatalf("attempt = %+v, want permanent", att)
		}
		if att.Result == nil || att.Result.LastError != "permission" {
			t.Fatalf("attempt result = %+v, want the permission label", att.Result)
		}
	})
}

// TestSyncAttemptPermanentStateClassification: corrupt local sync state, a lost
// or corrupt repo.json, and a corrupt index are permanent — they pause automatic
// sync instead of retrying every ordinary interval.
func TestSyncAttemptPermanentStateClassification(t *testing.T) {
	// A lost repo.json after enable is repository damage.
	t.Run("repo-lost", func(t *testing.T) {
		dir, state := t.TempDir(), t.TempDir()
		store := cloudsync.NewMemoryStore()
		setSyncEnv(t, dir, state, func() (cloudsync.RemoteStore, error) { return store, nil })
		doJSON(t, "POST", "/api/sync/enable", nil)
		if err := store.Remove(context.Background(), "repo.json"); err != nil {
			t.Fatal(err)
		}
		att := runSyncAttempt(context.Background(), triggerPeriodic)
		if !att.permanent || att.pauseReason != "repo-lost" {
			t.Fatalf("attempt = %+v, want permanent repo-lost", att)
		}
	})

	// A corrupt remote repo.json is permanent.
	t.Run("repo-invalid", func(t *testing.T) {
		dir, state := t.TempDir(), t.TempDir()
		store := cloudsync.NewMemoryStore()
		setSyncEnv(t, dir, state, func() (cloudsync.RemoteStore, error) { return store, nil })
		doJSON(t, "POST", "/api/sync/enable", nil)
		if err := store.Remove(context.Background(), "repo.json"); err != nil {
			t.Fatal(err)
		}
		if err := store.Seed("repo.json", []byte(`{bad json`), "2"); err != nil {
			t.Fatal(err)
		}
		att := runSyncAttempt(context.Background(), triggerPeriodic)
		if !att.permanent || att.pauseReason != "repo-lost" {
			t.Fatalf("attempt = %+v, want permanent repo-lost", att)
		}
	})

	// A corrupt connection record is permanent.
	t.Run("corrupt-connection", func(t *testing.T) {
		dir, state := t.TempDir(), t.TempDir()
		setSyncEnv(t, dir, state, nil)
		doJSON(t, "POST", "/api/sync/enable", nil)
		vaultID, replicaID, stateRoot, err := syncIdentity()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(syncConnectedPath(stateRoot, vaultID, replicaID), []byte(`{bad json`), 0600); err != nil {
			t.Fatal(err)
		}
		att := runSyncAttempt(context.Background(), triggerPeriodic)
		if !att.permanent || att.pauseReason != "corrupt-state" {
			t.Fatalf("attempt = %+v, want permanent corrupt-state", att)
		}
	})

	// A corrupt local index (primary and backup) is permanent.
	t.Run("corrupt-index", func(t *testing.T) {
		dir, state := t.TempDir(), t.TempDir()
		setSyncEnv(t, dir, state, nil)
		doJSON(t, "POST", "/api/sync/enable", nil)
		for _, name := range []string{"sync-index.json", "sync-index.json.bak"} {
			if err := os.WriteFile(filepath.Join(dir, ".memodump", name), []byte(`{bad json`), 0600); err != nil {
				t.Fatal(err)
			}
		}
		att := runSyncAttempt(context.Background(), triggerPeriodic)
		if !att.permanent || att.pauseReason != "corrupt-state" {
			t.Fatalf("attempt = %+v, want permanent corrupt-state", att)
		}
	})
}

// TestSyncAttemptAutomaticDisconnectedDoesNotRecord: a startup/periodic attempt
// on a never-connected vault goes idle without recording a last-run failure, so
// a user who never enabled sync never sees a spurious failure record.
func TestSyncAttemptAutomaticDisconnectedDoesNotRecord(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	setSyncEnv(t, dir, state, nil)
	att := runSyncAttempt(context.Background(), triggerStartup)
	if !att.disabled {
		t.Fatalf("attempt = %+v, want disabled", att)
	}
	syncLastRunMu.RLock()
	completed := syncLastRun.Completed
	result := syncLastRun.Result
	trigger := syncLastRun.Trigger
	syncLastRunMu.RUnlock()
	if !completed.IsZero() {
		t.Fatalf("automatic disabled attempt recorded a last-run at %v", completed)
	}
	if result.Synced || result.LastError != "" {
		t.Fatalf("automatic disabled attempt recorded a result: %+v", result)
	}
	if trigger != "" {
		t.Fatalf("automatic disabled attempt recorded trigger %q", trigger)
	}
}

// TestSyncAttemptRecordsTrigger: every attempt writes one redacted last-attempt
// record including its trigger, shared by manual and automatic callers.
func TestSyncAttemptRecordsTrigger(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	setSyncEnv(t, dir, state, nil)
	doJSON(t, "POST", "/api/sync/enable", nil)
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("# A\n"), 0644); err != nil {
		t.Fatal(err)
	}
	att := runSyncAttempt(context.Background(), triggerStartup)
	if att.disabled || att.Result == nil || !att.Result.Synced {
		t.Fatalf("startup attempt = %+v", att.Result)
	}
	syncLastRunMu.RLock()
	trigger := syncLastRun.Trigger
	completed := syncLastRun.Completed
	lastError := syncLastRun.Result.LastError
	syncLastRunMu.RUnlock()
	if trigger != "startup" || completed.IsZero() {
		t.Fatalf("lastRun = trigger %q at %v, want startup recorded", trigger, completed)
	}
	if lastError != "" {
		t.Fatalf("lastRun leaked a non-empty error: %q", lastError)
	}
}

// TestSyncAttemptConcurrentTriggersNoOverlap: concurrent attempts (manual +
// periodic) serialize on the process mutex and complete without corrupting the
// shared last-attempt record. The -race run covers the memory model.
func TestSyncAttemptConcurrentTriggersNoOverlap(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	setSyncEnv(t, dir, state, nil)
	doJSON(t, "POST", "/api/sync/enable", nil)
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("# A\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			trig := triggerPeriodic
			if i%2 == 0 {
				trig = triggerManual
			}
			att := runSyncAttempt(context.Background(), trig)
			if att.disabled {
				t.Error("unexpectedly disabled")
			}
		}(i)
	}
	wg.Wait()
	syncLastRunMu.RLock()
	trigger := syncLastRun.Trigger
	syncLastRunMu.RUnlock()
	if trigger != "manual" && trigger != "periodic" {
		t.Fatalf("lastRun trigger = %q, want manual or periodic", trigger)
	}
}
