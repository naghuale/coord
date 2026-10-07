package executor

import (
	"context"
	"coord/envelope"
	"coord/fsm"
	"fmt"
	"reflect"
)

// Result persists a resumable result before delivery. Transport delivery does
// not prove coordinator commit, so RESULT_READY remains until an external receipt.
func (x *Executor) Result(ctx context.Context, task envelope.Envelope, p envelope.ResultPayload) error {
	if err := x.validate(); err != nil {
		return err
	}
	runs, err := x.Checkpoint.Load()
	if err != nil {
		return err
	}
	r, ok := runs[task.Run]
	if !ok || r.Status != fsm.ResultReady || r.Fence != task.Fence || r.OwnerSession != x.Transport.WhoAmI() {
		return fmt.Errorf("result not ready or stale executor")
	}
	if err := x.checkTask(r, task); err != nil {
		return err
	}
	if !r.Deadline.After(x.Now()) {
		return fmt.Errorf("TASK deadline exceeded: %w", context.DeadlineExceeded)
	}
	if r.Result != nil && !reflect.DeepEqual(*r.Result, p) {
		return fmt.Errorf("result already checkpointed with different payload")
	}
	if r.ResultMessageID != "" {
		return x.flush(ctx, r.RunID)
	}
	e := x.reply(task, envelope.Result)
	e.ResultPayload = &p
	if err = e.Validate(); err != nil {
		return err
	}
	r.Result = &p
	r.ResultMessageID = e.MessageID
	return x.deliver(ctx, r, e)
}

// Replay resends checkpointed messages after the transport is reconnected.
// A new session must receive a fresh, fenced TASK instead of replaying old identities.
func (x *Executor) Replay(ctx context.Context) error {
	if err := x.validate(); err != nil {
		return err
	}
	runs, err := x.Checkpoint.Load()
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.OwnerSession != x.Transport.WhoAmI() {
			return fmt.Errorf("replay owner mismatch")
		}
		if err = x.flush(ctx, r.RunID); err != nil {
			return err
		}
	}
	return nil
}
