package watchdog

import "context"

type SessionRegistry interface {
	ListActive(ctx context.Context) ([]Session, error)
	HeartbeatState(ctx context.Context, sessionID string, generation uint64) (HeartbeatState, error)
	Fence(ctx context.Context, request FenceRequest) error
}

type Reconciler interface {
	Reconcile(ctx context.Context, session Session) (ReconcileResult, error)
}

type ReplacementDispatcher interface {
	DispatchReplacement(ctx context.Context, assignment AssignmentBinding, oldSession Session) (DispatchResult, error)
}

type EventWriter interface {
	Append(ctx context.Context, event Event) error
}
