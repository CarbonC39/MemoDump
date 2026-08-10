package main

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Automatic scheduling timing (R5). Intervals are minutes, not seconds: a cycle
// lists and reads every remote note record. The scheduler is process-local and
// in-memory: a restart forgets the pause and all backoff state.
const (
	startupDelay  = 10 * time.Second
	periodicEvery = 5 * time.Minute
)

// backoffSequence is the retry delay progression for retryable provider
// failures and deferred (Retry > 0) cycles, capped at its last entry.
var backoffSequence = []time.Duration{
	1 * time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 30 * time.Minute,
}

// schedulerTimer abstracts a resettable timer so tests can drive time
// deterministically without time.Sleep.
type schedulerTimer interface {
	C() <-chan time.Time
	Reset(d time.Duration) bool
	Stop() bool
}

// schedulerClock abstracts the time source for the scheduler.
type schedulerClock interface {
	Now() time.Time
	NewTimer(d time.Duration) schedulerTimer
}

// realSchedulerClock is the production clock.
type realSchedulerClock struct{}

func (realSchedulerClock) Now() time.Time { return time.Now() }

func (realSchedulerClock) NewTimer(d time.Duration) schedulerTimer {
	return &realSchedulerTimer{t: time.NewTimer(d)}
}

type realSchedulerTimer struct{ t *time.Timer }

func (r *realSchedulerTimer) C() <-chan time.Time        { return r.t.C }
func (r *realSchedulerTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }
func (r *realSchedulerTimer) Stop() bool                 { return r.t.Stop() }

// syncScheduler runs one periodic full-scan cycle per connected replica while
// the process is alive. It owns one goroutine, one resettable timer, a
// size-one coalescing wake channel, and a recompute channel; it owns no
// filesystem state and writes no scheduler JSON. All timing decisions are
// recomputed from the injected clock, so tests never sleep.
type syncScheduler struct {
	clock schedulerClock
	run   func(ctx context.Context, trigger attemptTrigger) *syncAttempt

	mu       sync.Mutex
	nextRun  time.Time // zero = idle (wait for a wake only)
	nextTrig attemptTrigger
	paused   bool
	pauseRsn string
	failures int

	wakeCtx   context.Context
	wake      chan struct{} // size-one coalescing immediate-run request (Enable)
	recompute chan struct{} // size-one state-change notice (ClearPause/Reset)
	cancel    context.CancelFunc
	done      chan struct{}
	started   chan struct{} // closed once the loop has armed and is waiting
	startOnce sync.Once

	processed atomic.Int64 // number of attempts fully processed (loop re-armed)
	loopTick  atomic.Int64 // number of loop iterations (re-arms) completed
}

// newSyncScheduler builds a scheduler with the given clock and run function.
// Tests inject a fake clock and a fake run; production uses realSchedulerClock
// and runSyncAttempt.
func newSyncScheduler(clock schedulerClock, run func(ctx context.Context, trigger attemptTrigger) *syncAttempt) *syncScheduler {
	return &syncScheduler{
		clock: clock, run: run,
		wake:      make(chan struct{}, 1),
		recompute: make(chan struct{}, 1),
	}
}

// Start launches the scheduler loop under a parent context. The first timer is
// armed synchronously for the startup delay, so callers can wait on Started()
// before driving an injected clock.
func (s *syncScheduler) Start(parent context.Context) {
	s.wakeCtx, s.cancel = context.WithCancel(parent)
	s.done = make(chan struct{})
	s.started = make(chan struct{})
	s.mu.Lock()
	s.nextRun = s.clock.Now().Add(startupDelay)
	s.nextTrig = triggerStartup
	s.mu.Unlock()
	timer := s.clock.NewTimer(startupDelay)
	go s.loop(timer)
}

// Started returns a channel closed once the loop has armed its timer and is
// waiting, so deterministic-clock tests never advance time before the scheduler
// is ready.
func (s *syncScheduler) Started() <-chan struct{} { return s.started }

// Stop cancels the loop and waits for it to exit.
func (s *syncScheduler) Stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	<-s.done
	s.cancel = nil
}

// Wake coalesces an immediate run request (a successful Enable).
func (s *syncScheduler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default: // already pending: at most one extra attempt
	}
}

// Reset clears pending wake/retry state and leaves the scheduler idle (a
// successful Enable wakes it again). Called by Disable and Reset.
func (s *syncScheduler) Reset() {
	s.mu.Lock()
	s.nextRun = time.Time{}
	s.nextTrig = triggerEnable
	s.failures = 0
	s.paused = false
	s.pauseRsn = ""
	s.mu.Unlock()
	for {
		select {
		case <-s.wake:
		default:
			break
		}
		break
	}
	s.recomputeNot()
}

// ClearPause clears a permanent pause after a successful manual run and
// schedules the next ordinary interval. Called by the manual attempt handler.
func (s *syncScheduler) ClearPause() {
	s.mu.Lock()
	s.paused = false
	s.pauseRsn = ""
	s.failures = 0
	s.nextRun = s.clock.Now().Add(periodicEvery)
	s.nextTrig = triggerPeriodic
	s.mu.Unlock()
	s.recomputeNot()
}

func (s *syncScheduler) recomputeNot() {
	select {
	case s.recompute <- struct{}{}:
	default:
	}
}

// syncSched is the process-wide automatic scheduler, started only by the CLI
// and Wails lifecycles (never implicitly by tests). Guarded by syncSchedMu.
var (
	syncSchedMu sync.RWMutex
	syncSched   *syncScheduler
)

// startSyncScheduler starts the process scheduler with the real clock and the
// shared attempt function. It is a no-op if already started.
func startSyncScheduler(parent context.Context) {
	syncSchedMu.Lock()
	defer syncSchedMu.Unlock()
	if syncSched != nil {
		return
	}
	s := newSyncScheduler(realSchedulerClock{}, runSyncAttempt)
	s.Start(parent)
	syncSched = s
}

// stopSyncScheduler stops and waits for the process scheduler.
func stopSyncScheduler() {
	syncSchedMu.Lock()
	defer syncSchedMu.Unlock()
	if syncSched == nil {
		return
	}
	syncSched.Stop()
	syncSched = nil
}

// wakeSyncScheduler requests an immediate coalesced attempt (a successful
// Enable).
func wakeSyncScheduler() {
	syncSchedMu.RLock()
	defer syncSchedMu.RUnlock()
	if syncSched != nil {
		syncSched.Wake()
	}
}

// resetSyncScheduler leaves the scheduler idle (Disable/Reset).
func resetSyncScheduler() {
	syncSchedMu.RLock()
	defer syncSchedMu.RUnlock()
	if syncSched != nil {
		syncSched.Reset()
	}
}

// clearSyncSchedulerPause clears a permanent pause after a successful manual
// run and schedules the ordinary interval.
func clearSyncSchedulerPause() {
	syncSchedMu.RLock()
	defer syncSchedMu.RUnlock()
	if syncSched != nil {
		syncSched.ClearPause()
	}
}

// syncSchedulerStatusSnapshot returns the scheduler state for the status
// endpoint. With no scheduler (tests, local build) it reports idle.
func syncSchedulerStatusSnapshot() (nextRun time.Time, paused bool, pauseRsn string) {
	syncSchedMu.RLock()
	defer syncSchedMu.RUnlock()
	if syncSched == nil {
		return time.Time{}, false, ""
	}
	st := syncSched.Status()
	return st.nextRun, st.paused, st.pauseRsn
}

// schedulerStatus is a secret-free snapshot of the scheduling state.
type schedulerStatus struct {
	nextRun  time.Time
	paused   bool
	pauseRsn string
	failures int
}

// Status returns a consistent snapshot of the scheduling state.
func (s *syncScheduler) Status() schedulerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return schedulerStatus{nextRun: s.nextRun, paused: s.paused, pauseRsn: s.pauseRsn, failures: s.failures}
}

// loop is the scheduler's single goroutine: it re-arms the timer from the
// current state, then waits on the timer, the wake channel, a recompute notice,
// or shutdown, running at most one attempt at a time.
func (s *syncScheduler) loop(timer schedulerTimer) {
	defer close(s.done)
	for {
		if !s.armTimer(timer) {
			timer.Stop()
		}
		s.startOnce.Do(func() { close(s.started) })
		s.loopTick.Add(1)
		select {
		case <-s.wakeCtx.Done():
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
			s.handleWake()
		case <-s.recompute:
			// ClearPause/Reset changed the schedule; loop re-arms the timer.
		case <-timer.C():
			s.handleTimerFire()
		}
	}
}

// armTimer resets the single timer to the next automatic run. It returns false
// when the scheduler is paused or idle (nothing to wait for).
func (s *syncScheduler) armTimer(timer schedulerTimer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paused || s.nextRun.IsZero() {
		return false
	}
	d := s.nextRun.Sub(s.clock.Now())
	if d < 0 {
		d = 0
	}
	timer.Reset(d)
	return true
}

// handleTimerFire runs the scheduled automatic attempt (startup/periodic/retry).
func (s *syncScheduler) handleTimerFire() {
	s.mu.Lock()
	trigger := s.nextTrig
	s.mu.Unlock()
	s.runAttempt(trigger)
}

// handleWake handles a coalesced immediate run (a successful Enable). It clears
// a permanent pause and schedules an immediate attempt.
func (s *syncScheduler) handleWake() {
	s.mu.Lock()
	s.paused = false
	s.pauseRsn = ""
	s.mu.Unlock()
	s.runAttempt(triggerEnable)
}

// runAttempt runs one attempt and computes the next schedule from its
// classification.
func (s *syncScheduler) runAttempt(trigger attemptTrigger) {
	att := s.run(s.wakeCtx, trigger)
	s.mu.Lock()
	switch {
	case att.disabled:
		// Not connected: go idle until a wake (Enable).
		s.nextRun = time.Time{}
		s.nextTrig = triggerEnable
	case att.permanent:
		// Pause automatic attempts until a successful manual run or Enable.
		s.paused = true
		s.pauseRsn = att.pauseReason
		s.nextRun = time.Time{}
	case att.retryable:
		// Retryable provider failure or deferred cycle: backoff, capped at 30
		// minutes, honoring a larger provider Retry-After.
		d := backoffSequence[min(s.failures, len(backoffSequence)-1)]
		if att.retryAfter > d {
			d = att.retryAfter
		}
		s.failures++
		s.nextRun = s.clock.Now().Add(d)
		s.nextTrig = triggerRetry
	default:
		// Success, locked, cancelled, or Blocked > 0: next ordinary interval.
		// Only success clears the transport failure count; the others do not
		// increase it and wait for the ordinary interval.
		if att.Result != nil && att.Result.Synced {
			s.failures = 0
		}
		s.nextRun = s.clock.Now().Add(periodicEvery)
		s.nextTrig = triggerPeriodic
	}
	s.mu.Unlock()
	s.processed.Add(1)
}
