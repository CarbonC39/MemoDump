package main

import (
	"context"
	"testing"
	"time"
)

// TestSyncStatusSchedulingFields: /api/sync/status reports the secret-free
// scheduling state (autoEnabled, interval, running, nextRun, paused) across
// idle, scheduled, paused, and running states.
func TestSyncStatusSchedulingFields(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	setSyncEnv(t, dir, state, nil)
	doJSON(t, "POST", "/api/sync/enable", nil)

	clock := newFakeClock()
	runner := &fakeRunner{}
	s := newSyncScheduler(clock, runner.run)
	syncSchedMu.Lock()
	syncSched = s
	syncSchedMu.Unlock()
	t.Cleanup(func() {
		syncSchedMu.Lock()
		syncSched = nil
		syncSchedMu.Unlock()
	})

	// Idle: connected, not paused, nothing scheduled.
	st := decodeSync[map[string]any](t, doJSON(t, "GET", "/api/sync/status", nil))
	if st["autoEnabled"] != true {
		t.Fatalf("status = %+v, want autoEnabled true", st)
	}
	if secs, ok := st["autoIntervalSecs"].(float64); !ok || int(secs) != 300 {
		t.Fatalf("autoIntervalSecs = %v, want 300", st["autoIntervalSecs"])
	}
	if st["autoPaused"] != false || st["pauseReason"] != "" {
		t.Fatalf("status = %+v, want not paused", st)
	}
	if st["nextRun"] != nil {
		t.Fatalf("nextRun = %v, want null when idle", st["nextRun"])
	}
	if st["syncRunning"] != false {
		t.Fatalf("syncRunning = %v, want false", st["syncRunning"])
	}

	// Scheduled: a next run time is exposed as RFC3339 UTC.
	s.mu.Lock()
	s.nextRun = clock.Now().Add(5 * time.Minute)
	s.nextTrig = triggerPeriodic
	s.mu.Unlock()
	st = decodeSync[map[string]any](t, doJSON(t, "GET", "/api/sync/status", nil))
	if st["nextRun"] == nil {
		t.Fatalf("status = %+v, want a scheduled nextRun", st)
	}
	if _, err := time.Parse(time.RFC3339, st["nextRun"].(string)); err != nil {
		t.Fatalf("nextRun %q is not RFC3339: %v", st["nextRun"], err)
	}

	// Paused: automatic sync disabled with a redacted reason.
	s.mu.Lock()
	s.paused = true
	s.pauseRsn = "permission"
	s.nextRun = time.Time{}
	s.mu.Unlock()
	st = decodeSync[map[string]any](t, doJSON(t, "GET", "/api/sync/status", nil))
	if st["autoEnabled"] != false || st["autoPaused"] != true || st["pauseReason"] != "permission" {
		t.Fatalf("status = %+v, want paused with reason permission", st)
	}

	// Running: an attempt owns the boundary.
	syncRunning.Store(true)
	defer syncRunning.Store(false)
	st = decodeSync[map[string]any](t, doJSON(t, "GET", "/api/sync/status", nil))
	if st["syncRunning"] != true {
		t.Fatalf("status = %+v, want syncRunning true", st)
	}
}

// TestSyncStatusDisconnectedNotAuto: a disconnected replica is never auto
// enabled even when the scheduler is idle (no pause).
func TestSyncStatusDisconnectedNotAuto(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	setSyncEnv(t, dir, state, nil)
	doJSON(t, "POST", "/api/sync/enable", nil)
	doJSON(t, "POST", "/api/sync/disable", nil)

	st := decodeSync[map[string]any](t, doJSON(t, "GET", "/api/sync/status", nil))
	if st["autoEnabled"] != false {
		t.Fatalf("status = %+v, want autoEnabled false when disconnected", st)
	}
	if st["connected"] != false {
		t.Fatalf("status = %+v, want disconnected", st)
	}
}

// TestSyncLifecycleStartStop: startSyncScheduler/stopSyncScheduler round-trip
// cleanly, are idempotent, and Stop waits for the loop goroutine to exit.
func TestSyncLifecycleStartStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startSyncScheduler(ctx)
	startSyncScheduler(ctx) // idempotent
	stopSyncScheduler()
	stopSyncScheduler() // idempotent

	// A fresh start after stop works and leaves no goroutine behind.
	startSyncScheduler(ctx)
	stopSyncScheduler()
}
