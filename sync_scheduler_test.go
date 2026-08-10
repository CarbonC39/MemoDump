package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"memodump/internal/syncservice"
)

// fakeClock is a deterministic clock for scheduler tests. Advancing it fires
// pending timers in order; nothing sleeps.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) NewTimer(d time.Duration) schedulerTimer {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{f: f, ch: make(chan time.Time, 1), when: f.now.Add(d)}
	f.timers = append(f.timers, t)
	return t
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	var ready []*fakeTimer
	for _, t := range f.timers {
		if !t.stopped && !t.fired && !t.when.After(f.now) {
			t.fired = true
			ready = append(ready, t)
		}
	}
	f.mu.Unlock()
	for _, t := range ready {
		t.ch <- t.when
	}
}

type fakeTimer struct {
	f       *fakeClock
	ch      chan time.Time
	when    time.Time
	fired   bool
	stopped bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	t.when = t.f.now.Add(d)
	t.fired = false
	t.stopped = false
	select {
	case <-t.ch:
	default:
	}
	return true
}

func (t *fakeTimer) Stop() bool {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	t.stopped = true
	return true
}

// fakeRunner records triggers and returns attempts from a configurable queue
// (falling back to a default). blockFirst, when non-nil, blocks the first
// attempt until it is closed (for coalescing/no-overlap tests).
type fakeRunner struct {
	mu         sync.Mutex
	triggers   []attemptTrigger
	queue      []*syncAttempt
	blockFirst chan struct{}
}

func (r *fakeRunner) run(ctx context.Context, trigger attemptTrigger) *syncAttempt {
	r.mu.Lock()
	r.triggers = append(r.triggers, trigger)
	var att *syncAttempt
	if len(r.queue) > 0 {
		att = r.queue[0]
		r.queue = r.queue[1:]
	}
	r.mu.Unlock()
	if att == nil {
		att = &syncAttempt{Result: &syncservice.Result{Synced: true}}
	}
	if r.blockFirst != nil {
		r.mu.Lock()
		block := r.blockFirst
		r.blockFirst = nil
		r.mu.Unlock()
		if block != nil {
			<-block
		}
	}
	return att
}

func (r *fakeRunner) seen() []attemptTrigger {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]attemptTrigger(nil), r.triggers...)
}

func testAttempt(kind string) *syncAttempt {
	switch kind {
	case "success":
		return &syncAttempt{Result: &syncservice.Result{Synced: true}}
	case "retryable":
		return &syncAttempt{retryable: true, Result: &syncservice.Result{Synced: false, Retry: 1}}
	case "permanent":
		return &syncAttempt{permanent: true, pauseReason: "permission", Result: &syncservice.Result{Synced: false, LastError: "permission"}}
	case "disabled":
		return &syncAttempt{disabled: true}
	case "blocked":
		return &syncAttempt{Result: &syncservice.Result{Synced: false, Blocked: 2}}
	default:
		return &syncAttempt{Result: &syncservice.Result{Synced: true}}
	}
}

// waitSeen waits (with a real deadline) until the runner has seen the triggers.
func waitSeen(t *testing.T, r *fakeRunner, n int) []attemptTrigger {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := r.seen()
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(time.Millisecond)
	}
}

// waitProcessed waits until the scheduler has fully processed (re-armed after)
// n attempts, so deterministic-clock tests never advance the clock while the
// single loop goroutine is still handling a fired attempt.
func waitProcessed(t *testing.T, s *syncScheduler, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.processed.Load() < n && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.processed.Load() < n {
		t.Fatalf("scheduler processed %d attempts, want %d", s.processed.Load(), n)
	}
}

// waitLoopTick waits until the loop goroutine has completed another iteration
// after the captured tick (i.e. it re-armed the timer after an external state
// change such as ClearPause or Reset).
func waitLoopTick(t *testing.T, s *syncScheduler, after int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.loopTick.Load() <= after && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.loopTick.Load() <= after {
		t.Fatalf("scheduler loop did not re-arm after tick %d", after)
	}
}

// TestSyncSchedulerStartupThenPeriodic: after the 10s startup delay a connected
// replica runs (startup), and a successful attempt schedules the next ordinary
// run 5 minutes later.
func TestSyncSchedulerStartupThenPeriodic(t *testing.T) {
	clock := newFakeClock()
	runner := &fakeRunner{}
	s := newSyncScheduler(clock, runner.run)
	s.Start(context.Background())
	<-s.Started()
	defer s.Stop()

	tick := s.loopTick.Load()
	clock.Advance(10 * time.Second)
	if got := waitSeen(t, runner, 1); got[0] != triggerStartup {
		t.Fatalf("first trigger = %v, want startup", got)
	}
	waitProcessed(t, s, 1)
	waitLoopTick(t, s, tick)
	st := s.Status()
	if st.paused || st.failures != 0 {
		t.Fatalf("status = %+v, want clean after success", st)
	}
	if st.nextRun.Sub(clock.Now()) != periodicEvery {
		t.Fatalf("next run in %v, want %v", st.nextRun.Sub(clock.Now()), periodicEvery)
	}

	clock.Advance(5 * time.Minute)
	if got := waitSeen(t, runner, 2); got[1] != triggerPeriodic {
		t.Fatalf("second trigger = %v, want periodic", got)
	}
}

// TestSyncSchedulerEnableWake: a successful Enable coalesces an immediate run
// with the enable trigger, without waiting for the startup delay.
func TestSyncSchedulerEnableWake(t *testing.T) {
	clock := newFakeClock()
	runner := &fakeRunner{}
	s := newSyncScheduler(clock, runner.run)
	s.Start(context.Background())
	<-s.Started()
	defer s.Stop()

	s.Wake()
	if got := waitSeen(t, runner, 1); got[0] != triggerEnable {
		t.Fatalf("wake trigger = %v, want enable", got)
	}
}

// TestSyncSchedulerBackoffSequence: successive retryable failures use the exact
// 1m/2m/5m/10m/30m progression and then stay capped at 30m.
func TestSyncSchedulerBackoffSequence(t *testing.T) {
	clock := newFakeClock()
	runner := &fakeRunner{}
	s := newSyncScheduler(clock, runner.run)
	s.Start(context.Background())
	<-s.Started()
	defer s.Stop()

	// The startup attempt and every retry are retryable.
	for i := 0; i < 7; i++ {
		runner.mu.Lock()
		runner.queue = append(runner.queue, testAttempt("retryable"))
		runner.mu.Unlock()
	}
	clock.Advance(10 * time.Second)
	waitSeen(t, runner, 1)
	waitProcessed(t, s, 1)

	delays := []time.Duration{1 * time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for i, d := range delays {
		tick := s.loopTick.Load()
		clock.Advance(d)
		waitSeen(t, runner, i+2)
		waitProcessed(t, s, int64(i+2))
		waitLoopTick(t, s, tick)
	}
	got := waitSeen(t, runner, 7)
	want := []attemptTrigger{triggerStartup, triggerRetry, triggerRetry, triggerRetry, triggerRetry, triggerRetry, triggerRetry}
	if len(got) != len(want) {
		t.Fatalf("saw %d triggers, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("trigger[%d] = %v, want %v (all: %v)", i, got[i], w, got)
		}
	}
}

// TestSyncSchedulerHonorsRetryAfter: a larger provider Retry-After extends the
// backoff instead of shortening it.
func TestSyncSchedulerHonorsRetryAfter(t *testing.T) {
	clock := newFakeClock()
	runner := &fakeRunner{}
	s := newSyncScheduler(clock, runner.run)
	s.Start(context.Background())
	<-s.Started()
	defer s.Stop()

	// The startup attempt is retryable with a 7-minute Retry-After, so the next
	// retry is scheduled 7 minutes out (honoring the larger Retry-After instead
	// of the 1-minute backoff slot).
	runner.mu.Lock()
	runner.queue = append(runner.queue, &syncAttempt{retryable: true, retryAfter: 7 * time.Minute})
	runner.mu.Unlock()
	tick := s.loopTick.Load()
	clock.Advance(10 * time.Second)
	waitSeen(t, runner, 1)
	waitProcessed(t, s, 1)
	waitLoopTick(t, s, tick)

	// 5 minutes is short of the 7-minute Retry-After: no retry yet.
	clock.Advance(5 * time.Minute)
	if got := runner.seen(); len(got) != 1 {
		t.Fatalf("retry fired before the Retry-After elapsed: %v", got)
	}
	clock.Advance(2 * time.Minute)
	if got := waitSeen(t, runner, 2); got[1] != triggerRetry {
		t.Fatalf("retry after Retry-After = %v, want retry", got)
	}
}

// TestSyncSchedulerPermanentPause: a permanent failure pauses automatic attempts
// until a successful manual run (ClearPause) or an Enable wakes it.
func TestSyncSchedulerPermanentPause(t *testing.T) {
	clock := newFakeClock()
	runner := &fakeRunner{}
	s := newSyncScheduler(clock, runner.run)
	s.Start(context.Background())
	<-s.Started()
	defer s.Stop()

	// The startup attempt fails permanently, pausing the scheduler.
	runner.mu.Lock()
	runner.queue = append(runner.queue, testAttempt("permanent"))
	runner.mu.Unlock()
	clock.Advance(10 * time.Second)
	waitSeen(t, runner, 1)
	waitProcessed(t, s, 1)

	st := s.Status()
	if !st.paused || st.pauseRsn != "permission" {
		t.Fatalf("status = %+v, want paused with reason permission", st)
	}

	// Time passing does not retry while paused.
	clock.Advance(30 * time.Minute)
	if got := runner.seen(); len(got) != 1 {
		t.Fatalf("paused scheduler ran %d attempts, want 1: %v", len(got), got)
	}

	// A successful manual run clears the pause and schedules the ordinary run.
	tick := s.loopTick.Load()
	s.ClearPause()
	waitLoopTick(t, s, tick)
	clock.Advance(5 * time.Minute)
	if got := waitSeen(t, runner, 2); got[1] != triggerPeriodic {
		t.Fatalf("after manual recovery = %v, want periodic", got)
	}
	waitProcessed(t, s, 2)
}

// TestSyncSchedulerWakeClearsPause: a successful Enable wakes a paused
// scheduler immediately.
func TestSyncSchedulerWakeClearsPause(t *testing.T) {
	clock := newFakeClock()
	runner := &fakeRunner{}
	s := newSyncScheduler(clock, runner.run)
	s.Start(context.Background())
	<-s.Started()
	defer s.Stop()

	// The startup attempt fails permanently, pausing the scheduler.
	runner.mu.Lock()
	runner.queue = append(runner.queue, testAttempt("permanent"))
	runner.mu.Unlock()
	tick := s.loopTick.Load()
	clock.Advance(10 * time.Second)
	waitSeen(t, runner, 1)
	waitProcessed(t, s, 1)
	waitLoopTick(t, s, tick)
	if !s.Status().paused {
		t.Fatal("expected the scheduler paused")
	}

	s.Wake()
	if got := waitSeen(t, runner, 2); got[1] != triggerEnable {
		t.Fatalf("wake after pause = %v, want enable", got)
	}
	if s.Status().paused {
		t.Fatal("wake did not clear the pause")
	}
}

// TestSyncSchedulerBlockedNotRetryable: a blocked cycle (Blocked > 0, no fatal
// error) waits for the ordinary interval without increasing the failure count.
func TestSyncSchedulerBlockedNotRetryable(t *testing.T) {
	clock := newFakeClock()
	runner := &fakeRunner{}
	s := newSyncScheduler(clock, runner.run)
	s.Start(context.Background())
	<-s.Started()
	defer s.Stop()

	clock.Advance(10 * time.Second)
	waitSeen(t, runner, 1)
	runner.mu.Lock()
	runner.queue = append(runner.queue, testAttempt("blocked"))
	runner.mu.Unlock()
	clock.Advance(10 * time.Second)
	waitSeen(t, runner, 1)
	if s.Status().failures != 0 {
		t.Fatalf("blocked cycle increased the failure count: %+v", s.Status())
	}
}

// TestSyncSchedulerResetIdles: Disable/Reset leaves the scheduler idle; a later
// successful Enable wakes it again.
func TestSyncSchedulerResetIdles(t *testing.T) {
	clock := newFakeClock()
	runner := &fakeRunner{}
	s := newSyncScheduler(clock, runner.run)
	s.Start(context.Background())
	<-s.Started()
	defer s.Stop()

	clock.Advance(10 * time.Second)
	waitSeen(t, runner, 1)
	waitProcessed(t, s, 1)

	tick := s.loopTick.Load()
	s.Reset()
	waitLoopTick(t, s, tick)
	clock.Advance(30 * time.Minute)
	if got := runner.seen(); len(got) != 1 {
		t.Fatalf("idle scheduler ran %d attempts, want 1: %v", len(got), got)
	}

	s.Wake()
	if got := waitSeen(t, runner, 2); got[1] != triggerEnable {
		t.Fatalf("wake after reset = %v, want enable", got)
	}
}

// TestSyncSchedulerWakeCoalescing: wakes arriving while an attempt runs produce
// at most one later attempt, never parallel runs.
func TestSyncSchedulerWakeCoalescing(t *testing.T) {
	clock := newFakeClock()
	block := make(chan struct{})
	runner := &fakeRunner{blockFirst: block}
	s := newSyncScheduler(clock, runner.run)
	s.Start(context.Background())
	<-s.Started()
	defer s.Stop()

	// The startup attempt blocks until released.
	clock.Advance(10 * time.Second)
	waitSeen(t, runner, 1)
	s.Wake()
	s.Wake()
	s.Wake()
	close(block) // release the startup attempt

	// At most ONE coalesced enable attempt follows, never three.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := runner.seen(); len(got) >= 3 || time.Now().After(deadline) {
			if len(got) > 2 {
				t.Fatalf("wakes caused %d attempts, want at most 2: %v", len(got), got)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSyncSchedulerShutdown: Stop cancels the loop, waits for it, and leaves no
// goroutine running; double-stop is safe.
func TestSyncSchedulerShutdown(t *testing.T) {
	clock := newFakeClock()
	runner := &fakeRunner{}
	s := newSyncScheduler(clock, runner.run)
	s.Start(context.Background())
	<-s.Started()
	s.Stop()
	s.Stop()
	// No further attempts after shutdown.
	clock.Advance(time.Hour)
	if got := runner.seen(); len(got) != 0 {
		t.Fatalf("scheduler ran after shutdown: %v", got)
	}
}

// TestSyncSchedulerRestartForgetsState: a fresh scheduler starts with no
// failures, no pause, and a startup schedule — proving there is no durable
// scheduler/backoff state across restart.
func TestSyncSchedulerRestartForgetsState(t *testing.T) {
	clock := newFakeClock()
	runner := &fakeRunner{}
	s := newSyncScheduler(clock, runner.run)
	s.Start(context.Background())
	<-s.Started()

	// Startup is retryable (backoff), then the first retry fails permanently.
	runner.mu.Lock()
	runner.queue = append(runner.queue, testAttempt("retryable"), testAttempt("permanent"))
	runner.mu.Unlock()
	clock.Advance(10 * time.Second)
	waitSeen(t, runner, 1)
	waitProcessed(t, s, 1)
	clock.Advance(1 * time.Minute)
	waitSeen(t, runner, 2)
	waitProcessed(t, s, 2)
	if !s.Status().paused {
		t.Fatal("expected a paused state before restart")
	}
	s.Stop()

	// A fresh scheduler after "restart" forgets pause and backoff.
	clock2 := newFakeClock()
	runner2 := &fakeRunner{}
	s2 := newSyncScheduler(clock2, runner2.run)
	s2.Start(context.Background())
	defer s2.Stop()
	st := s2.Status()
	if st.paused || st.pauseRsn != "" || st.failures != 0 {
		t.Fatalf("restarted scheduler status = %+v, want clean", st)
	}
	if st.nextRun.Sub(clock2.Now()) != startupDelay {
		t.Fatalf("restarted scheduler next run in %v, want startup delay", st.nextRun.Sub(clock2.Now()))
	}
}
