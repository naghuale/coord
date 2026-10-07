package executor

import (
	"context"
	"coord/envelope"
	"coord/fsm"
	"fmt"
)

// Receipt commits local completion only after a correlated coordinator RESULT_ACK.
// The owner of the executor loop serializes this with Accept, Heartbeat and Result.
func (x *Executor) Receipt(ctx context.Context, e envelope.Envelope) error {
	if err := x.validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if e.Kind != envelope.ResultAck {
		return fmt.Errorf("not a result receipt")
	}
	runs, err := x.Checkpoint.Load()
	if err != nil {
		return fmt.Errorf("load result receipt checkpoint: %w", err)
	}
	r, ok := runs[e.Run]
	if !ok || e.To != x.Transport.WhoAmI() || e.To != r.OwnerSession || e.Fence != r.Fence || e.Task != r.Task || e.Attempt != r.Attempt || e.TargetSHA != r.TargetSHA {
		return fmt.Errorf("stale result receipt")
	}
	coordinator := r.CoordinatorSession
	resultID := r.ResultMessageID
	for _, pending := range r.Outbox {
		if pending.Kind == envelope.Result && pending.MessageID == e.InReplyTo {
			if coordinator == "" {
				coordinator = pending.To
			}
			if resultID == "" {
				resultID = pending.MessageID
			}
		}
	}
	if coordinator == "" || e.From != coordinator || resultID == "" || e.InReplyTo != resultID || r.Result == nil || e.ResultAckPayload.Outcome != r.Result.Outcome {
		return fmt.Errorf("uncorrelated result receipt")
	}
	if r.Terminal() {
		return nil
	}
	if r.Status != fsm.ResultReady {
		return fmt.Errorf("receipt before result readiness")
	}
	r.Status = fsm.Failed
	if e.ResultAckPayload.Outcome == "SUCCESS" {
		r.Status = fsm.Done
	}
	r.CoordinatorSession = coordinator
	r.ResultMessageID = resultID
	r.Outbox = nil
	r.Blocker = ""
	if r.Seen == nil {
		r.Seen = map[string]bool{}
	}
	r.Seen[e.MessageID] = true
	runs[r.RunID] = r
	if err = x.Checkpoint.Save(runs); err != nil {
		return fmt.Errorf("persist committed result receipt: %w", err)
	}
	return nil
}
