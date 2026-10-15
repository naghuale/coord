package watchdog

import "sync"

// Metrics records watchdog observations. Implementations may use Prometheus,
// in-memory counters for tests, or no-ops.
//
// The interface has two classes of counter, and conflating them is a defect.
//
// Operational counters (everything above the hard block) describe normal
// activity. They MAY be non-zero in correct operation and carry no defect
// signal on their own.
//
// Hard violation counters count REJECTED violations, never state
// observations. They MUST stay zero in correct operation and may only be
// incremented from a rejection branch — a point where an invalid transition or
// a passive wait was detected and refused.
//
// Emitting a hard counter for an ordinary observation inverts its meaning: the
// counter becomes baseline traffic, stops being a violation signal, and any
// alert or threshold built on it misfires permanently. This is why the counter
// lives at the rejection site rather than at the observation site.
type Metrics interface {
	// Operational counters.
	HeartbeatReceived(sessionID string)
	HeartbeatFresh(sessionID string)
	HeartbeatMissed(sessionID string)
	HeartbeatStale(sessionID string)
	ReconcileAttempted(sessionID string)
	SessionRecovered(sessionID string)
	SessionFenced(sessionID string, reason ReasonCode)
	ReplacementDispatched(sessionID string)
	PassiveWaitRejected(sessionID string)
	SessionState(state SessionState)

	// Hard violation counters.
	//
	// Wiring status, stated explicitly so the gap is not mistaken for
	// enforcement:
	//
	//   DeliveredClassifiedAdmitted, QueuedClassifiedRunning
	//       Wired. Emitted from Watchdog.classifyRunning when the guard refuses
	//       a DELIVERED->RUNNING or QUEUED->RUNNING transition. A rejected
	//       session is additionally written to the audit log as a
	//       SESSION_REJECTION event naming the session, generation and reason,
	//       because the counter is aggregate and cannot identify the offender.
	//
	//   StaleWithoutReconcile, PassiveWaitAllowed
	//       NOT WIRED. Declared and specified, but they belong to the dispatch
	//       integration boundary, which is not implemented: registry-backed
	//       fencing and remote process termination are NOT_EVALUATED. They read
	//       zero today because no code path can emit them. That is a recorded,
	//       accepted gap and must not be read as active enforcement.
	StaleWithoutReconcile(sessionID string)
	QueuedClassifiedRunning(sessionID string)
	DeliveredClassifiedAdmitted(sessionID string)
	PassiveWaitAllowed(sessionID string)
}

// NoopMetrics discards all observations.
type NoopMetrics struct{}

func (NoopMetrics) HeartbeatReceived(string)           {}
func (NoopMetrics) HeartbeatFresh(string)              {}
func (NoopMetrics) HeartbeatMissed(string)             {}
func (NoopMetrics) HeartbeatStale(string)              {}
func (NoopMetrics) ReconcileAttempted(string)          {}
func (NoopMetrics) SessionRecovered(string)            {}
func (NoopMetrics) SessionFenced(string, ReasonCode)   {}
func (NoopMetrics) ReplacementDispatched(string)       {}
func (NoopMetrics) PassiveWaitRejected(string)         {}
func (NoopMetrics) SessionState(SessionState)          {}
func (NoopMetrics) StaleWithoutReconcile(string)       {}
func (NoopMetrics) QueuedClassifiedRunning(string)     {}
func (NoopMetrics) DeliveredClassifiedAdmitted(string) {}
func (NoopMetrics) PassiveWaitAllowed(string)          {}

// TestMetrics is an in-memory implementation for tests.
type TestMetrics struct {
	mu                               sync.Mutex
	HeartbeatReceivedCount           int
	HeartbeatFreshCount              int
	HeartbeatMissedCount             int
	HeartbeatStaleCount              int
	ReconcileAttemptedCount          int
	SessionRecoveredCount            int
	SessionFencedCount               int
	ReplacementDispatchedCount       int
	PassiveWaitRejectedCount         int
	SessionStateCounts               map[SessionState]int
	StaleWithoutReconcileCount       int
	QueuedClassifiedRunningCount     int
	DeliveredClassifiedAdmittedCount int
	PassiveWaitAllowedCount          int
}

func (m *TestMetrics) HeartbeatReceived(string) {
	m.mu.Lock()
	m.HeartbeatReceivedCount++
	m.mu.Unlock()
}
func (m *TestMetrics) HeartbeatFresh(string) {
	m.mu.Lock()
	m.HeartbeatFreshCount++
	m.mu.Unlock()
}
func (m *TestMetrics) HeartbeatMissed(string) {
	m.mu.Lock()
	m.HeartbeatMissedCount++
	m.mu.Unlock()
}
func (m *TestMetrics) HeartbeatStale(string) {
	m.mu.Lock()
	m.HeartbeatStaleCount++
	m.mu.Unlock()
}
func (m *TestMetrics) ReconcileAttempted(string) {
	m.mu.Lock()
	m.ReconcileAttemptedCount++
	m.mu.Unlock()
}
func (m *TestMetrics) SessionRecovered(string) {
	m.mu.Lock()
	m.SessionRecoveredCount++
	m.mu.Unlock()
}
func (m *TestMetrics) SessionFenced(_ string, _ ReasonCode) {
	m.mu.Lock()
	m.SessionFencedCount++
	m.mu.Unlock()
}
func (m *TestMetrics) ReplacementDispatched(string) {
	m.mu.Lock()
	m.ReplacementDispatchedCount++
	m.mu.Unlock()
}
func (m *TestMetrics) PassiveWaitRejected(string) {
	m.mu.Lock()
	m.PassiveWaitRejectedCount++
	m.mu.Unlock()
}
func (m *TestMetrics) SessionState(state SessionState) {
	m.mu.Lock()
	if m.SessionStateCounts == nil {
		m.SessionStateCounts = make(map[SessionState]int)
	}
	m.SessionStateCounts[state]++
	m.mu.Unlock()
}
func (m *TestMetrics) StaleWithoutReconcile(string) {
	m.mu.Lock()
	m.StaleWithoutReconcileCount++
	m.mu.Unlock()
}
func (m *TestMetrics) QueuedClassifiedRunning(string) {
	m.mu.Lock()
	m.QueuedClassifiedRunningCount++
	m.mu.Unlock()
}
func (m *TestMetrics) DeliveredClassifiedAdmitted(string) {
	m.mu.Lock()
	m.DeliveredClassifiedAdmittedCount++
	m.mu.Unlock()
}
func (m *TestMetrics) PassiveWaitAllowed(string) {
	m.mu.Lock()
	m.PassiveWaitAllowedCount++
	m.mu.Unlock()
}
