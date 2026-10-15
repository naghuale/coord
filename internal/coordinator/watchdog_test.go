package coordinator

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"coord/internal/watchdog"
)

type stubWatchdog struct{ err error }

func (s *stubWatchdog) Run(_ context.Context) error { return s.err }

func TestRunWithWatchdogPropagatesError(t *testing.T) {
	want := errors.New("watchdog failure")
	err := RunWithWatchdog(context.Background(), &stubWatchdog{err: want}, func(_ context.Context) error {
		return nil
	})
	if err != want {
		t.Errorf("expected watchdog error %v, got %v", want, err)
	}
}

func TestRunWithWatchdogStateMachineError(t *testing.T) {
	want := errors.New("state machine failure")
	err := RunWithWatchdog(context.Background(), &stubWatchdog{}, func(_ context.Context) error {
		return want
	})
	if err != want {
		t.Errorf("expected state machine error %v, got %v", want, err)
	}
}

func TestRunWithWatchdogBothSucceed(t *testing.T) {
	err := RunWithWatchdog(context.Background(), &stubWatchdog{}, func(_ context.Context) error {
		return nil
	})
	if err != nil {
		t.Errorf("expected nil when both succeed, got %v", err)
	}
}

func TestValidateCompletionGatePassesCleanState(t *testing.T) {
	state := watchdog.CoordinatorState{}
	if err := ValidateCompletionGate(state); err != nil {
		t.Errorf("expected no error for clean state, got: %v", err)
	}
}

func TestValidateCompletionGateRejectsDelivered(t *testing.T) {
	state := watchdog.CoordinatorState{
		PendingAssignments: []watchdog.PendingAssignment{
			{SessionState: watchdog.SessionDelivered},
		},
	}
	if err := ValidateCompletionGate(state); err == nil {
		t.Error("expected error for DELIVERED session, got nil")
	}
}

func TestValidateCompletionGateRejectsFuturePromise(t *testing.T) {
	state := watchdog.CoordinatorState{
		FuturePromise: "awaiting Review",
	}
	if err := ValidateCompletionGate(state); err == nil {
		t.Error("expected error for future promise, got nil")
	}
}

// --- coordinator boundary tests (TD-R2) ---

// TestCoordinatorCannotStopWithStaleHeartbeat asserts the coordinator turn may
// not end while a pending assignment still has a stale heartbeat and no
// reconcile has been attempted.
func TestCoordinatorCannotStopWithStaleHeartbeat(t *testing.T) {
	state := watchdog.CoordinatorState{
		PendingAssignments: []watchdog.PendingAssignment{
			{
				SessionState:             watchdog.SessionRunning,
				HeartbeatStale:           true,
				ReconcileAttempted:       false,
				IndependentWorkAvailable: false,
			},
		},
	}

	err := ValidateCompletionGate(state)
	if err == nil {
		t.Fatal("coordinator turn must not complete with a stale heartbeat and no reconcile")
	}
	if !errors.Is(err, watchdog.ErrHeartbeatStale) {
		t.Errorf("expected ErrHeartbeatStale, got %v", err)
	}

	// After an explicit reconcile attempt the gate must let the turn proceed.
	state.PendingAssignments[0].ReconcileAttempted = true
	if err := ValidateCompletionGate(state); err != nil {
		t.Errorf("expected gate to pass after reconcile attempt, got %v", err)
	}
}

// TestCoordinatorPerformsIndependentWorkDuringReconcile asserts that available
// independent work must be performed before the turn can end, even while a
// reconcile is outstanding.
func TestCoordinatorPerformsIndependentWorkDuringReconcile(t *testing.T) {
	state := watchdog.CoordinatorState{
		PendingAssignments: []watchdog.PendingAssignment{
			{
				SessionState:             watchdog.SessionRunning,
				HeartbeatStale:           true,
				ReconcileAttempted:       true,
				IndependentWorkAvailable: true,
				IndependentWorkPerformed: false,
			},
		},
	}

	err := ValidateCompletionGate(state)
	if err == nil {
		t.Fatal("coordinator must not stop while independent work is available")
	}
	if !errors.Is(err, watchdog.ErrCoordinatorStoppedEarly) {
		t.Errorf("expected ErrCoordinatorStoppedEarly, got %v", err)
	}

	state.PendingAssignments[0].IndependentWorkPerformed = true
	if err := ValidateCompletionGate(state); err != nil {
		t.Errorf("expected gate to pass after independent work performed, got %v", err)
	}
}

// TestCoordinatorActiveWorkOnStaleSession asserts a stale heartbeat alone does
// not block the turn once reconcile was attempted, but an unstarted session
// state still does.
func TestCoordinatorActiveWorkOnStaleSession(t *testing.T) {
	base := watchdog.CoordinatorState{
		PendingAssignments: []watchdog.PendingAssignment{
			{
				SessionState:             watchdog.SessionRunning,
				HeartbeatStale:           true,
				ReconcileAttempted:       true,
				IndependentWorkAvailable: true,
				IndependentWorkPerformed: true,
			},
		},
	}
	if err := ValidateCompletionGate(base); err != nil {
		t.Errorf("reconciled and worked session must allow the turn to end, got %v", err)
	}

	queued := base
	queued.PendingAssignments = []watchdog.PendingAssignment{
		{SessionState: watchdog.SessionQueued, ReconcileAttempted: true},
	}
	if err := ValidateCompletionGate(queued); !errors.Is(err, watchdog.ErrQueuedNotRunning) {
		t.Errorf("expected ErrQueuedNotRunning, got %v", err)
	}

	delivered := base
	delivered.PendingAssignments = []watchdog.PendingAssignment{
		{SessionState: watchdog.SessionDelivered, ReconcileAttempted: true},
	}
	if err := ValidateCompletionGate(delivered); !errors.Is(err, watchdog.ErrDeliveredNotAdmitted) {
		t.Errorf("expected ErrDeliveredNotAdmitted, got %v", err)
	}
}

// TestFailedReconcileCreatesFenceAndReplacementAction asserts the coordinator
// boundary treats a failed reconcile as a containment event: the turn may end
// only because fence and replacement were dispatched.
func TestFailedReconcileCreatesFenceAndReplacementAction(t *testing.T) {
	session := watchdog.Session{
		ID:         "sess-coord",
		Generation: 3,
		AdmittedAt: time.Unix(0, 0),
		Assignment: watchdog.AssignmentBinding{ID: "assign-coord", ProjectID: "coord", Role: "EXECUTOR"},
		State:      watchdog.SessionRunning,
	}
	now := time.Unix(1000, 0)
	states := map[string]watchdog.HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation,
			LastReceivedAt: now.Add(-10 * time.Second),
			Status:         watchdog.SessionRunning,
		},
	}

	clock := newFakeClock(now)
	reg := newFakeRegistry([]watchdog.Session{session}, states)
	rec := &failingReconciler{}
	disp := &recordingDispatcher{}
	evts := &recordingEvents{}
	met := &countingMetrics{}
	w := watchdog.New(policy(), clock, reg, rec, disp, evts, met)

	// maxMissed is reached on the third tick.
	if err := runTicks(t, w, clock, 3); err != nil {
		t.Fatalf("watchdog run: %v", err)
	}

	if !disp.replacementRequested(session.Assignment.ID) {
		t.Error("failed reconcile must produce a replacement dispatch")
	}
	if met.fenced != 1 {
		t.Errorf("expected exactly 1 fence, got %d", met.fenced)
	}
	if met.replaced != 1 {
		t.Errorf("expected exactly 1 replacement, got %d", met.replaced)
	}
	if !evts.hasType(watchdog.EventSessionFenced) {
		t.Error("failed reconcile must emit SESSION_FENCED")
	}
	// Hard metrics must stay zero on the coordinator boundary path.
	if met.queuedClassifiedRunning != 0 || met.deliveredClassifiedAdmitted != 0 ||
		met.staleWithoutReconcile != 0 || met.passiveWaitAllowed != 0 {
		t.Error("hard metrics must stay zero on the coordinator boundary path")
	}
}

// TestSuccessfulReconcileAllowsFurtherTransition asserts a successful reconcile
// restores the session to RUNNING, emits SESSION_RECOVERED, and performs no
// fence or replacement.
func TestSuccessfulReconcileAllowsFurtherTransition(t *testing.T) {
	session := watchdog.Session{
		ID:         "sess-ok",
		Generation: 2,
		AdmittedAt: time.Unix(0, 0),
		Assignment: watchdog.AssignmentBinding{ID: "assign-ok", ProjectID: "coord", Role: "EXECUTOR"},
		State:      watchdog.SessionRunning,
	}
	now := time.Unix(1000, 0)
	states := map[string]watchdog.HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation,
			LastReceivedAt: now.Add(-10 * time.Second),
			Status:         watchdog.SessionRunning,
		},
	}

	clock := newFakeClock(now)
	reg := newFakeRegistry([]watchdog.Session{session}, states)
	rec := &succeedingReconciler{generation: session.Generation}
	disp := &recordingDispatcher{}
	evts := &recordingEvents{}
	met := &countingMetrics{}
	w := watchdog.New(policy(), clock, reg, rec, disp, evts, met)

	if err := runTicks(t, w, clock, 3); err != nil {
		t.Fatalf("watchdog run: %v", err)
	}

	if met.recovered != 1 {
		t.Errorf("expected 1 recovery, got %d", met.recovered)
	}
	if met.fenced != 0 {
		t.Errorf("successful reconcile must not fence, got %d fences", met.fenced)
	}
	if disp.replacementRequested(session.Assignment.ID) {
		t.Error("successful reconcile must not dispatch a replacement")
	}
	if !evts.hasType(watchdog.EventSessionRecovered) {
		t.Error("successful reconcile must emit SESSION_RECOVERED")
	}

	// With the session reconciled and no outstanding promise, the coordinator
	// turn may end.
	state := watchdog.CoordinatorState{
		PendingAssignments: []watchdog.PendingAssignment{
			{
				SessionState:             watchdog.SessionRunning,
				HeartbeatStale:           true,
				ReconcileAttempted:       true,
				IndependentWorkAvailable: true,
				IndependentWorkPerformed: true,
			},
		},
	}
	if err := ValidateCompletionGate(state); err != nil {
		t.Errorf("expected gate to pass after successful reconcile, got %v", err)
	}
}

// TestPassiveFuturePromiseRejected asserts the coordinator may not end its turn
// on a promise of a future result instead of a verified state.
func TestPassiveFuturePromiseRejected(t *testing.T) {
	state := watchdog.CoordinatorState{FuturePromise: "will finish when the executor reports"}
	if err := ValidateCompletionGate(state); !errors.Is(err, watchdog.ErrPassiveFuturePromise) {
		t.Errorf("expected ErrPassiveFuturePromise, got %v", err)
	}
}

// --- local fakes for the coordinator boundary tests ---

// fakeClock is a deterministic clock whose ticker fires on demand. The
// coordinator boundary drives the watchdog through the exported Run loop, so it
// needs a working ticker rather than a nil stub.
type fakeClock struct {
	mu      sync.Mutex
	current time.Time
	ticks   chan time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{current: start, ticks: make(chan time.Time, 16)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *fakeClock) Tick() {
	c.mu.Lock()
	now := c.current
	c.mu.Unlock()
	select {
	case c.ticks <- now:
	default:
	}
}

func (c *fakeClock) NewTicker(time.Duration) watchdog.Ticker {
	return &fakeTicker{ch: c.ticks}
}

func (c *fakeClock) NewTimer(time.Duration) watchdog.Timer {
	return &fakeTimer{ch: make(chan time.Time, 1)}
}

type fakeTicker struct{ ch chan time.Time }

func (t *fakeTicker) C() <-chan time.Time { return t.ch }

func (t *fakeTicker) Stop() {}

type fakeTimer struct{ ch chan time.Time }

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() bool { return true }

type fakeRegistry struct {
	mu       sync.Mutex
	sessions []watchdog.Session
	states   map[string]watchdog.HeartbeatState
	fenced   map[string]watchdog.FenceRequest
}

func newFakeRegistry(sessions []watchdog.Session, states map[string]watchdog.HeartbeatState) *fakeRegistry {
	return &fakeRegistry{sessions: sessions, states: states, fenced: map[string]watchdog.FenceRequest{}}
}

func (r *fakeRegistry) ListActive(context.Context) ([]watchdog.Session, error) {
	return r.sessions, nil
}

func (r *fakeRegistry) HeartbeatState(_ context.Context, id string, gen uint64) (watchdog.HeartbeatState, error) {
	s, ok := r.states[id]
	if !ok {
		return watchdog.HeartbeatState{SessionID: id, Generation: gen, Status: watchdog.SessionUnknown}, nil
	}
	return s, nil
}

func (r *fakeRegistry) Fence(_ context.Context, req watchdog.FenceRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fenced[req.SessionID] = req
	return nil
}

type failingReconciler struct{}

func (failingReconciler) Reconcile(context.Context, watchdog.Session) (watchdog.ReconcileResult, error) {
	return watchdog.ReconcileResult{}, errors.New("reconcile failed")
}

type succeedingReconciler struct{ generation uint64 }

func (s succeedingReconciler) Reconcile(context.Context, watchdog.Session) (watchdog.ReconcileResult, error) {
	return watchdog.ReconcileResult{Running: true, Generation: s.generation}, nil
}

type recordingDispatcher struct {
	mu         sync.Mutex
	dispatched []string
}

func (d *recordingDispatcher) DispatchReplacement(
	_ context.Context, ab watchdog.AssignmentBinding, _ watchdog.Session,
) (watchdog.DispatchResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dispatched = append(d.dispatched, ab.ID)
	return watchdog.DispatchResult{Dispatched: true}, nil
}

func (d *recordingDispatcher) replacementRequested(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Contains(d.dispatched, id)
}

type recordingEvents struct {
	mu     sync.Mutex
	events []watchdog.Event
}

func (e *recordingEvents) Append(_ context.Context, ev watchdog.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
	return nil
}

func (e *recordingEvents) hasType(t watchdog.WatchdogEvent) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range e.events {
		if ev.Type == t {
			return true
		}
	}
	return false
}

type countingMetrics struct {
	mu                          sync.Mutex
	fresh, stale, reconciled    int
	recovered, fenced, replaced int
	sessionStates               map[watchdog.SessionState]int
	staleWithoutReconcile       int
	queuedClassifiedRunning     int
	deliveredClassifiedAdmitted int
	passiveWaitAllowed          int
}

func (m *countingMetrics) HeartbeatReceived(string) { m.mu.Lock(); m.fresh++; m.mu.Unlock() }
func (m *countingMetrics) HeartbeatFresh(string)    { m.mu.Lock(); m.fresh++; m.mu.Unlock() }
func (m *countingMetrics) HeartbeatMissed(string)   { m.mu.Lock(); m.fresh++; m.mu.Unlock() }
func (m *countingMetrics) HeartbeatStale(string)    { m.mu.Lock(); m.stale++; m.mu.Unlock() }
func (m *countingMetrics) ReconcileAttempted(string) {
	m.mu.Lock()
	m.reconciled++
	m.mu.Unlock()
}
func (m *countingMetrics) SessionRecovered(string) {
	m.mu.Lock()
	m.recovered++
	m.mu.Unlock()
}
func (m *countingMetrics) SessionFenced(string, watchdog.ReasonCode) {
	m.mu.Lock()
	m.fenced++
	m.mu.Unlock()
}
func (m *countingMetrics) ReplacementDispatched(string) {
	m.mu.Lock()
	m.replaced++
	m.mu.Unlock()
}
func (m *countingMetrics) PassiveWaitRejected(string) { m.mu.Lock(); m.fresh++; m.mu.Unlock() }
func (m *countingMetrics) SessionState(state watchdog.SessionState) {
	m.mu.Lock()
	if m.sessionStates == nil {
		m.sessionStates = map[watchdog.SessionState]int{}
	}
	m.sessionStates[state]++
	m.mu.Unlock()
}
func (m *countingMetrics) StaleWithoutReconcile(string) {
	m.mu.Lock()
	m.staleWithoutReconcile++
	m.mu.Unlock()
}
func (m *countingMetrics) QueuedClassifiedRunning(string) {
	m.mu.Lock()
	m.queuedClassifiedRunning++
	m.mu.Unlock()
}
func (m *countingMetrics) DeliveredClassifiedAdmitted(string) {
	m.mu.Lock()
	m.deliveredClassifiedAdmitted++
	m.mu.Unlock()
}
func (m *countingMetrics) PassiveWaitAllowed(string) {
	m.mu.Lock()
	m.passiveWaitAllowed++
	m.mu.Unlock()
}

// runTicks drives the exported watchdog Run loop, delivering count ticks on the
// fake clock and then stopping it via context cancellation. The coordinator
// boundary can only reach inspection through Run, so this is the honest entry
// point; an internal per-session call would not prove the loop is wired.
func runTicks(t *testing.T, w *watchdog.Watchdog, clock *fakeClock, count int) error {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	for i := 0; i < count; i++ {
		clock.Tick()
		// Let the loop drain the tick before the next one.
		time.Sleep(2 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	case <-time.After(2 * time.Second):
		cancel()
		return errors.New("watchdog did not stop on context cancellation")
	}
}

func policy() watchdog.Policy {
	p := watchdog.DefaultPolicy()
	p.Heartbeat.Interval = time.Second
	p.Heartbeat.Timeout = 3 * time.Second
	p.StartupGracePeriod = 5 * time.Second
	return p
}

// blockingWatchdog simulates watchdog.Run: it returns only when its context is
// cancelled, exactly as the real loop does.
type blockingWatchdog struct {
	stopped chan struct{}
}

func newBlockingWatchdog() *blockingWatchdog {
	return &blockingWatchdog{stopped: make(chan struct{})}
}

func (b *blockingWatchdog) Run(ctx context.Context) error {
	<-ctx.Done()
	close(b.stopped)
	return ctx.Err()
}

// TestCoordinatorStopsWatchdogWhenStateMachineCompletes asserts the watchdog is
// stopped once the state machine finishes. A watchdog that keeps ticking after
// the coordinator turn ends would hold the process open indefinitely.
func TestCoordinatorStopsWatchdogWhenStateMachineCompletes(t *testing.T) {
	wd := newBlockingWatchdog()

	done := make(chan error, 1)
	go func() {
		done <- RunWithWatchdog(context.Background(), wd, func(context.Context) error {
			return nil // state machine succeeds immediately
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("expected nil when both complete, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithWatchdog hung: state machine completed but the watchdog was never stopped")
	}

	select {
	case <-wd.stopped:
	default:
		t.Error("watchdog Run did not observe context cancellation")
	}
}
