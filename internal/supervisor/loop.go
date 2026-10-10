package supervisor

import (
	"context"
	"errors"
	"fmt"

	"coord/internal/agentsession"
)

type Loop struct {
	Registry agentsession.Registry
	Process  Process
	Clock    Clock
	Writer   ReceiptWriter
	Policy   Policy
}

func (s *Loop) Run(ctx context.Context, req RunRequest) (Receipt, error) {
	if s.Registry == nil || s.Process == nil || s.Clock == nil {
		return Receipt{}, errors.New("supervisor dependencies are not configured")
	}
	receipt := Receipt{SessionID: req.Session.SessionID, Generation: req.Session.Generation, Events: []Event{}}
	if req.Session.Status != agentsession.StatusAdmitted && req.Session.Status != agentsession.StatusActive {
		return receipt, fmt.Errorf("session is not admitted: %s", req.Session.Status)
	}
	if err := s.Process.Start(ctx, req.Command, req.Environment, req.WorkingDirectory); err != nil {
		return receipt, fmt.Errorf("start process: %w", err)
	}
	ticker := s.Clock.NewTicker(s.Policy.HeartbeatInterval)
	defer ticker.Stop()
	failures := 0
	sequence := uint64(0)
	revision := req.Session.RegistryRevision

	finish := func(fenced bool, reason FenceReason, cause error) (Receipt, error) {
		receipt.Fenced = fenced
		receipt.FenceReason = reason
		receipt.ExitCode = s.Process.ExitCode()
		if fenced {
			fenceCtx, cancel := context.WithTimeout(context.Background(), s.Policy.HeartbeatTimeout)
			_ = s.Registry.Fence(fenceCtx, agentsession.FenceRequest{SessionID: req.Session.SessionID, Generation: req.Session.Generation, Reason: string(reason), IdempotencyKey: agentsession.IdempotencyKey("fence", req.ProjectID, req.AssignmentID, req.Session.SessionID, req.Session.Generation, 0, string(reason))})
			cancel()
		}
		completeCtx, cancel := context.WithTimeout(context.Background(), s.Policy.HeartbeatTimeout)
		_ = s.Registry.Complete(completeCtx, agentsession.CompleteRequest{SessionID: req.Session.SessionID, Generation: req.Session.Generation, ExitCode: receipt.ExitCode, Fenced: fenced, IdempotencyKey: agentsession.IdempotencyKey("complete", req.ProjectID, req.AssignmentID, req.Session.SessionID, req.Session.Generation, 0, req.LeaseTokenDigest)})
		cancel()
		if s.Writer != nil {
			_ = s.Writer.WriteReceipt(context.Background(), receipt)
		}
		return receipt, cause
	}

	for {
		select {
		case err := <-s.Process.Done():
			return finish(false, "", err)
		case <-ctx.Done():
			_ = s.Process.TerminateGroup(context.Background(), s.Policy.GracefulStopTimeout)
			return finish(true, FenceContext, ctx.Err())
		case <-ticker.C():
			sequence++
			hbCtx, cancel := context.WithTimeout(ctx, s.Policy.HeartbeatTimeout)
			result, err := s.Registry.Heartbeat(hbCtx, agentsession.HeartbeatRequest{ProjectID: req.ProjectID, AssignmentID: req.AssignmentID, SessionID: req.Session.SessionID, Generation: req.Session.Generation, Sequence: sequence, LeaseTokenDigest: req.LeaseTokenDigest, RegistryRevision: revision, IdempotencyKey: agentsession.IdempotencyKey("heartbeat", req.ProjectID, req.AssignmentID, req.Session.SessionID, req.Session.Generation, sequence, req.LeaseTokenDigest)})
			cancel()
			if err != nil {
				failures++
				if failures >= s.Policy.MaxConsecutiveFailures {
					_ = s.Process.TerminateGroup(context.Background(), s.Policy.GracefulStopTimeout)
					return finish(true, FenceHeartbeat, err)
				}
				continue
			}
			failures = 0
			receipt.Events = append(receipt.Events, Event{Type: "HEARTBEAT", Status: string(result.Status), Revision: result.RegistryRevision, At: s.Clock.Now()})
			if result.Generation != req.Session.Generation {
				_ = s.Process.TerminateGroup(context.Background(), s.Policy.GracefulStopTimeout)
				return finish(true, FenceGeneration, errors.New("registry generation mismatch"))
			}
			if result.LeaseTokenDigest != req.LeaseTokenDigest {
				_ = s.Process.TerminateGroup(context.Background(), s.Policy.GracefulStopTimeout)
				return finish(true, FenceLease, errors.New("registry lease digest mismatch"))
			}
			switch result.Status {
			case agentsession.StatusFenced, agentsession.StatusRevoked, agentsession.StatusExpired, agentsession.StatusRejected:
				_ = s.Process.TerminateGroup(context.Background(), s.Policy.GracefulStopTimeout)
				return finish(true, FenceRegistryStatus, fmt.Errorf("terminal registry status: %s", result.Status))
			}
			revision = result.RegistryRevision
		}
	}
}
