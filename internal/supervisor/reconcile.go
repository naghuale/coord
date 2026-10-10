package supervisor

import (
	"context"
	"errors"
	"fmt"

	"coord/internal/agentsession"
)

// ErrSessionOrphaned indicates that the registry session is active but its
// local process is absent.
var ErrSessionOrphaned = errors.New("session orphaned: registry active, no local process")

// Reconciler repairs registry state when an admitted local process disappears.
type Reconciler struct {
	Registry agentsession.Registry
}

// ReconcileRequest identifies a session and the local process state to reconcile.
type ReconcileRequest struct {
	Session          agentsession.Session
	ProjectID        string
	AssignmentID     string
	LeaseTokenDigest string
	ProcessAlive     bool
}

// Reconcile fences and completes an active registry session whose process has
// disappeared. Terminal sessions are completed without another fence.
func (r *Reconciler) Reconcile(ctx context.Context, req ReconcileRequest) error {
	if req.ProcessAlive {
		return nil
	}
	if r.Registry == nil {
		return errors.New("reconciler registry is not configured")
	}

	switch req.Session.Status {
	case agentsession.StatusFenced, agentsession.StatusRevoked,
		agentsession.StatusExpired, agentsession.StatusRejected:
		return r.Registry.Complete(ctx, agentsession.CompleteRequest{
			SessionID:      req.Session.SessionID,
			Generation:     req.Session.Generation,
			ExitCode:       -1,
			Fenced:         false,
			IdempotencyKey: agentsession.IdempotencyKey("reconcile-complete", req.ProjectID, req.AssignmentID, req.Session.SessionID, req.Session.Generation, 0, req.LeaseTokenDigest),
		})
	case agentsession.StatusAdmitted, agentsession.StatusActive:
		fenceErr := r.Registry.Fence(ctx, agentsession.FenceRequest{
			SessionID:      req.Session.SessionID,
			Generation:     req.Session.Generation,
			Reason:         string(FenceHeartbeat),
			IdempotencyKey: agentsession.IdempotencyKey("reconcile-fence", req.ProjectID, req.AssignmentID, req.Session.SessionID, req.Session.Generation, 0, string(FenceHeartbeat)),
		})
		completeErr := r.Registry.Complete(ctx, agentsession.CompleteRequest{
			SessionID:      req.Session.SessionID,
			Generation:     req.Session.Generation,
			ExitCode:       -1,
			Fenced:         true,
			IdempotencyKey: agentsession.IdempotencyKey("reconcile-complete", req.ProjectID, req.AssignmentID, req.Session.SessionID, req.Session.Generation, 0, req.LeaseTokenDigest),
		})
		if fenceErr != nil || completeErr != nil {
			return errors.Join(ErrSessionOrphaned, fenceErr, completeErr)
		}
		return ErrSessionOrphaned
	default:
		return fmt.Errorf("unsupported registry session status %q", req.Session.Status)
	}
}
