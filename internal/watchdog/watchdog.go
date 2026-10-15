package watchdog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type Watchdog struct {
	policy     Policy
	clock      Clock
	registry   SessionRegistry
	reconciler Reconciler
	dispatcher ReplacementDispatcher
	events     EventWriter
	metrics    Metrics
	mu         sync.Mutex
	missedMap  map[string]int
}

func New(
	policy Policy,
	clock Clock,
	registry SessionRegistry,
	reconciler Reconciler,
	dispatcher ReplacementDispatcher,
	events EventWriter,
	metrics Metrics,
) *Watchdog {
	return &Watchdog{
		policy:     policy,
		clock:      clock,
		registry:   registry,
		reconciler: reconciler,
		dispatcher: dispatcher,
		events:     events,
		metrics:    metrics,
		missedMap:  make(map[string]int),
	}
}

func (w *Watchdog) Run(ctx context.Context) error {
	if err := w.policy.Validate(); err != nil {
		return err
	}

	ticker := w.clock.NewTicker(w.policy.Heartbeat.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C():
			if err := w.inspectAll(ctx, now); err != nil {
				return err
			}
		}
	}
}

// isRejection reports whether an error is a per-session refusal of an invalid
// transition, as opposed to a failure that must abort the run.
//
// This distinction is fail-closed by construction. A rejection means one session
// claimed a state its admission record does not support; the violation is
// already counted, and the other sessions still need watching.
//
// Everything else — a fence that did not take, a replacement that could not be
// dispatched, an audit event that was lost, a registry that could not be read —
// is a containment or durability failure. Those must propagate, or a session
// would stay uncontained while the loop kept reporting success.
func isRejection(err error) bool {
	return errors.Is(err, ErrDeliveredNotAdmitted) || errors.Is(err, ErrQueuedNotRunning)
}

func (w *Watchdog) inspectAll(ctx context.Context, now time.Time) error {
	sessions, err := w.registry.ListActive(ctx)
	if err != nil {
		return fmt.Errorf("list active sessions: %w", err)
	}
	for _, s := range sessions {
		err := w.inspectSession(ctx, now, s)
		if err == nil {
			continue
		}
		// A per-session rejection must not blind the watchdog to the remaining
		// sessions in this tick, so it is recorded and the sweep continues.
		if isRejection(err) {
			w.recordRejection(ctx, now, s, err)
			continue
		}
		return err
	}
	return nil
}

// recordRejection writes a per-session audit event for a rejection the sweep
// deliberately continued past.
//
// The hard counter alone is not enough here. It is aggregate and unlabelled: an
// operator sees "3 rejections" with no session ID and no indication of which
// session is stuck claiming RUNNING while the registry says DELIVERED. That is
// precisely the gap in which a stall stays silent, which R146 exists to
// prevent. This event names the session, its generation and the reason.
//
// A failure to write this event is deliberately NOT propagated. One rejected
// session must not end the sweep — that is the fail-closed stop D1 removed —
// and it is not a containment failure either: the session was refused, not left
// uncontained. The lost event is counted as a missed heartbeat observation so
// it stays visible without aborting the loop.
func (w *Watchdog) recordRejection(ctx context.Context, now time.Time, session Session, reason error) {
	if err := w.appendEvent(ctx, Event{
		Type:       EventSessionRejection,
		SessionID:  session.ID,
		Generation: session.Generation,
		Reason:     rejectionReason(reason),
		OccurredAt: now,
	}); err != nil {
		w.metrics.HeartbeatMissed(session.ID)
	}
}

// rejectionReason maps a refusal error onto its reason code.
func rejectionReason(err error) ReasonCode {
	switch {
	case errors.Is(err, ErrDeliveredNotAdmitted):
		return ReasonDeliveredNotAdmitted
	case errors.Is(err, ErrQueuedNotRunning):
		return ReasonQueuedNotRunning
	default:
		return ReasonInvalidStateTransition
	}
}

func (w *Watchdog) inspectSession(ctx context.Context, now time.Time, session Session) error {
	state, err := w.registry.HeartbeatState(ctx, session.ID, session.Generation)
	if err != nil {
		return w.handleRegistryFailure(ctx, now, session, err)
	}

	if state.Generation != session.Generation {
		w.metrics.SessionState(SessionFenced)
		return w.fenceAndReplace(ctx, session, ReasonGenerationMismatch)
	}

	if state.Status == SessionCompleted || state.Status == SessionFenced || state.Status == SessionInterrupted {
		w.metrics.SessionState(state.Status)
		return nil
	}

	if state.LastReceivedAt.IsZero() {
		if now.Sub(session.AdmittedAt) >= w.policy.StartupGracePeriod {
			return w.reconcile(ctx, session, ReasonHeartbeatNeverSeen)
		}
		return nil
	}

	elapsed := now.Sub(state.LastReceivedAt)
	if elapsed < w.policy.Heartbeat.Timeout {
		w.metrics.HeartbeatFresh(session.ID)
		// SessionUnknown is accepted as running because it is the registry's own
		// "no state recorded" answer for an admitted session; holding it suspect
		// would contradict the registry rather than constrain the agent.
		if err := w.classifyRunning(session, state); err != nil {
			return err
		}
		w.resetMissed(session.ID)
		return nil
	}

	// Heartbeat is stale (elapsed >= timeout). The stale path never promotes a
	// session, so the observed state is recorded as-is.
	w.metrics.SessionState(state.Status)

	// Track missed heartbeats
	missed := w.incrementMissed(session.ID)
	state.MissedHeartbeats = missed

	// First miss -> MARK_SUSPECT
	if missed == 1 && w.policy.Actions.OnFirstMiss == ActionMarkSuspect {
		w.metrics.SessionState(SessionSuspect)
		w.metrics.HeartbeatMissed(session.ID)
		if err := w.appendEvent(ctx, Event{
			Type:       EventHeartbeatMissed,
			SessionID:  session.ID,
			Generation: session.Generation,
			Reason:     ReasonHeartbeatMissed,
			OccurredAt: now,
		}); err != nil {
			return err
		}
		// Don't reconcile yet - wait for maxMissed
		return nil
	}

	// Missed count >= maxMissed -> RECONCILE_SESSION
	if missed >= w.policy.Heartbeat.MaxMissed {
		w.metrics.HeartbeatStale(session.ID)
		return w.reconcile(ctx, session, ReasonHeartbeatStale)
	}

	// Still in SUSPECT state, waiting for more misses to reach maxMissed
	w.metrics.SessionState(SessionSuspect)
	w.metrics.HeartbeatMissed(session.ID)
	return nil
}

// appendEvent writes an audit event. A dropped event is an audit gap, so the
// error is surfaced rather than discarded: every call site either returns it or
// folds it into the error it is already producing.
func (w *Watchdog) appendEvent(ctx context.Context, event Event) error {
	if err := w.events.Append(ctx, event); err != nil {
		return fmt.Errorf("append watchdog event %s for session %s: %w",
			event.Type, event.SessionID, err)
	}
	return nil
}

// classifyRunning validates a claimed transition to RUNNING and rejects the
// invalid ones.
//
// The registry's own state for the session (session.State) is the admission
// record; HeartbeatState.Status is what the session claims about itself. A
// claim of RUNNING without admission evidence is an invalid transition: a
// DELIVERED session was never admitted, and a QUEUED session was never started,
// so neither may be treated as RUNNING. Those are rejected here and counted as
// hard violations.
//
// Observing a session that is merely in DELIVERED or QUEUED state is not a
// violation — there is no rejected transition — so it records only the session
// state and leaves the hard counters at zero.
// transitionGuard decides a session's reported state from its admission record
// and the claim it makes about itself. It returns the state to record and an
// error when the claim is refused.
//
// The guard is a package-level variable so the mutation suite can substitute a
// permissive decision and drive it through the real inspectSession path, rather
// than asserting against a hand-written copy of the logic. Production always
// runs guardAdmitOnly.
var transitionGuard = guardAdmitOnly

// guardAdmitOnly admits a RUNNING claim only when the admission record supports
// it. A session the registry still holds as DELIVERED was never admitted, and
// one it holds as QUEUED was never started, so neither may be treated as
// RUNNING.
func guardAdmitOnly(session Session, state HeartbeatState) (SessionState, error) {
	if state.Status != SessionRunning && state.Status != SessionUnknown {
		return state.Status, nil
	}

	switch session.State {
	case SessionDelivered:
		return SessionDelivered, fmt.Errorf("reject DELIVERED->RUNNING for session %s: %w",
			session.ID, ErrDeliveredNotAdmitted)
	case SessionQueued:
		return SessionQueued, fmt.Errorf("reject QUEUED->RUNNING for session %s: %w",
			session.ID, ErrQueuedNotRunning)
	}

	return SessionRunning, nil
}

func (w *Watchdog) classifyRunning(session Session, state HeartbeatState) error {
	resolved, err := transitionGuard(session, state)

	// A refusal is the only place a hard violation counter may move. An
	// ordinary observation has no refused transition, so it records the
	// session state alone and leaves the hard counters at zero.
	if err != nil {
		if session.State == SessionDelivered {
			w.metrics.DeliveredClassifiedAdmitted(session.ID)
		}
		if session.State == SessionQueued {
			w.metrics.QueuedClassifiedRunning(session.ID)
		}
	}

	w.metrics.SessionState(resolved)
	return err
}

func (w *Watchdog) handleRegistryFailure(ctx context.Context, now time.Time, session Session, err error) error {
	w.metrics.HeartbeatMissed(session.ID)
	if appendErr := w.appendEvent(ctx, Event{
		Type:       EventHeartbeatMissed,
		SessionID:  session.ID,
		Generation: session.Generation,
		Reason:     ReasonRegistryUnavailable,
		OccurredAt: now,
	}); appendErr != nil {
		return errors.Join(fmt.Errorf("registry failure for session %s: %w", session.ID, err), appendErr)
	}
	return fmt.Errorf("registry failure for session %s: %w", session.ID, err)
}

func (w *Watchdog) reconcile(ctx context.Context, session Session, reason ReasonCode) error {
	w.metrics.ReconcileAttempted(session.ID)
	if err := w.appendEvent(ctx, Event{
		Type:       EventHeartbeatStale,
		SessionID:  session.ID,
		Generation: session.Generation,
		Reason:     reason,
		OccurredAt: w.clock.Now(),
	}); err != nil {
		return err
	}

	reconcileCtx, cancel := context.WithTimeout(ctx, w.policy.Reconcile.Timeout)
	defer cancel()

	result, err := w.reconciler.Reconcile(reconcileCtx, session)
	if err == nil && result.Running && result.Generation == session.Generation {
		w.metrics.SessionRecovered(session.ID)
		w.resetMissed(session.ID)
		return w.appendEvent(ctx, Event{
			Type:       EventSessionRecovered,
			SessionID:  session.ID,
			Generation: session.Generation,
			OccurredAt: w.clock.Now(),
		})
	}

	return w.fenceAndReplace(ctx, session, ReasonReconcileFailed)
}

func (w *Watchdog) fenceAndReplace(ctx context.Context, session Session, reason ReasonCode) error {
	if err := w.registry.Fence(ctx, FenceRequest{
		SessionID:  session.ID,
		Generation: session.Generation,
		Reason:     reason,
	}); err != nil {
		return fmt.Errorf("fence session %s: %w", session.ID, err)
	}

	w.metrics.SessionFenced(session.ID, reason)
	if err := w.appendEvent(ctx, Event{
		Type:       EventSessionFenced,
		SessionID:  session.ID,
		Generation: session.Generation,
		Reason:     reason,
		OccurredAt: w.clock.Now(),
	}); err != nil {
		return err
	}

	// Fence first, dispatch second. Dispatching a replacement while the old
	// generation is still uncontained lets two generations write.
	_, err := w.dispatcher.DispatchReplacement(ctx, session.Assignment, session)
	if err != nil {
		return fmt.Errorf("dispatch replacement for session %s: %w", session.ID, err)
	}

	w.metrics.ReplacementDispatched(session.ID)
	return nil
}

func (w *Watchdog) getMissed(sessionID string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.missedMap[sessionID]
}

func (w *Watchdog) incrementMissed(sessionID string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.missedMap[sessionID]++
	return w.missedMap[sessionID]
}

func (w *Watchdog) resetMissed(sessionID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.missedMap, sessionID)
}
