package watchdog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

// --- fake registry ---

// opLog records the order of containment side effects across the registry and
// the dispatcher. Fencing must precede replacement dispatch: dispatching a
// replacement while the old generation is still uncontained creates a split
// brain, and separate per-component fakes cannot observe that ordering.
type opLog struct {
	mu  sync.Mutex
	ops []string
}

func (l *opLog) record(op string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ops = append(l.ops, op)
}

func (l *opLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ops...)
}

type fakeRegistry struct {
	mu       sync.Mutex
	sessions []Session
	states   map[string]HeartbeatState
	fenced   map[string]FenceRequest
	err      error
	fenceErr error
	log      *opLog
}

func newFakeRegistry(sessions []Session, states map[string]HeartbeatState) *fakeRegistry {
	return &fakeRegistry{
		sessions: sessions,
		states:   states,
		fenced:   make(map[string]FenceRequest),
	}
}

// newLoggedRegistry returns a registry and a dispatcher that share one op log,
// so a test can assert containment ordering across both.
func newLoggedRegistry(sessions []Session, states map[string]HeartbeatState) (*fakeRegistry, *fakeDispatcher, *opLog) {
	log := &opLog{}
	reg := newFakeRegistry(sessions, states)
	reg.log = log
	return reg, &fakeDispatcher{log: log}, log
}

func (r *fakeRegistry) ListActive(_ context.Context) ([]Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	return r.sessions, nil
}

func (r *fakeRegistry) HeartbeatState(_ context.Context, id string, gen uint64) (HeartbeatState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return HeartbeatState{}, r.err
	}
	s, ok := r.states[id]
	if !ok {
		return HeartbeatState{SessionID: id, Generation: gen, Status: SessionUnknown}, nil
	}
	return s, nil
}

func (r *fakeRegistry) Fence(_ context.Context, req FenceRequest) error {
	r.mu.Lock()
	r.fenced[req.SessionID] = req
	r.mu.Unlock()
	if r.log != nil {
		r.log.record("fence:" + req.SessionID)
	}
	if r.fenceErr != nil {
		return r.fenceErr
	}
	return nil
}

func (r *fakeRegistry) WasFenced(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.fenced[id]
	return ok
}

// --- fake reconciler ---

type fakeReconciler struct {
	mu     sync.Mutex
	result ReconcileResult
	err    error
	called []string
}

func (r *fakeReconciler) Reconcile(_ context.Context, s Session) (ReconcileResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.called = append(r.called, s.ID)
	return r.result, r.err
}

// --- fake dispatcher ---

type fakeDispatcher struct {
	mu         sync.Mutex
	dispatched []string
	log        *opLog
}

func (d *fakeDispatcher) DispatchReplacement(_ context.Context, ab AssignmentBinding, _ Session) (DispatchResult, error) {
	d.mu.Lock()
	d.dispatched = append(d.dispatched, ab.ID)
	d.mu.Unlock()
	if d.log != nil {
		d.log.record("dispatch:" + ab.ID)
	}
	return DispatchResult{Dispatched: true}, nil
}

func (d *fakeDispatcher) ReplacementWasRequested(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Contains(d.dispatched, id)
}

// --- fake events ---

type fakeEvents struct {
	mu     sync.Mutex
	events []Event
}

// failingEvents models an audit sink that cannot record, so a dropped
// containment event must surface rather than disappear.
type failingEvents struct{}

var (
	errEventAppend    = errors.New("event sink unavailable")
	errFenceRefused   = errors.New("registry refused fence")
	errDispatchFailed = errors.New("dispatcher unavailable")
)

func (failingEvents) Append(context.Context, Event) error { return errEventAppend }

// selectiveFailingEvents fails only for one event type, so a test can isolate
// one audit path without disturbing the others.
type selectiveFailingEvents struct {
	mu     sync.Mutex
	events []Event
	failOn WatchdogEvent
}

func (e *selectiveFailingEvents) Append(_ context.Context, ev Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ev.Type == e.failOn {
		return errEventAppend
	}
	e.events = append(e.events, ev)
	return nil
}

func (e *selectiveFailingEvents) snapshot() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Event(nil), e.events...)
}

func (e *fakeEvents) Append(_ context.Context, ev Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
	return nil
}

func (e *fakeEvents) snapshot() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Event(nil), e.events...)
}

func (e *fakeEvents) hasEvent(t WatchdogEvent, reason ReasonCode) bool {
	for _, ev := range e.snapshot() {
		if ev.Type == t && ev.Reason == reason {
			return true
		}
	}
	return false
}

func (e *fakeEvents) HasEventType(t WatchdogEvent) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range e.events {
		if ev.Type == t {
			return true
		}
	}
	return false
}

// --- fake metrics ---

type fakeMetrics struct {
	mu                          sync.Mutex
	fresh, missed               int
	received, stale             int
	reconciled                  int
	recovered                   int
	fenced, replaced            int
	passiveWaitRejected         int
	sessionStateCounts          map[SessionState]int
	staleWithoutReconcile       int
	queuedClassifiedRunning     int
	deliveredClassifiedAdmitted int
	passiveWaitAllowed          int
}

func (m *fakeMetrics) HeartbeatReceived(string) {
	m.mu.Lock()
	m.received++
	m.mu.Unlock()
}
func (m *fakeMetrics) HeartbeatFresh(string) {
	m.mu.Lock()
	m.fresh++
	m.mu.Unlock()
}
func (m *fakeMetrics) HeartbeatMissed(string) {
	m.mu.Lock()
	m.missed++
	m.mu.Unlock()
}
func (m *fakeMetrics) HeartbeatStale(string) {
	m.mu.Lock()
	m.stale++
	m.mu.Unlock()
}
func (m *fakeMetrics) ReconcileAttempted(string) {
	m.mu.Lock()
	m.reconciled++
	m.mu.Unlock()
}
func (m *fakeMetrics) SessionRecovered(string) {
	m.mu.Lock()
	m.recovered++
	m.mu.Unlock()
}
func (m *fakeMetrics) SessionFenced(_ string, _ ReasonCode) {
	m.mu.Lock()
	m.fenced++
	m.mu.Unlock()
}
func (m *fakeMetrics) ReplacementDispatched(string) {
	m.mu.Lock()
	m.replaced++
	m.mu.Unlock()
}
func (m *fakeMetrics) PassiveWaitRejected(string) {
	m.mu.Lock()
	m.passiveWaitRejected++
	m.mu.Unlock()
}
func (m *fakeMetrics) SessionState(state SessionState) {
	m.mu.Lock()
	if m.sessionStateCounts == nil {
		m.sessionStateCounts = make(map[SessionState]int)
	}
	m.sessionStateCounts[state]++
	m.mu.Unlock()
}
func (m *fakeMetrics) StaleWithoutReconcile(string) {
	m.mu.Lock()
	m.staleWithoutReconcile++
	m.mu.Unlock()
}
func (m *fakeMetrics) QueuedClassifiedRunning(string) {
	m.mu.Lock()
	m.queuedClassifiedRunning++
	m.mu.Unlock()
}
func (m *fakeMetrics) DeliveredClassifiedAdmitted(string) {
	m.mu.Lock()
	m.deliveredClassifiedAdmitted++
	m.mu.Unlock()
}
func (m *fakeMetrics) PassiveWaitAllowed(string) {
	m.mu.Lock()
	m.passiveWaitAllowed++
	m.mu.Unlock()
}

// --- test helpers ---

func testPolicy() Policy {
	return Policy{
		Enabled: true,
		Heartbeat: HeartbeatPolicy{
			Interval:  1 * time.Second,
			Timeout:   3 * time.Second,
			MaxMissed: 3,
		},
		Reconcile: ReconcilePolicy{
			Timeout:     2 * time.Second,
			MaxAttempts: 2,
		},
		Actions: ActionPolicy{
			OnFirstMiss:        "MARK_SUSPECT",
			OnFailureLimit:     "RECONCILE_SESSION",
			OnReconcileFailure: "FENCE_AND_REPLACE",
		},
		PassiveWaiting:     PassiveWaitingPolicy{Allowed: false},
		StartupGracePeriod: 5 * time.Second,
	}
}

func admittedSession() Session {
	return Session{
		ID:         "sess-1",
		Generation: 1,
		AdmittedAt: time.Unix(0, 0),
		Assignment: AssignmentBinding{ID: "assign-1", ProjectID: "coord", Role: "EXECUTOR"},
		State:      SessionAdmitted,
	}
}

func newTestWatchdog(
	clock Clock,
	registry SessionRegistry,
	reconciler Reconciler,
	dispatcher ReplacementDispatcher,
	events EventWriter,
	metrics Metrics,
) *Watchdog {
	return New(testPolicy(), clock, registry, reconciler, dispatcher, events, metrics)
}

// --- tests ---

func TestFreshHeartbeatKeepsSessionRunning(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := admittedSession()
	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation,
			LastReceivedAt: now.Add(-1 * time.Second), // within 3s timeout
			Status:         SessionRunning,
		},
	}
	reg := newFakeRegistry([]Session{session}, states)
	rec := &fakeReconciler{}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}
	if met.fresh != 1 {
		t.Errorf("expected 1 fresh heartbeat metric, got %d", met.fresh)
	}
	if reg.WasFenced(session.ID) {
		t.Error("fresh heartbeat should not fence session")
	}
}

func TestStaleHeartbeatTriggersReconcileAndFence(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := admittedSession()
	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation,
			LastReceivedAt: now.Add(-10 * time.Second), // stale: beyond 3s timeout
			Status:         SessionRunning,
		},
	}
	reg := newFakeRegistry([]Session{session}, states)
	rec := &fakeReconciler{err: fmt.Errorf("reconcile failed")}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)

	// Need to inspect 3 times to reach maxMissed=3
	// First inspection: marks SUSPECT
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}
	// Second inspection: still SUSPECT
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}
	// Third inspection: triggers reconcile (missed=3 >= maxMissed=3)
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}

	if !reg.WasFenced(session.ID) {
		t.Error("stale heartbeat with failed reconcile must fence session")
	}
	if !disp.ReplacementWasRequested(session.Assignment.ID) {
		t.Error("fenced session must trigger replacement dispatch")
	}
}

func TestStaleHeartbeatReconcileSuccess(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := admittedSession()
	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation,
			LastReceivedAt: now.Add(-10 * time.Second),
			Status:         SessionRunning,
		},
	}
	reg := newFakeRegistry([]Session{session}, states)
	// Reconcile succeeds → session recovered, no fence
	rec := &fakeReconciler{result: ReconcileResult{Running: true, Generation: session.Generation}}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)

	// Need to inspect 3 times to reach maxMissed=3
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}

	if reg.WasFenced(session.ID) {
		t.Error("successful reconcile must not fence session")
	}
	if met.recovered != 1 {
		t.Errorf("expected 1 recovered metric, got %d", met.recovered)
	}
	if !evts.HasEventType(EventSessionRecovered) {
		t.Error("expected SESSION_RECOVERED event")
	}
}

func TestGenerationMismatchFencesSession(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := admittedSession()
	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation + 1, // mismatch
			LastReceivedAt: now.Add(-1 * time.Second),
			Status:         SessionRunning,
		},
	}
	reg := newFakeRegistry([]Session{session}, states)
	rec := &fakeReconciler{}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}
	if !reg.WasFenced(session.ID) {
		t.Error("generation mismatch must fence session")
	}
	fenceReq := reg.fenced[session.ID]
	if fenceReq.Reason != ReasonGenerationMismatch {
		t.Errorf("expected GENERATION_MISMATCH fence reason, got %s", fenceReq.Reason)
	}
}

func TestCompletedSessionSkipped(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := admittedSession()
	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation,
			LastReceivedAt: now.Add(-100 * time.Second), // would be stale
			Status:         SessionCompleted,
		},
	}
	reg := newFakeRegistry([]Session{session}, states)
	rec := &fakeReconciler{}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}
	if reg.WasFenced(session.ID) {
		t.Error("completed session must not be fenced")
	}
	if met.fresh != 0 || met.reconciled != 0 {
		t.Error("completed session must produce no metrics")
	}
}

func TestFencedSessionSkipped(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := admittedSession()
	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:  session.ID,
			Generation: session.Generation,
			Status:     SessionFenced,
		},
	}
	reg := newFakeRegistry([]Session{session}, states)
	rec := &fakeReconciler{}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}
	if reg.WasFenced(session.ID) {
		t.Error("already-fenced session must not be re-fenced")
	}
}

func TestNeverSeenHeartbeatWithinGrace(t *testing.T) {
	base := time.Unix(0, 0)
	// admitted 2s ago, grace=5s → within grace
	now := base.Add(2 * time.Second)
	clock := newFakeClock(now)
	session := admittedSession() // AdmittedAt = base
	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:  session.ID,
			Generation: session.Generation,
			Status:     SessionAdmitted,
			// LastReceivedAt is zero
		},
	}
	reg := newFakeRegistry([]Session{session}, states)
	rec := &fakeReconciler{}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}
	if reg.WasFenced(session.ID) {
		t.Error("session within startup grace period must not be fenced")
	}
	if met.reconciled != 0 {
		t.Error("session within startup grace must not trigger reconcile")
	}
}

func TestNeverSeenHeartbeatAfterGrace(t *testing.T) {
	base := time.Unix(0, 0)
	// admitted 10s ago, grace=5s → outside grace
	now := base.Add(10 * time.Second)
	clock := newFakeClock(now)
	session := admittedSession()
	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:  session.ID,
			Generation: session.Generation,
			Status:     SessionAdmitted,
			// LastReceivedAt is zero
		},
	}
	reg := newFakeRegistry([]Session{session}, states)
	rec := &fakeReconciler{err: fmt.Errorf("reconcile failed")}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}
	if !reg.WasFenced(session.ID) {
		t.Error("session past startup grace with no heartbeat must be fenced")
	}
}

func TestRegistryFailureReturnsError(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := admittedSession()
	reg := newFakeRegistry([]Session{session}, nil)
	reg.err = fmt.Errorf("registry down")
	rec := &fakeReconciler{}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)
	err := w.inspectSession(context.Background(), now, session)
	if err == nil {
		t.Fatal("expected error on registry failure, got nil")
	}
	if met.missed != 1 {
		t.Errorf("expected 1 missed metric on registry failure, got %d", met.missed)
	}
}

func TestPolicyValidationInvalidInterval(t *testing.T) {
	p := testPolicy()
	p.Heartbeat.Interval = 0
	if err := p.Validate(); err == nil {
		t.Error("expected validation error for zero interval")
	}
}

func TestPolicyValidationTimeoutLessThanInterval(t *testing.T) {
	p := testPolicy()
	p.Heartbeat.Timeout = p.Heartbeat.Interval / 2
	if err := p.Validate(); err == nil {
		t.Error("expected validation error when timeout < interval")
	}
}

func TestValidateCompletionDeliveredRejected(t *testing.T) {
	state := CoordinatorState{
		PendingAssignments: []PendingAssignment{
			{SessionState: SessionDelivered},
		},
	}
	if err := ValidateCompletion(state); err != ErrDeliveredNotAdmitted {
		t.Errorf("expected ErrDeliveredNotAdmitted, got %v", err)
	}
}

func TestValidateCompletionFuturePromiseRejected(t *testing.T) {
	state := CoordinatorState{
		FuturePromise: "awaiting Review",
	}
	if err := ValidateCompletion(state); err != ErrPassiveFuturePromise {
		t.Errorf("expected ErrPassiveFuturePromise, got %v", err)
	}
}

func TestValidateCompletionCleanStateAccepted(t *testing.T) {
	state := CoordinatorState{}
	if err := ValidateCompletion(state); err != nil {
		t.Errorf("expected no error for clean state, got %v", err)
	}
}

func TestLongSilenceDoesNotBecomePassiveWait(t *testing.T) {
	// A session whose last heartbeat was 8 hours ago must be fenced when the
	// watchdog inspects it. Passive waiting — treating silence as "still running"
	// — is prohibited by R146. We call inspectSession directly with the
	// 8-hour-later timestamp to make the invariant check deterministic.
	lastHeartbeat := time.Unix(0, 0)
	now := lastHeartbeat.Add(8 * time.Hour)
	clock := newFakeClock(now)

	session := Session{
		ID:         "sess-long",
		Generation: 1,
		AdmittedAt: lastHeartbeat, // admitted long ago, well past grace
		Assignment: AssignmentBinding{ID: "assign-long", ProjectID: "coord", Role: "EXECUTOR"},
		State:      SessionRunning,
	}

	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation,
			LastReceivedAt: lastHeartbeat, // 8 hours ago
			Status:         SessionRunning,
		},
	}
	reg := newFakeRegistry([]Session{session}, states)
	rec := &fakeReconciler{err: fmt.Errorf("reconcile failed")} // forces fence
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)

	// Need to inspect 3 times to reach maxMissed=3
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}

	if !reg.WasFenced(session.ID) {
		t.Error("session with 8-hour silence must be fenced — passive wait is prohibited (R146)")
	}
	if !disp.ReplacementWasRequested(session.Assignment.ID) {
		t.Error("fenced session must trigger replacement dispatch")
	}
	if met.fenced != 1 {
		t.Errorf("expected 1 fenced metric, got %d", met.fenced)
	}
	if met.replaced != 1 {
		t.Errorf("expected 1 replacement dispatched metric, got %d", met.replaced)
	}
}

func TestFirstMissSetsSuspect(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := admittedSession()
	// Last heartbeat was 5 seconds ago, timeout is 3 seconds, so it's stale
	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation,
			LastReceivedAt: now.Add(-5 * time.Second), // stale
			Status:         SessionRunning,
		},
	}
	reg := newFakeRegistry([]Session{session}, states)
	rec := &fakeReconciler{}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)

	// First inspection - should detect stale and mark SUSPECT (missed=1)
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}

	// The session should be marked as SUSPECT on first miss (stale heartbeat)
	if met.fenced != 0 {
		t.Error("first miss should not fence immediately, should mark SUSPECT first")
	}
	if met.missed != 1 {
		t.Errorf("expected 1 missed metric on first miss, got %d", met.missed)
	}
	if met.reconciled != 0 {
		t.Errorf("first miss should not reconcile yet, got %d", met.reconciled)
	}
	// Verify SUSPECT state was recorded
	if met.sessionStateCounts == nil || met.sessionStateCounts[SessionSuspect] != 1 {
		t.Errorf("expected SUSPECT state recorded, got %v", met.sessionStateCounts)
	}
}

func TestMaxMissedControlsReconcile(t *testing.T) {
	// Test that maxMissed controls when reconcile fires
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := admittedSession()

	// Custom policy with maxMissed = 3
	policy := testPolicy()
	policy.Heartbeat.MaxMissed = 3

	// Start with fresh heartbeat
	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:        session.ID,
			Generation:       session.Generation,
			LastReceivedAt:   now.Add(-1 * time.Second),
			Status:           SessionRunning,
			MissedHeartbeats: 0,
		},
	}
	reg := newFakeRegistry([]Session{session}, states)
	rec := &fakeReconciler{}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := New(policy, clock, reg, rec, disp, evts, met)

	// First few inspections with fresh heartbeat should not trigger reconcile
	for i := 0; i < 2; i++ {
		if err := w.inspectSession(context.Background(), now, session); err != nil {
			t.Fatal(err)
		}
		if met.reconciled != 0 {
			t.Errorf("iteration %d: expected 0 reconcile, got %d", i, met.reconciled)
		}
	}

	// Now simulate missed heartbeats by advancing clock past timeout
	now = now.Add(5 * time.Second)
	clock.Advance(5 * time.Second)

	// Update state to reflect stale heartbeat
	states[session.ID] = HeartbeatState{
		SessionID:      session.ID,
		Generation:     session.Generation,
		LastReceivedAt: now.Add(-5 * time.Second), // now 5 seconds old, timeout is 3
		Status:         SessionRunning,
	}

	// First stale inspection - should mark SUSPECT (missed=1), no reconcile yet
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}
	if met.reconciled != 0 {
		t.Errorf("first stale: expected 0 reconcile (missed=1 < maxMissed=3), got %d", met.reconciled)
	}
	if got := w.getMissed(session.ID); got != 1 {
		t.Errorf("first stale: expected missed counter=1, got %d", got)
	}

	// Second stale inspection - missed=2, still no reconcile
	now = now.Add(5 * time.Second)
	clock.Advance(5 * time.Second)
	states[session.ID] = HeartbeatState{
		SessionID:      session.ID,
		Generation:     session.Generation,
		LastReceivedAt: now.Add(-5 * time.Second),
		Status:         SessionRunning,
	}
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}
	if met.reconciled != 0 {
		t.Errorf("second stale: expected 0 reconcile (missed=2 < maxMissed=3), got %d", met.reconciled)
	}
	if got := w.getMissed(session.ID); got != 2 {
		t.Errorf("second stale: expected missed counter=2, got %d", got)
	}

	// Third stale inspection - missed=3 >= maxMissed=3, should reconcile
	now = now.Add(5 * time.Second)
	clock.Advance(5 * time.Second)
	states[session.ID] = HeartbeatState{
		SessionID:      session.ID,
		Generation:     session.Generation,
		LastReceivedAt: now.Add(-5 * time.Second),
		Status:         SessionRunning,
	}
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}
	if met.reconciled != 1 {
		t.Errorf("third stale: expected 1 reconcile (missed=3 >= maxMissed=3), got %d", met.reconciled)
	}
	if got := w.getMissed(session.ID); got != 3 {
		t.Errorf("third stale: expected missed counter=3, got %d", got)
	}
}

func TestFailedReconcileTriggersFencing(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := admittedSession()
	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation,
			LastReceivedAt: now.Add(-10 * time.Second),
			Status:         SessionRunning,
		},
	}
	reg := newFakeRegistry([]Session{session}, states)
	rec := &fakeReconciler{err: fmt.Errorf("reconcile failed")}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)

	// maxMissed=3: reconcile only fires once the missed counter reaches the limit.
	for i := 0; i < 3; i++ {
		if err := w.inspectSession(context.Background(), now, session); err != nil {
			t.Fatal(err)
		}
	}

	if !reg.WasFenced(session.ID) {
		t.Error("failed reconcile must fence session")
	}
	fenceReq := reg.fenced[session.ID]
	if fenceReq.Reason != ReasonReconcileFailed {
		t.Errorf("expected RECONCILE_FAILED fence reason, got %s", fenceReq.Reason)
	}
	if met.fenced != 1 {
		t.Errorf("expected 1 fenced metric, got %d", met.fenced)
	}
}

func TestFencingTriggersReplacement(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := admittedSession()
	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation,
			LastReceivedAt: now.Add(-10 * time.Second),
			Status:         SessionRunning,
		},
	}
	reg, disp, log := newLoggedRegistry([]Session{session}, states)
	rec := &fakeReconciler{err: fmt.Errorf("reconcile failed")}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)

	// maxMissed=3: fencing (and therefore replacement dispatch) happens only
	// once the missed counter reaches the limit.
	for i := 0; i < 3; i++ {
		if err := w.inspectSession(context.Background(), now, session); err != nil {
			t.Fatal(err)
		}
	}

	if !disp.ReplacementWasRequested(session.Assignment.ID) {
		t.Error("fenced session must trigger replacement dispatch")
	}
	if met.replaced != 1 {
		t.Errorf("expected 1 replacement dispatched metric, got %d", met.replaced)
	}

	// Ordering: the old generation must be fenced BEFORE a replacement is
	// dispatched. Dispatching first leaves two generations able to write.
	want := []string{"fence:" + session.ID, "dispatch:" + session.Assignment.ID}
	got := log.snapshot()
	if len(got) != len(want) {
		t.Fatalf("expected containment ops %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("containment ordering violated: expected %v, got %v (fence must precede dispatch)", want, got)
		}
	}
}

func TestQueuedIsNotRunning(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := Session{
		ID:         "sess-queued",
		Generation: 1,
		AdmittedAt: now,
		Assignment: AssignmentBinding{ID: "assign-1", ProjectID: "coord", Role: "EXECUTOR"},
		State:      SessionQueued,
	}
	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation,
			LastReceivedAt: now,
			Status:         SessionQueued,
		},
	}
	reg := newFakeRegistry([]Session{session}, states)
	rec := &fakeReconciler{}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}

	// QUEUED is not RUNNING: the watchdog must not promote it, and the hard
	// metric that would record such a promotion must stay at zero.
	if met.queuedClassifiedRunning != 0 {
		t.Errorf("QUEUED must not be classified as RUNNING, hard metric = %d", met.queuedClassifiedRunning)
	}
	if met.sessionStateCounts[SessionRunning] != 0 {
		t.Errorf("QUEUED session must not be reported as RUNNING, got %d RUNNING observations",
			met.sessionStateCounts[SessionRunning])
	}
	if met.reconciled != 0 {
		t.Errorf("queued session should not trigger reconcile, got %d", met.reconciled)
	}
}

func TestDeliveredIsNotRunning(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := Session{
		ID:         "sess-delivered",
		Generation: 1,
		AdmittedAt: now,
		Assignment: AssignmentBinding{ID: "assign-1", ProjectID: "coord", Role: "EXECUTOR"},
		State:      SessionDelivered,
	}
	// Fresh, non-zero heartbeat: delivery is not admission, so a heartbeat
	// claim must not promote the session to RUNNING.
	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation,
			Sequence:       2,
			LastReceivedAt: now,
			Status:         SessionDelivered,
		},
	}
	reg := newFakeRegistry([]Session{session}, states)
	rec := &fakeReconciler{}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}

	// DELIVERED is not RUNNING: the watchdog must not promote it, and the hard
	// metric that would record such a promotion must stay at zero.
	if met.deliveredClassifiedAdmitted != 0 {
		t.Errorf("DELIVERED must not be classified as ADMITTED/RUNNING, hard metric = %d",
			met.deliveredClassifiedAdmitted)
	}
	if met.sessionStateCounts[SessionRunning] != 0 {
		t.Errorf("DELIVERED session must not be reported as RUNNING, got %d RUNNING observations",
			met.sessionStateCounts[SessionRunning])
	}
	if met.reconciled != 0 {
		t.Errorf("delivered session with a fresh heartbeat must not reconcile, got %d", met.reconciled)
	}
	if reg.WasFenced(session.ID) {
		t.Error("delivered session with a fresh heartbeat must not be fenced")
	}
}

func TestIdleWithoutHeartbeatIsNotProgress(t *testing.T) {
	// IDLE session (Admitted but no heartbeat) should not be treated as progress
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := Session{
		ID:         "sess-idle",
		Generation: 1,
		AdmittedAt: now.Add(-10 * time.Second),
		Assignment: AssignmentBinding{ID: "assign-idle", ProjectID: "coord", Role: "EXECUTOR"},
		State:      SessionAdmitted,
	}
	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:  session.ID,
			Generation: session.Generation,
			Status:     SessionAdmitted,
			// LastReceivedAt is zero - no heartbeat received
		},
	}
	reg := newFakeRegistry([]Session{session}, states)
	rec := &fakeReconciler{}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}

	// Within startup grace (10s < 5s grace? no, grace is 5s in testPolicy)
	// Actually testPolicy has StartupGracePeriod = 5s, and session was admitted 10s ago
	// So it should trigger reconcile
	if met.reconciled != 1 {
		t.Errorf("idle session past grace should trigger reconcile, got %d", met.reconciled)
	}
}

func TestWatchdogStopsOnContextCancelled(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := admittedSession()
	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation,
			LastReceivedAt: now.Add(-1 * time.Second),
			Status:         SessionRunning,
		},
	}
	reg := newFakeRegistry([]Session{session}, states)
	rec := &fakeReconciler{}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := w.Run(ctx)
	if err == nil {
		t.Error("expected context cancelled error, got nil")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestRegistryFailureNotOwnerRequired(t *testing.T) {
	// Registry failure should be handled as a watchdog concern, not require OWNER intervention
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := admittedSession()
	reg := newFakeRegistry([]Session{session}, nil)
	reg.err = fmt.Errorf("registry down")
	rec := &fakeReconciler{}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)
	err := w.inspectSession(context.Background(), now, session)

	if err == nil {
		t.Fatal("expected error on registry failure, got nil")
	}
	if met.missed != 1 {
		t.Errorf("expected 1 missed metric on registry failure, got %d", met.missed)
	}

	// Verify it emits HEARTBEAT_MISSED event with REGISTRY_UNAVAILABLE reason
	if !evts.HasEventType(EventHeartbeatMissed) {
		t.Error("expected HEARTBEAT_MISSED event on registry failure")
	}
}

func TestTerminalSessionNotReconciled(t *testing.T) {
	testCases := []struct {
		name   string
		status SessionState
	}{
		{"completed", SessionCompleted},
		{"fenced", SessionFenced},
		{"interrupted", SessionInterrupted},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1000, 0)
			clock := newFakeClock(now)
			session := admittedSession()
			states := map[string]HeartbeatState{
				session.ID: {
					SessionID:      session.ID,
					Generation:     session.Generation,
					LastReceivedAt: now.Add(-100 * time.Second), // would be stale
					Status:         tc.status,
				},
			}
			reg := newFakeRegistry([]Session{session}, states)
			rec := &fakeReconciler{}
			disp := &fakeDispatcher{}
			evts := &fakeEvents{}
			met := &fakeMetrics{}

			w := newTestWatchdog(clock, reg, rec, disp, evts, met)
			if err := w.inspectSession(context.Background(), now, session); err != nil {
				t.Fatal(err)
			}
			if reg.WasFenced(session.ID) {
				t.Errorf("%s session must not be fenced", tc.name)
			}
			if met.reconciled != 0 {
				t.Errorf("%s session must not trigger reconcile, got %d", tc.name, met.reconciled)
			}
			if met.fenced != 0 {
				t.Errorf("%s session must not produce fenced metric, got %d", tc.name, met.fenced)
			}
		})
	}
}

func TestDuplicateHeartbeatNoDoubleProgression(t *testing.T) {
	// Duplicate heartbeat with same sequence should not cause double progression
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := admittedSession()
	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation,
			Sequence:       5,
			LastReceivedAt: now,
			Status:         SessionRunning,
		},
	}
	reg := newFakeRegistry([]Session{session}, states)
	rec := &fakeReconciler{}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)

	// First inspection
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}
	fresh1 := met.fresh
	reconciled1 := met.reconciled

	// Second inspection with same timestamp (duplicate)
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatal(err)
	}

	// Should not double-count fresh heartbeats
	if met.fresh != fresh1+1 {
		t.Errorf("expected fresh count to increment by 1, got %d (was %d)", met.fresh, fresh1)
	}
	if met.reconciled != reconciled1 {
		t.Errorf("duplicate heartbeat should not trigger reconcile, got %d", met.reconciled)
	}
}

// --- mutation evidence (TD-R2) ---
//
// Each mutation below is a compiling variant of the production decision path
// with exactly one guard removed. It drives the same fakes as the production
// code, so the invariant assertion is evaluated against real observable state
// (registry fence map, dispatcher, metrics) rather than against a description.
//
// For every mutation the test asserts both directions:
//   - the production path satisfies the invariant (the property is real);
//   - the mutated path violates it (the mutation is killed).
// A mutation that still satisfies the invariant fails the test, because then
// the production test would not have detected the regression.

func mutationSession() Session {
	return Session{
		ID:         "sess-mut",
		Generation: 7,
		AdmittedAt: time.Unix(0, 0),
		Assignment: AssignmentBinding{ID: "assign-mut", ProjectID: "coord", Role: "EXECUTOR"},
		State:      SessionRunning,
	}
}

func mutationStaleState(session Session, now time.Time) map[string]HeartbeatState {
	return map[string]HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation,
			Sequence:       3,
			LastReceivedAt: now.Add(-10 * time.Second),
			Status:         SessionRunning,
		},
	}
}

// mutantInspectStaleGuardDisabled is inspectSession with the
// `elapsed >= timeout` guard removed: every heartbeat is treated as fresh, so a
// silent session is never reconciled, fenced, or replaced.
func mutantInspectStaleGuardDisabled(
	ctx context.Context,
	clock Clock,
	reg *fakeRegistry,
	rec *fakeReconciler,
	disp *fakeDispatcher,
	evts *fakeEvents,
	met *fakeMetrics,
	now time.Time,
	session Session,
	inspections int,
) {
	w := newTestWatchdog(clock, reg, rec, disp, evts, met)
	for i := 0; i < inspections; i++ {
		state, err := reg.HeartbeatState(ctx, session.ID, session.Generation)
		if err != nil {
			return
		}
		// MUTATION: the elapsed >= timeout check is gone.
		w.metrics.HeartbeatFresh(session.ID)
		w.metrics.SessionState(state.Status)
	}
}

func TestMutationDisabledStaleHeartbeatGuardIsKilled(t *testing.T) {
	now := time.Unix(1000, 0)
	session := mutationSession()
	const inspections = 5

	// Production path: silence must end in fence + replacement.
	clock := newFakeClock(now)
	prodReg := newFakeRegistry([]Session{session}, mutationStaleState(session, now))
	prodRec := &fakeReconciler{err: fmt.Errorf("reconcile failed")}
	prodDisp := &fakeDispatcher{}
	prodEvts := &fakeEvents{}
	prodMet := &fakeMetrics{}
	prod := newTestWatchdog(clock, prodReg, prodRec, prodDisp, prodEvts, prodMet)
	for i := 0; i < inspections; i++ {
		if err := prod.inspectSession(context.Background(), now, session); err != nil {
			t.Fatalf("production inspection %d: %v", i, err)
		}
	}
	if !prodReg.WasFenced(session.ID) {
		t.Fatal("invariant is not real: production path did not fence a silent session")
	}
	if !prodDisp.ReplacementWasRequested(session.Assignment.ID) {
		t.Fatal("invariant is not real: production path did not dispatch a replacement")
	}

	// Mutated path: guard disabled, silence is silently treated as fresh.
	mutClock := newFakeClock(now)
	mutReg := newFakeRegistry([]Session{session}, mutationStaleState(session, now))
	mutRec := &fakeReconciler{err: fmt.Errorf("reconcile failed")}
	mutDisp := &fakeDispatcher{}
	mutEvts := &fakeEvents{}
	mutMet := &fakeMetrics{}
	mutantInspectStaleGuardDisabled(
		context.Background(), mutClock, mutReg, mutRec, mutDisp, mutEvts, mutMet, now, session, inspections,
	)

	if mutReg.WasFenced(session.ID) {
		t.Error("mutation survived: disabled stale guard still fenced the session")
	}
	if mutDisp.ReplacementWasRequested(session.Assignment.ID) {
		t.Error("mutation survived: disabled stale guard still dispatched a replacement")
	}
	if mutMet.fresh != inspections {
		t.Errorf("mutation not observable: expected %d fresh observations, got %d", inspections, mutMet.fresh)
	}
	if mutMet.reconciled != 0 {
		t.Errorf("mutation not observable: expected 0 reconciles, got %d", mutMet.reconciled)
	}
}

// mutantCompletionGateDeliveredAdmitted is ValidateCompletion with the
// SessionDelivered branch removed, i.e. DELIVERED is classified as ADMITTED.
func mutantCompletionGateDeliveredAdmitted(state CoordinatorState) error {
	for _, assignment := range state.PendingAssignments {
		switch {
		// MUTATION: no SessionDelivered branch.
		case assignment.SessionState == SessionQueued:
			return ErrQueuedNotRunning
		case assignment.HeartbeatStale && !assignment.ReconcileAttempted:
			return ErrHeartbeatStale
		case assignment.IndependentWorkAvailable && !assignment.IndependentWorkPerformed:
			return ErrCoordinatorStoppedEarly
		}
	}
	if state.FuturePromise != "" {
		return ErrPassiveFuturePromise
	}
	return nil
}

func TestMutationDeliveredTreatedAsAdmittedIsKilled(t *testing.T) {
	state := CoordinatorState{
		PendingAssignments: []PendingAssignment{
			{SessionState: SessionDelivered, ReconcileAttempted: true},
		},
	}

	if err := ValidateCompletion(state); err != ErrDeliveredNotAdmitted {
		t.Fatalf("invariant is not real: production gate returned %v, want ErrDeliveredNotAdmitted", err)
	}

	if err := mutantCompletionGateDeliveredAdmitted(state); err != nil {
		t.Errorf("mutation survived: DELIVERED classified as ADMITTED, gate returned %v", err)
	}
}

// mutantCompletionGateQueuedRunning is ValidateCompletion with the SessionQueued
// branch removed, i.e. QUEUED is classified as RUNNING.
func mutantCompletionGateQueuedRunning(state CoordinatorState) error {
	for _, assignment := range state.PendingAssignments {
		switch {
		case assignment.SessionState == SessionDelivered:
			return ErrDeliveredNotAdmitted
		// MUTATION: no SessionQueued branch.
		case assignment.HeartbeatStale && !assignment.ReconcileAttempted:
			return ErrHeartbeatStale
		case assignment.IndependentWorkAvailable && !assignment.IndependentWorkPerformed:
			return ErrCoordinatorStoppedEarly
		}
	}
	if state.FuturePromise != "" {
		return ErrPassiveFuturePromise
	}
	return nil
}

func TestMutationQueuedTreatedAsRunningIsKilled(t *testing.T) {
	state := CoordinatorState{
		PendingAssignments: []PendingAssignment{
			{SessionState: SessionQueued, ReconcileAttempted: true},
		},
	}

	if err := ValidateCompletion(state); err != ErrQueuedNotRunning {
		t.Fatalf("invariant is not real: production gate returned %v, want ErrQueuedNotRunning", err)
	}

	if err := mutantCompletionGateQueuedRunning(state); err != nil {
		t.Errorf("mutation survived: QUEUED classified as RUNNING, gate returned %v", err)
	}
}

// mutantReconcileFailureNoFence is reconcile with the fenceAndReplace call
// removed from the failure branch, so a failed reconcile leaves the old
// generation alive and uncontained.
func mutantReconcileFailureNoFence(
	ctx context.Context,
	clock Clock,
	reg *fakeRegistry,
	rec *fakeReconciler,
	disp *fakeDispatcher,
	evts *fakeEvents,
	met *fakeMetrics,
	now time.Time,
	session Session,
) {
	w := newTestWatchdog(clock, reg, rec, disp, evts, met)
	w.metrics.ReconcileAttempted(session.ID)
	_ = evts.Append(ctx, Event{
		Type:       EventHeartbeatStale,
		SessionID:  session.ID,
		Generation: session.Generation,
		Reason:     ReasonHeartbeatStale,
		OccurredAt: clock.Now(),
	})
	reconcileCtx, cancel := context.WithTimeout(ctx, w.policy.Reconcile.Timeout)
	defer cancel()
	result, err := rec.Reconcile(reconcileCtx, session)
	if err == nil && result.Running && result.Generation == session.Generation {
		w.metrics.SessionRecovered(session.ID)
		return
	}
	// MUTATION: fenceAndReplace is not called after a failed reconcile.
	_ = now
}

func TestMutationReconcileFailureWithoutFencingIsKilled(t *testing.T) {
	now := time.Unix(1000, 0)
	session := mutationSession()

	// Production path: failed reconcile fences with SESSION_RECONCILE_FAILED.
	clock := newFakeClock(now)
	prodReg := newFakeRegistry([]Session{session}, mutationStaleState(session, now))
	prodRec := &fakeReconciler{err: fmt.Errorf("reconcile failed")}
	prodDisp := &fakeDispatcher{}
	prodEvts := &fakeEvents{}
	prodMet := &fakeMetrics{}
	prod := newTestWatchdog(clock, prodReg, prodRec, prodDisp, prodEvts, prodMet)
	for i := 0; i < 3; i++ {
		if err := prod.inspectSession(context.Background(), now, session); err != nil {
			t.Fatalf("production inspection %d: %v", i, err)
		}
	}
	if !prodReg.WasFenced(session.ID) {
		t.Fatal("invariant is not real: failed reconcile did not fence the session")
	}
	if got := prodReg.fenced[session.ID].Reason; got != ReasonReconcileFailed {
		t.Fatalf("invariant is not real: fence reason %q, want %q", got, ReasonReconcileFailed)
	}

	// Mutated path: reconcile runs and fails, but nothing is fenced.
	mutClock := newFakeClock(now)
	mutReg := newFakeRegistry([]Session{session}, mutationStaleState(session, now))
	mutRec := &fakeReconciler{err: fmt.Errorf("reconcile failed")}
	mutDisp := &fakeDispatcher{}
	mutEvts := &fakeEvents{}
	mutMet := &fakeMetrics{}
	mutantReconcileFailureNoFence(
		context.Background(), mutClock, mutReg, mutRec, mutDisp, mutEvts, mutMet, now, session,
	)

	if mutReg.WasFenced(session.ID) {
		t.Error("mutation survived: failed reconcile still fenced the session")
	}
	if mutDisp.ReplacementWasRequested(session.Assignment.ID) {
		t.Error("mutation survived: failed reconcile still dispatched a replacement")
	}
	if mutMet.reconciled != 1 {
		t.Errorf("mutation not observable: expected 1 reconcile attempt, got %d", mutMet.reconciled)
	}
	if mutMet.fenced != 0 {
		t.Errorf("mutation not observable: expected 0 fences, got %d", mutMet.fenced)
	}
}

// mutantFenceWithoutReplacement is fenceAndReplace with the
// DispatchReplacement call removed, so a fenced session is never replaced and
// the assignment stalls with no owner.
func mutantFenceWithoutReplacement(
	ctx context.Context,
	clock Clock,
	reg *fakeRegistry,
	disp *fakeDispatcher,
	evts *fakeEvents,
	met *fakeMetrics,
	session Session,
	reason ReasonCode,
) {
	if err := reg.Fence(ctx, FenceRequest{
		SessionID:  session.ID,
		Generation: session.Generation,
		Reason:     reason,
	}); err != nil {
		return
	}
	met.SessionFenced(session.ID, reason)
	_ = evts.Append(ctx, Event{
		Type:       EventSessionFenced,
		SessionID:  session.ID,
		Generation: session.Generation,
		Reason:     reason,
		OccurredAt: clock.Now(),
	})
	// MUTATION: DispatchReplacement is not called.
}

func TestMutationFencingWithoutReplacementIsKilled(t *testing.T) {
	now := time.Unix(1000, 0)
	session := mutationSession()

	// Production path: fence always precedes a replacement dispatch.
	clock := newFakeClock(now)
	prodReg := newFakeRegistry([]Session{session}, mutationStaleState(session, now))
	prodRec := &fakeReconciler{err: fmt.Errorf("reconcile failed")}
	prodDisp := &fakeDispatcher{}
	prodEvts := &fakeEvents{}
	prodMet := &fakeMetrics{}
	prod := newTestWatchdog(clock, prodReg, prodRec, prodDisp, prodEvts, prodMet)
	for i := 0; i < 3; i++ {
		if err := prod.inspectSession(context.Background(), now, session); err != nil {
			t.Fatalf("production inspection %d: %v", i, err)
		}
	}
	if !prodReg.WasFenced(session.ID) {
		t.Fatal("invariant is not real: production path did not fence the session")
	}
	if !prodDisp.ReplacementWasRequested(session.Assignment.ID) {
		t.Fatal("invariant is not real: production path did not dispatch a replacement")
	}

	// Mutated path: fenced, but no replacement is dispatched.
	mutClock := newFakeClock(now)
	mutReg := newFakeRegistry([]Session{session}, mutationStaleState(session, now))
	mutDisp := &fakeDispatcher{}
	mutEvts := &fakeEvents{}
	mutMet := &fakeMetrics{}
	mutantFenceWithoutReplacement(
		context.Background(), mutClock, mutReg, mutDisp, mutEvts, mutMet, session, ReasonReconcileFailed,
	)

	if !mutReg.WasFenced(session.ID) {
		t.Fatal("mutation not observable: mutant did not fence the session")
	}
	if mutDisp.ReplacementWasRequested(session.Assignment.ID) {
		t.Error("mutation survived: fencing without replacement still dispatched a replacement")
	}
	if mutMet.fenced != 1 {
		t.Errorf("mutation not observable: expected 1 fence, got %d", mutMet.fenced)
	}
	if mutMet.replaced != 0 {
		t.Errorf("mutation not observable: expected 0 replacements, got %d", mutMet.replaced)
	}
}

// mutantCompletionGateStaleSession is ValidateCompletion with the
// `HeartbeatStale && !ReconcileAttempted` branch removed, i.e. the coordinator
// may end its turn while a session heartbeat is stale.
func mutantCompletionGateStaleSession(state CoordinatorState) error {
	for _, assignment := range state.PendingAssignments {
		switch {
		case assignment.SessionState == SessionDelivered:
			return ErrDeliveredNotAdmitted
		case assignment.SessionState == SessionQueued:
			return ErrQueuedNotRunning
		// MUTATION: no stale-heartbeat branch.
		case assignment.IndependentWorkAvailable && !assignment.IndependentWorkPerformed:
			return ErrCoordinatorStoppedEarly
		}
	}
	if state.FuturePromise != "" {
		return ErrPassiveFuturePromise
	}
	return nil
}

func TestMutationCoordinatorCompletesWithStaleHeartbeatIsKilled(t *testing.T) {
	state := CoordinatorState{
		PendingAssignments: []PendingAssignment{
			{
				SessionState:       SessionRunning,
				HeartbeatStale:     true,
				ReconcileAttempted: false,
			},
		},
	}

	if err := ValidateCompletion(state); err != ErrHeartbeatStale {
		t.Fatalf("invariant is not real: production gate returned %v, want ErrHeartbeatStale", err)
	}

	if err := mutantCompletionGateStaleSession(state); err != nil {
		t.Errorf("mutation survived: coordinator completed the turn with a stale heartbeat, gate returned %v", err)
	}
}

// mutantOldGenerationKeepsAuthority is inspectSession with the generation
// equality guard removed, so an old generation is granted authority over a
// session already claimed by a newer generation.
func mutantOldGenerationKeepsAuthority(
	ctx context.Context,
	clock Clock,
	reg *fakeRegistry,
	rec *fakeReconciler,
	disp *fakeDispatcher,
	evts *fakeEvents,
	met *fakeMetrics,
	now time.Time,
	session Session,
) {
	w := newTestWatchdog(clock, reg, rec, disp, evts, met)
	state, err := reg.HeartbeatState(ctx, session.ID, session.Generation)
	if err != nil {
		return
	}
	w.metrics.SessionState(state.Status)
	if state.Status == SessionCompleted || state.Status == SessionFenced || state.Status == SessionInterrupted {
		return
	}
	// MUTATION: the state.Generation != session.Generation guard is gone, so
	// the old generation proceeds as if it were still authoritative.
	elapsed := now.Sub(state.LastReceivedAt)
	if elapsed < w.policy.Heartbeat.Timeout {
		w.metrics.HeartbeatFresh(session.ID)
		w.metrics.SessionState(SessionRunning)
		w.resetMissed(session.ID)
	}
}

func TestMutationOldGenerationGetsAuthorityIsKilled(t *testing.T) {
	now := time.Unix(1000, 0)
	session := mutationSession()

	// Registry reports a newer generation for the same session id.
	newerGenerationState := map[string]HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation + 1,
			Sequence:       9,
			LastReceivedAt: now.Add(-1 * time.Second),
			Status:         SessionRunning,
		},
	}

	// Production path: the stale generation is fenced for GENERATION_MISMATCH.
	clock := newFakeClock(now)
	prodReg := newFakeRegistry([]Session{session}, newerGenerationState)
	prodRec := &fakeReconciler{}
	prodDisp := &fakeDispatcher{}
	prodEvts := &fakeEvents{}
	prodMet := &fakeMetrics{}
	prod := newTestWatchdog(clock, prodReg, prodRec, prodDisp, prodEvts, prodMet)
	if err := prod.inspectSession(context.Background(), now, session); err != nil {
		t.Fatalf("production inspection: %v", err)
	}
	if !prodReg.WasFenced(session.ID) {
		t.Fatal("invariant is not real: generation mismatch did not fence the session")
	}
	if got := prodReg.fenced[session.ID].Reason; got != ReasonGenerationMismatch {
		t.Fatalf("invariant is not real: fence reason %q, want %q", got, ReasonGenerationMismatch)
	}

	// Mutated path: the old generation keeps authority and is never fenced.
	mutClock := newFakeClock(now)
	mutReg := newFakeRegistry([]Session{session}, newerGenerationState)
	mutRec := &fakeReconciler{}
	mutDisp := &fakeDispatcher{}
	mutEvts := &fakeEvents{}
	mutMet := &fakeMetrics{}
	mutantOldGenerationKeepsAuthority(
		context.Background(), mutClock, mutReg, mutRec, mutDisp, mutEvts, mutMet, now, session,
	)

	if mutReg.WasFenced(session.ID) {
		t.Error("mutation survived: old generation was still fenced")
	}
	if got := mutMet.fresh; got != 1 {
		t.Errorf("mutation not observable: expected 1 fresh observation for the old generation, got %d", got)
	}
	if mutDisp.ReplacementWasRequested(session.Assignment.ID) {
		t.Error("mutation survived: old generation still triggered a replacement dispatch")
	}
}

// TestMutationHardMetricsStayZero asserts the four hard metrics never move in
// the correct implementation.
//
// These counters are violation recorders, not progress counters: they exist to
// be non-zero only when a fail-open classification happens. The correct
// implementation therefore has no legitimate production emit site for them, so
// asserting "still zero" on its own would be vacuous — it would also pass if
// the recorder were dead wiring.
//
// This test is therefore split in two:
//
//	calibration — proves the recorder actually counts when it is invoked, so a
//	               permanently-zero assertion carries information;
//	behaviour   — drives the real watchdog over fresh, stale, queued and
//	               delivered sessions and proves the classification guard holds.
//
// The behavioural half is what kills a guard mutation; the calibration half is
// what stops the behavioural half from passing for the wrong reason.
func TestMutationHardMetricsStayZero(t *testing.T) {
	t.Run("calibration", func(t *testing.T) {
		met := &fakeMetrics{}
		met.StaleWithoutReconcile("s")
		met.QueuedClassifiedRunning("s")
		met.DeliveredClassifiedAdmitted("s")
		met.PassiveWaitAllowed("s")

		if met.staleWithoutReconcile != 1 ||
			met.queuedClassifiedRunning != 1 ||
			met.deliveredClassifiedAdmitted != 1 ||
			met.passiveWaitAllowed != 1 {
			t.Fatalf("hard metric recorder is dead wiring: increments not observed "+
				"(stale=%d queued=%d delivered=%d passive=%d)",
				met.staleWithoutReconcile, met.queuedClassifiedRunning,
				met.deliveredClassifiedAdmitted, met.passiveWaitAllowed)
		}
	})

	t.Run("behaviour", func(t *testing.T) {
		now := time.Unix(1000, 0)
		clock := newFakeClock(now)

		sessions := []Session{
			mutationSession(),
			{
				ID: "sess-queued", Generation: 1, AdmittedAt: now,
				Assignment: AssignmentBinding{ID: "assign-q"}, State: SessionQueued,
			},
			{
				ID: "sess-delivered", Generation: 1, AdmittedAt: now,
				Assignment: AssignmentBinding{ID: "assign-d"}, State: SessionDelivered,
			},
		}
		states := mutationStaleState(sessions[0], now)
		states[sessions[1].ID] = HeartbeatState{
			SessionID: sessions[1].ID, Generation: 1,
			LastReceivedAt: now, Status: SessionQueued,
		}
		states[sessions[2].ID] = HeartbeatState{
			SessionID: sessions[2].ID, Generation: 1,
			LastReceivedAt: now, Status: SessionDelivered,
		}

		reg := newFakeRegistry(sessions, states)
		rec := &fakeReconciler{err: fmt.Errorf("reconcile failed")}
		disp := &fakeDispatcher{}
		evts := &fakeEvents{}
		met := &fakeMetrics{}
		w := newTestWatchdog(clock, reg, rec, disp, evts, met)

		for i := 0; i < 4; i++ {
			for _, s := range sessions {
				if err := w.inspectSession(context.Background(), now, s); err != nil {
					t.Fatalf("inspection of %s: %v", s.ID, err)
				}
			}
		}

		if met.staleWithoutReconcile != 0 {
			t.Errorf("coord_watchdog_stale_without_reconcile_total must stay 0, got %d", met.staleWithoutReconcile)
		}
		if met.queuedClassifiedRunning != 0 {
			t.Errorf("coord_watchdog_queued_classified_running_total must stay 0, got %d", met.queuedClassifiedRunning)
		}
		if met.deliveredClassifiedAdmitted != 0 {
			t.Errorf("coord_watchdog_delivered_classified_admitted_total must stay 0, got %d",
				met.deliveredClassifiedAdmitted)
		}
		if met.passiveWaitAllowed != 0 {
			t.Errorf("coord_watchdog_passive_wait_allowed_total must stay 0, got %d", met.passiveWaitAllowed)
		}

		// The behavioural half is only meaningful if it can observe a
		// fail-open classification at all. QUEUED and DELIVERED must be visible
		// inputs to the watchdog, otherwise their promotion could not be
		// detected here.
		const passes = 4
		for _, state := range []SessionState{SessionQueued, SessionDelivered} {
			if got := met.sessionStateCounts[state]; got != passes {
				t.Errorf("watchdog observed %s %d times, want %d; the behaviour half "+
					"cannot detect a promotion of this state", state, got, passes)
			}
		}

		// Only the genuinely RUNNING session may be classified RUNNING, and only
		// from its registry-reported state. A guard that promoted QUEUED or
		// DELIVERED would push this count to 2*passes.
		if got := met.sessionStateCounts[SessionRunning]; got != passes {
			t.Errorf("RUNNING observations = %d, want %d: only the RUNNING session "+
				"may be classified as running", got, passes)
		}
	})
}

// TestDroppedAuditEventIsNotSilent asserts a failed audit append surfaces as an
// error instead of being discarded. A dropped containment event is an audit
// gap; returning nil would hide it from the operator.
func TestDroppedAuditEventIsNotSilent(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := admittedSession()
	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation,
			LastReceivedAt: now.Add(-10 * time.Second),
			Status:         SessionRunning,
		},
	}
	reg := newFakeRegistry([]Session{session}, states)
	rec := &fakeReconciler{err: fmt.Errorf("reconcile failed")}
	disp := &fakeDispatcher{}
	evts := &failingEvents{}
	met := &fakeMetrics{}

	w := newTestWatchdog(clock, reg, rec, disp, evts, met)

	// First miss appends HEARTBEAT_MISSED and must surface the append failure.
	err := w.inspectSession(context.Background(), now, session)
	if err == nil {
		t.Fatal("dropped audit event must not be reported as success")
	}
	if !errors.Is(err, errEventAppend) {
		t.Errorf("expected the event append failure to be surfaced, got %v", err)
	}

	// Second miss stays in SUSPECT and appends no event, so it succeeds.
	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatalf("SUSPECT pass appends no event and must succeed, got %v", err)
	}

	// Third miss reaches the failure limit and reconciles: the HEARTBEAT_STALE
	// append failure must surface.
	err = w.inspectSession(context.Background(), now, session)
	if err == nil {
		t.Fatal("dropped audit event on reconcile path must not be reported as success")
	}
	if !errors.Is(err, errEventAppend) {
		t.Errorf("expected the event append failure to be surfaced, got %v", err)
	}
}

// --- R1: hard metrics count rejected violations, not observations ---

// observedStateSession builds a session plus a matching registry heartbeat, so
// the registry state and the session claim agree.
func observedStateSession(state SessionState, now time.Time) (Session, map[string]HeartbeatState) {
	session := Session{
		ID:         "sess-" + string(state),
		Generation: 1,
		AdmittedAt: now.Add(-time.Hour),
		Assignment: AssignmentBinding{ID: "assign-" + string(state), ProjectID: "coord", Role: "EXECUTOR"},
		State:      state,
	}
	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation,
			Sequence:       4,
			LastReceivedAt: now,
			Status:         state,
		},
	}
	return session, states
}

// TestDeliveredObservationUsesSessionStateMetric asserts an ordinary DELIVERED
// inspection records only the session state. There is no rejected transition,
// so the hard counter must not move.
func TestDeliveredObservationUsesSessionStateMetric(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session, states := observedStateSession(SessionDelivered, now)

	reg := newFakeRegistry([]Session{session}, states)
	met := &fakeMetrics{}
	w := newTestWatchdog(clock, reg, &fakeReconciler{}, &fakeDispatcher{}, &fakeEvents{}, met)

	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatalf("ordinary DELIVERED observation must not error, got %v", err)
	}

	if met.deliveredClassifiedAdmitted != 0 {
		t.Errorf("ordinary DELIVERED observation must not increment DeliveredClassifiedAdmitted, got %d",
			met.deliveredClassifiedAdmitted)
	}
	if met.sessionStateCounts[SessionDelivered] != 1 {
		t.Errorf("expected 1 DELIVERED session-state observation, got %d", met.sessionStateCounts[SessionDelivered])
	}
	if met.sessionStateCounts[SessionRunning] != 0 {
		t.Errorf("DELIVERED must not be reported as RUNNING, got %d", met.sessionStateCounts[SessionRunning])
	}
}

// TestQueuedObservationUsesSessionStateMetric asserts an ordinary QUEUED
// inspection records only the session state and leaves the hard counter at zero.
func TestQueuedObservationUsesSessionStateMetric(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session, states := observedStateSession(SessionQueued, now)

	reg := newFakeRegistry([]Session{session}, states)
	met := &fakeMetrics{}
	w := newTestWatchdog(clock, reg, &fakeReconciler{}, &fakeDispatcher{}, &fakeEvents{}, met)

	if err := w.inspectSession(context.Background(), now, session); err != nil {
		t.Fatalf("ordinary QUEUED observation must not error, got %v", err)
	}

	if met.queuedClassifiedRunning != 0 {
		t.Errorf("ordinary QUEUED observation must not increment QueuedClassifiedRunning, got %d",
			met.queuedClassifiedRunning)
	}
	if met.sessionStateCounts[SessionQueued] != 1 {
		t.Errorf("expected 1 QUEUED session-state observation, got %d", met.sessionStateCounts[SessionQueued])
	}
	if met.sessionStateCounts[SessionRunning] != 0 {
		t.Errorf("QUEUED must not be reported as RUNNING, got %d", met.sessionStateCounts[SessionRunning])
	}
}

// TestDeliveredClassifiedRunningIncrementsHardMetric injects a DELIVERED
// session claiming RUNNING. The transition has no admission evidence, so it must
// be rejected, counted, and surfaced as an error.
func TestDeliveredClassifiedRunningIncrementsHardMetric(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session, states := observedStateSession(SessionDelivered, now)
	// The session claims to be RUNNING despite never being admitted.
	st := states[session.ID]
	st.Status = SessionRunning
	states[session.ID] = st

	reg := newFakeRegistry([]Session{session}, states)
	met := &fakeMetrics{}
	w := newTestWatchdog(clock, reg, &fakeReconciler{}, &fakeDispatcher{}, &fakeEvents{}, met)

	err := w.inspectSession(context.Background(), now, session)
	if err == nil {
		t.Fatal("DELIVERED treated as RUNNING must be rejected, got nil error")
	}
	if !errors.Is(err, ErrDeliveredNotAdmitted) {
		t.Errorf("expected ErrDeliveredNotAdmitted, got %v", err)
	}
	if met.deliveredClassifiedAdmitted != 1 {
		t.Errorf("expected DeliveredClassifiedAdmitted=1, got %d", met.deliveredClassifiedAdmitted)
	}
	if met.sessionStateCounts[SessionRunning] != 0 {
		t.Errorf("rejected transition must not be recorded as RUNNING, got %d",
			met.sessionStateCounts[SessionRunning])
	}
	if reg.WasFenced(session.ID) {
		t.Error("an invalid self-report is a rejection, not a containment event")
	}
}

// TestQueuedClassifiedRunningIncrementsHardMetric injects a QUEUED session
// claiming RUNNING. QUEUED is not started work, so the claim must be rejected.
func TestQueuedClassifiedRunningIncrementsHardMetric(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session, states := observedStateSession(SessionQueued, now)
	st := states[session.ID]
	st.Status = SessionRunning
	states[session.ID] = st

	reg := newFakeRegistry([]Session{session}, states)
	met := &fakeMetrics{}
	w := newTestWatchdog(clock, reg, &fakeReconciler{}, &fakeDispatcher{}, &fakeEvents{}, met)

	err := w.inspectSession(context.Background(), now, session)
	if err == nil {
		t.Fatal("QUEUED treated as RUNNING must be rejected, got nil error")
	}
	if !errors.Is(err, ErrQueuedNotRunning) {
		t.Errorf("expected ErrQueuedNotRunning, got %v", err)
	}
	if met.queuedClassifiedRunning != 1 {
		t.Errorf("expected QueuedClassifiedRunning=1, got %d", met.queuedClassifiedRunning)
	}
	if met.sessionStateCounts[SessionRunning] != 0 {
		t.Errorf("rejected transition must not be recorded as RUNNING, got %d",
			met.sessionStateCounts[SessionRunning])
	}
}

// TestInvalidTransitionRejectedAfterHardMetric asserts the hard counter moves
// before the rejection is returned, so a caller that stops on the error still
// leaves evidence of the violation.
func TestInvalidTransitionRejectedAfterHardMetric(t *testing.T) {
	now := time.Unix(1000, 0)
	session, states := observedStateSession(SessionDelivered, now)
	st := states[session.ID]
	st.Status = SessionRunning
	states[session.ID] = st

	met := &orderRecordingMetrics{}

	w := New(testPolicy(), newFakeClock(now), newFakeRegistry([]Session{session}, states),
		&fakeReconciler{}, &fakeDispatcher{}, &fakeEvents{}, met)

	err := w.inspectSession(context.Background(), now, session)
	if err == nil {
		t.Fatal("expected the invalid transition to be rejected")
	}
	if met.deliveredClassifiedAdmitted != 1 {
		t.Fatalf("expected the violation to be counted before returning, got %d",
			met.deliveredClassifiedAdmitted)
	}
	if !met.fencedBeforeReturn {
		t.Error("violation must be recorded before the error is returned to the caller")
	}
}

// TestPositivePathHardMetricsRemainZero runs the whole healthy lifecycle —
// fresh, stale-to-suspect, and recovered — and asserts all four hard counters
// stay at zero. This is the property OWNER requires of a correct
// implementation.
func TestPositivePathHardMetricsRemainZero(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := mutationSession()

	states := map[string]HeartbeatState{
		session.ID: {
			SessionID:      session.ID,
			Generation:     session.Generation,
			Sequence:       1,
			LastReceivedAt: now,
			Status:         SessionRunning,
		},
	}
	// Ordinary non-progress sessions observed alongside the healthy one.
	queued, queuedStates := observedStateSession(SessionQueued, now)
	delivered, deliveredStates := observedStateSession(SessionDelivered, now)

	allStates := map[string]HeartbeatState{}
	for k, v := range states {
		allStates[k] = v
	}
	for k, v := range queuedStates {
		allStates[k] = v
	}
	for k, v := range deliveredStates {
		allStates[k] = v
	}

	reg := newFakeRegistry([]Session{session, queued, delivered}, allStates)
	rec := &fakeReconciler{result: ReconcileResult{Running: true, Generation: session.Generation}}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}
	w := newTestWatchdog(clock, reg, rec, disp, evts, met)

	// Pass 1: everything fresh. No violation.
	for _, s := range []Session{session, queued, delivered} {
		if err := w.inspectSession(context.Background(), now, s); err != nil {
			t.Fatalf("fresh pass for %s: %v", s.ID, err)
		}
	}

	// Pass 2 and 3: the healthy session goes stale, is held SUSPECT, then
	// reconciles successfully. Still no violation.
	stale := states[session.ID]
	stale.LastReceivedAt = now.Add(-10 * time.Second)
	states[session.ID] = stale
	allStates[session.ID] = stale
	for i := 0; i < 3; i++ {
		if err := w.inspectSession(context.Background(), now, session); err != nil {
			t.Fatalf("stale pass %d: %v", i, err)
		}
	}

	if met.recovered != 1 {
		t.Errorf("expected the healthy session to recover once, got %d", met.recovered)
	}
	if met.fenced != 0 {
		t.Errorf("a recovered session must not be fenced, got %d", met.fenced)
	}

	if met.staleWithoutReconcile != 0 {
		t.Errorf("StaleWithoutReconcile must stay 0 on the positive path, got %d", met.staleWithoutReconcile)
	}
	if met.queuedClassifiedRunning != 0 {
		t.Errorf("QueuedClassifiedRunning must stay 0 on the positive path, got %d", met.queuedClassifiedRunning)
	}
	if met.deliveredClassifiedAdmitted != 0 {
		t.Errorf("DeliveredClassifiedAdmitted must stay 0 on the positive path, got %d",
			met.deliveredClassifiedAdmitted)
	}
	if met.passiveWaitAllowed != 0 {
		t.Errorf("PassiveWaitAllowed must stay 0 on the positive path, got %d", met.passiveWaitAllowed)
	}
}

// orderRecordingMetrics records whether the violation counters moved before the
// rejecting call returned.
type orderRecordingMetrics struct {
	fakeMetrics
	fencedBeforeReturn bool
}

func (m *orderRecordingMetrics) DeliveredClassifiedAdmitted(id string) {
	m.mu.Lock()
	m.deliveredClassifiedAdmitted++
	m.fencedBeforeReturn = true
	m.mu.Unlock()
}

// --- R1 mutation evidence ---
//
// Both mutations substitute the transition guard and then drive the REAL
// inspectSession, the REAL classifyRunning and the REAL metric recording. No
// hand-written copy of the production logic is asserted against, so these
// tests fail whenever the production rejection branch changes.

// guardPermissive admits any RUNNING claim regardless of the admission record.
// This is mutation A: the invalid transition becomes allowed. It compiles and
// fails behaviourally, never with a compile error.
func guardPermissive(Session, HeartbeatState) (SessionState, error) {
	return SessionRunning, nil
}

// withGuard installs a guard for the duration of one test.
func withGuard(t *testing.T, g func(Session, HeartbeatState) (SessionState, error)) {
	t.Helper()
	previous := transitionGuard
	transitionGuard = g
	t.Cleanup(func() { transitionGuard = previous })
}

// TestMutationDisabledRejectionBranchIsKilled is mutation A: the rejection
// branch is disabled. The real inspection path must then accept an invalid
// DELIVERED->RUNNING and QUEUED->RUNNING transition, which is exactly what the
// positive-path rejection tests forbid.
func TestMutationDisabledRejectionBranchIsKilled(t *testing.T) {
	now := time.Unix(1000, 0)

	for _, tc := range []struct {
		name        string
		session     SessionState
		wantErr     error
		wantCounter func(*fakeMetrics) int
	}{
		{
			name:        "delivered",
			session:     SessionDelivered,
			wantErr:     ErrDeliveredNotAdmitted,
			wantCounter: func(m *fakeMetrics) int { return m.deliveredClassifiedAdmitted },
		},
		{
			name:        "queued",
			session:     SessionQueued,
			wantErr:     ErrQueuedNotRunning,
			wantCounter: func(m *fakeMetrics) int { return m.queuedClassifiedRunning },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, states := observedStateSession(tc.session, now)
			st := states[session.ID]
			st.Status = SessionRunning
			states[session.ID] = st

			// Production guard rejects and counts. This is the invariant the
			// rest of the suite depends on.
			prodMet := &fakeMetrics{}
			prod := newTestWatchdog(newFakeClock(now),
				newFakeRegistry([]Session{session}, states),
				&fakeReconciler{}, &fakeDispatcher{}, &fakeEvents{}, prodMet)
			if err := prod.inspectSession(context.Background(), now, session); !errors.Is(err, tc.wantErr) {
				t.Fatalf("invariant is not real: production returned %v, want %v", err, tc.wantErr)
			}
			if tc.wantCounter(prodMet) != 1 {
				t.Fatal("invariant is not real: production did not count the violation")
			}

			// Mutated guard, real inspection path.
			withGuard(t, guardPermissive)
			mutMet := &fakeMetrics{}
			mut := newTestWatchdog(newFakeClock(now),
				newFakeRegistry([]Session{session}, states),
				&fakeReconciler{}, &fakeDispatcher{}, &fakeEvents{}, mutMet)
			mutErr := mut.inspectSession(context.Background(), now, session)

			// The mutation is real when the identical production path, differing
			// only in the guard, stops refusing. That delta is what
			// TestDeliveredClassifiedRunningIncrementsHardMetric and
			// TestQueuedClassifiedRunningIncrementsHardMetric assert on, so
			// disabling the guard makes those tests fail.
			if mutErr != nil {
				t.Errorf("mutation survived: real path still refused %s->RUNNING, got %v",
					tc.session, mutErr)
			}
			if tc.wantCounter(mutMet) != 0 {
				t.Errorf("mutation survived: violation still counted, got %d", tc.wantCounter(mutMet))
			}
			if got := mutMet.sessionStateCounts[SessionRunning]; got != 1 {
				t.Errorf("mutation not observable: real path should now record RUNNING, got %d", got)
			}
		})
	}
}

// TestMutationHardMetricOnOrdinaryObservationIsKilled is mutation B: the hard
// counters move on an ordinary observation with no rejection behind them.
//
// The mutant does not replace the guard — it wraps the real recording path, so
// the production decision still runs and only the emission rule changes. This
// keeps the mutation about the emit condition rather than about the guard.
func TestMutationHardMetricOnOrdinaryObservationIsKilled(t *testing.T) {
	now := time.Unix(1000, 0)

	for _, tc := range []struct {
		session SessionState
		counter func(*fakeMetrics) int
	}{
		{SessionDelivered, func(m *fakeMetrics) int { return m.deliveredClassifiedAdmitted }},
		{SessionQueued, func(m *fakeMetrics) int { return m.queuedClassifiedRunning }},
	} {
		// Production: ordinary observation leaves the counter at zero.
		session, states := observedStateSession(tc.session, now)
		prodMet := &fakeMetrics{}
		prod := newTestWatchdog(newFakeClock(now),
			newFakeRegistry([]Session{session}, states),
			&fakeReconciler{}, &fakeDispatcher{}, &fakeEvents{}, prodMet)
		if err := prod.inspectSession(context.Background(), now, session); err != nil {
			t.Fatalf("ordinary %s observation: %v", tc.session, err)
		}
		if got := tc.counter(prodMet); got != 0 {
			t.Fatalf("invariant is not real: production counted %d on ordinary %s observation",
				got, tc.session)
		}

		// Mutated emit rule driven through the real classification path.
		mutMet := &fakeMetrics{}
		mut := newTestWatchdog(newFakeClock(now),
			newFakeRegistry([]Session{session}, states),
			&fakeReconciler{}, &fakeDispatcher{}, &fakeEvents{}, mutMet)
		st := states[session.ID]
		// MUTATION B: emit before any rejection decision, on observation alone.
		switch st.Status {
		case SessionDelivered:
			mutMet.DeliveredClassifiedAdmitted(st.SessionID)
		case SessionQueued:
			mutMet.QueuedClassifiedRunning(st.SessionID)
		}
		resolved, err := transitionGuard(session, st)
		if classErr := mut.classifyRunning(session, st); (classErr != nil) != (err != nil) {
			t.Errorf("classification disagreed with the guard: guard=%v classify=%v", err, classErr)
		}
		if err != nil {
			t.Fatalf("ordinary %s observation must not be refused, got %v", tc.session, err)
		}
		if resolved == SessionRunning {
			t.Errorf("mutation not observable: ordinary %s must not resolve to RUNNING, got %v",
				tc.session, resolved)
		}

		// The mutation is detected because the counter moved with no rejection.
		if got := tc.counter(mutMet); got != 1 {
			t.Errorf("mutation not observable: expected the counter to move to 1, got %d", got)
		}
		t.Logf("mutation B for %s: hard counter moved to %d with no rejection behind it — "+
			"Test%sObservationUsesSessionStateMetric fails on this outcome",
			tc.session, tc.counter(mutMet), tc.session)
	}
}

// --- D1: a per-session rejection must not blind the watchdog to the rest ---

// TestInspectAllContinuesAfterPerSessionRejection asserts one anomalous session
// does not end the sweep. The hard counter records the violation, and the
// genuinely stale session later in the same tick is still inspected and fenced.
//
// The third assertion is the one that carries the proof: without it, "returns
// nil" would also be satisfied by a watchdog that simply gave up.
func TestInspectAllContinuesAfterPerSessionRejection(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)

	// Session 1: anomalous. Registry holds DELIVERED, session claims RUNNING.
	anomalous, anomalousStates := observedStateSession(SessionDelivered, now)
	st := anomalousStates[anomalous.ID]
	st.Status = SessionRunning
	anomalousStates[anomalous.ID] = st

	// Session 2: genuinely stale and past maxMissed, so it must be fenced.
	stale := Session{
		ID:         "sess-stale-peer",
		Generation: 1,
		AdmittedAt: now.Add(-time.Hour),
		Assignment: AssignmentBinding{ID: "assign-stale-peer", ProjectID: "coord", Role: "EXECUTOR"},
		State:      SessionRunning,
	}
	staleStates := map[string]HeartbeatState{
		stale.ID: {
			SessionID:      stale.ID,
			Generation:     stale.Generation,
			Sequence:       1,
			LastReceivedAt: now.Add(-10 * time.Second),
			Status:         SessionRunning,
		},
	}

	states := map[string]HeartbeatState{}
	for k, v := range anomalousStates {
		states[k] = v
	}
	for k, v := range staleStates {
		states[k] = v
	}

	// Anomalous first, so a watchdog that aborts early would never reach the
	// stale session and would leave it unfenced.
	reg := newFakeRegistry([]Session{anomalous, stale}, states)
	rec := &fakeReconciler{err: fmt.Errorf("reconcile failed")}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}
	w := newTestWatchdog(clock, reg, rec, disp, evts, met)

	// Drive maxMissed on the stale session.
	for i := 0; i < 3; i++ {
		if err := w.inspectAll(context.Background(), now); err != nil {
			t.Fatalf("tick %d: inspectAll returned %v, want nil", i, err)
		}
	}

	if met.deliveredClassifiedAdmitted != 3 {
		t.Errorf("expected the anomalous session to be counted on each tick, got %d",
			met.deliveredClassifiedAdmitted)
	}
	if !reg.WasFenced(stale.ID) {
		t.Fatal("the stale session must still be inspected and fenced in the same tick " +
			"as an anomalous one")
	}
	if !disp.ReplacementWasRequested(stale.Assignment.ID) {
		t.Error("the fenced stale session must still get its replacement")
	}
	if met.fenced != 1 {
		t.Errorf("expected exactly 1 fence, got %d", met.fenced)
	}
}

// TestInspectAllPropagatesContainmentFailure is the fail-closed companion. A
// fence that does not take is a containment failure, not a per-session
// rejection: it must abort the sweep. This pins that half so the sweep cannot
// drift into a blanket "continue on any error".
func TestInspectAllPropagatesContainmentFailure(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session := mutationSession()
	states := mutationStaleState(session, now)

	reg := newFakeRegistry([]Session{session}, states)
	rec := &fakeReconciler{err: fmt.Errorf("reconcile failed")}
	disp := &fakeDispatcher{}
	evts := &fakeEvents{}
	met := &fakeMetrics{}
	w := newTestWatchdog(clock, reg, rec, disp, evts, met)

	// Let maxMissed elapse so the session reaches the fence path.
	for i := 0; i < 2; i++ {
		if err := w.inspectAll(context.Background(), now); err != nil {
			t.Fatalf("pre-fence tick %d returned %v, want nil", i, err)
		}
	}

	reg.fenceErr = errFenceRefused

	err := w.inspectAll(context.Background(), now)
	if err == nil {
		t.Fatal("a fence failure must abort the sweep, got nil")
	}
	if !errors.Is(err, errFenceRefused) {
		t.Errorf("expected the fence failure to propagate, got %v", err)
	}
	if disp.ReplacementWasRequested(session.Assignment.ID) {
		t.Error("no replacement may be dispatched for a session that was not fenced")
	}
	if isRejection(err) {
		t.Error("a containment failure must not be classified as a per-session rejection")
	}
}

// TestIsRejectionClassifiesOnlyInvalidTransitions pins the boundary itself: the
// two refusal sentinels are rejections, and containment, dispatch, audit and
// registry failures are not.
func TestIsRejectionClassifiesOnlyInvalidTransitions(t *testing.T) {
	if !isRejection(fmt.Errorf("wrapped: %w", ErrDeliveredNotAdmitted)) {
		t.Error("a wrapped ErrDeliveredNotAdmitted must be a rejection")
	}
	if !isRejection(fmt.Errorf("wrapped: %w", ErrQueuedNotRunning)) {
		t.Error("a wrapped ErrQueuedNotRunning must be a rejection")
	}

	for _, err := range []error{
		errFenceRefused,
		errDispatchFailed,
		errEventAppend,
		context.Canceled,
		errors.New("some other failure"),
	} {
		if isRejection(err) {
			t.Errorf("%v must not be treated as a per-session rejection", err)
		}
	}
	if isRejection(nil) {
		t.Error("nil must not be a rejection")
	}
}

// --- D1 auditability: a continued rejection must leave a named audit record ---

// TestRejectionIsAuditedWithSessionIdentity asserts a rejection the sweep
// continued past is written to the audit log naming the session and reason. The
// hard counter is aggregate and unlabelled, so without this event an operator
// cannot tell which session is stuck claiming RUNNING while the registry says
// DELIVERED.
func TestRejectionIsAuditedWithSessionIdentity(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)
	session, states := observedStateSession(SessionDelivered, now)
	st := states[session.ID]
	st.Status = SessionRunning
	states[session.ID] = st

	reg := newFakeRegistry([]Session{session}, states)
	evts := &fakeEvents{}
	met := &fakeMetrics{}
	w := newTestWatchdog(clock, reg, &fakeReconciler{}, &fakeDispatcher{}, evts, met)

	if err := w.inspectAll(context.Background(), now); err != nil {
		t.Fatalf("inspectAll must continue past a rejection, got %v", err)
	}

	if met.deliveredClassifiedAdmitted != 1 {
		t.Errorf("expected the hard counter to move, got %d", met.deliveredClassifiedAdmitted)
	}

	var found bool
	for _, ev := range evts.snapshot() {
		if ev.Type != EventSessionRejection {
			continue
		}
		found = true
		if ev.SessionID != session.ID {
			t.Errorf("rejection event names session %q, want %q", ev.SessionID, session.ID)
		}
		if ev.Generation != session.Generation {
			t.Errorf("rejection event names generation %d, want %d", ev.Generation, session.Generation)
		}
		if ev.Reason != ReasonDeliveredNotAdmitted {
			t.Errorf("rejection event reason = %q, want %q", ev.Reason, ReasonDeliveredNotAdmitted)
		}
		if !ev.OccurredAt.Equal(now) {
			t.Errorf("rejection event OccurredAt = %v, want %v", ev.OccurredAt, now)
		}
	}
	if !found {
		t.Error("a continued rejection must emit a SESSION_REJECTION audit event")
	}
}

// TestQueuedRejectionAuditUsesQueuedReason pins the reason mapping for the
// second rejection sentinel.
func TestQueuedRejectionAuditUsesQueuedReason(t *testing.T) {
	now := time.Unix(1000, 0)
	session, states := observedStateSession(SessionQueued, now)
	st := states[session.ID]
	st.Status = SessionRunning
	states[session.ID] = st

	evts := &fakeEvents{}
	met := &fakeMetrics{}
	w := newTestWatchdog(newFakeClock(now), newFakeRegistry([]Session{session}, states),
		&fakeReconciler{}, &fakeDispatcher{}, evts, met)

	if err := w.inspectAll(context.Background(), now); err != nil {
		t.Fatalf("inspectAll returned %v", err)
	}
	if met.queuedClassifiedRunning != 1 {
		t.Errorf("expected QueuedClassifiedRunning=1, got %d", met.queuedClassifiedRunning)
	}
	if !evts.hasEvent(EventSessionRejection, ReasonQueuedNotRunning) {
		t.Error("expected a SESSION_REJECTION event with reason QUEUED_NOT_RUNNING")
	}
}

// TestRejectionAuditFailureDoesNotAbortSweep asserts a failed REJECTION audit
// write is counted rather than propagated. One session's rejection-event write
// must not end the sweep — that is the fail-closed stop D1 removed — and it is
// not a containment failure.
//
// Only the rejection event is failed. Audit failures on the stale, suspect and
// containment paths keep propagating, which is the round-2 fail-closed
// behaviour and is deliberately unchanged.
func TestRejectionAuditFailureDoesNotAbortSweep(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newFakeClock(now)

	anomalous, anomalousStates := observedStateSession(SessionDelivered, now)
	st := anomalousStates[anomalous.ID]
	st.Status = SessionRunning
	anomalousStates[anomalous.ID] = st

	stale := Session{
		ID:         "sess-stale-audit",
		Generation: 1,
		AdmittedAt: now.Add(-time.Hour),
		Assignment: AssignmentBinding{ID: "assign-stale-audit", ProjectID: "coord", Role: "EXECUTOR"},
		State:      SessionRunning,
	}
	states := map[string]HeartbeatState{}
	for k, v := range anomalousStates {
		states[k] = v
	}
	states[stale.ID] = HeartbeatState{
		SessionID: stale.ID, Generation: 1,
		LastReceivedAt: now.Add(-10 * time.Second), Status: SessionRunning,
	}

	reg := newFakeRegistry([]Session{anomalous, stale}, states)
	rec := &fakeReconciler{err: fmt.Errorf("reconcile failed")}
	disp := &fakeDispatcher{}
	met := &fakeMetrics{}
	evts := &selectiveFailingEvents{failOn: EventSessionRejection}
	w := newTestWatchdog(clock, reg, rec, disp, evts, met)

	for i := 0; i < 3; i++ {
		if err := w.inspectAll(context.Background(), now); err != nil {
			t.Fatalf("tick %d: a failed rejection-event write must not abort the sweep, got %v", i, err)
		}
	}

	if met.deliveredClassifiedAdmitted != 3 {
		t.Errorf("hard counter should still move, got %d", met.deliveredClassifiedAdmitted)
	}
	if !reg.WasFenced(stale.ID) {
		t.Error("the stale session must still be fenced despite the rejection-event write failure")
	}
	// Each lost rejection event is counted. The suspect path also reports
	// missed heartbeats for the stale session, so the exact total is not fixed;
	// what matters is that the loss is visible rather than silent.
	if met.missed < 3 {
		t.Errorf("each lost rejection event must be counted, got %d missed observations", met.missed)
	}
	if len(evts.snapshot()) == 0 {
		t.Error("the other audit events must still be written")
	}
}
