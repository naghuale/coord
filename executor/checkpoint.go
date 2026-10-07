// Package executor provides persistent application ACK and progress bookkeeping.
// It does not execute user work or restart its own process.
package executor

import (
	"context"
	"coord/checkpoint"
	"coord/envelope"
	"coord/fsm"
	"coord/retry"
	"coord/transport"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Executor struct {
	Transport      transport.Transport
	Checkpoint     checkpoint.File
	Policy         retry.Policy
	UpdateInterval time.Duration
	SendTimeout    time.Duration
	Now            func() time.Time
}

func (x *Executor) validate() error {
	if x.Transport == nil || x.Transport.WhoAmI() == "" || x.Checkpoint.Path == "" || x.Now == nil || x.UpdateInterval <= 0 || x.SendTimeout <= 0 {
		return fmt.Errorf("invalid executor configuration")
	}
	return x.Policy.Validate()
}

// Accept verifies the base HEAD and persists ownership before acknowledging.
// The caller must acquire the worktree writer lock before Accept and hold it
// until process exit or explicit cancellation by the supervisor.
func (x *Executor) Accept(ctx context.Context, e envelope.Envelope) (fsm.Run, error) {
	if err := x.validate(); err != nil {
		return fsm.Run{}, err
	}
	if err := e.Validate(); err != nil {
		return fsm.Run{}, err
	}
	if e.Kind != envelope.Task || e.To != x.Transport.WhoAmI() {
		return fsm.Run{}, fmt.Errorf("not a TASK for this executor")
	}
	runs, err := x.Checkpoint.Load()
	if err != nil {
		return fsm.Run{}, err
	}
	if !e.TaskPayload.Deadline.After(x.Now()) {
		return fsm.Run{}, fmt.Errorf("TASK deadline exceeded: %w", context.DeadlineExceeded)
	}
	r, exists := runs[e.Run]
	if exists && (r.Task != e.Task || r.Attempt != e.Attempt || r.TargetSHA != e.TargetSHA || r.Fence > e.Fence) {
		return fsm.Run{}, fmt.Errorf("checkpoint identity mismatch")
	}
	if exists && r.Fence == e.Fence && r.OwnerSession != e.To {
		return fsm.Run{}, fmt.Errorf("checkpoint owner mismatch")
	}
	if exists && r.Fence == e.Fence {
		if err := x.checkTask(r, e); err != nil {
			return fsm.Run{}, err
		}
	}
	if !exists {
		r = fsm.Run{Task: e.Task, RunID: e.Run, Attempt: e.Attempt, TargetSHA: e.TargetSHA, ProgressPct: e.TaskPayload.ProgressPct}
	}
	newAssignment := !exists || r.Fence != e.Fence
	if newAssignment {
		cmd := exec.CommandContext(ctx, "git", "-C", e.TaskPayload.Worktree, "rev-parse", "HEAD")
		b, err := cmd.Output()
		if err != nil {
			return fsm.Run{}, err
		}
		if strings.TrimSpace(string(b)) != e.TargetSHA {
			return fsm.Run{}, fmt.Errorf("base SHA mismatch")
		}
	}
	r.CoordinatorSession = e.From
	r.Fence = e.Fence
	r.OwnerSession = e.To
	r.Worktree = e.TaskPayload.Worktree
	r.Deadline = e.TaskPayload.Deadline
	r.PayloadRef = e.TaskPayload.PayloadRef
	if newAssignment {
		// Progress survives recovery, but messages and result belong to the old writer.
		r.Status = fsm.Accepted
		r.Outbox = nil
		r.Result = nil
		r.ResultMessageID = ""
		r.Blocker = ""
		r.ResumeStatus = ""
		r.RoleSelection = e.TaskPayload.AssignedRole
		r.TaskMessages = nil
	}
	if !hasID(r.TaskMessages, e.MessageID) {
		r.TaskMessages = append(r.TaskMessages, e.MessageID)
	}
	r.Seen = map[string]bool{}
	r.NextUpdateBy = x.Now().Add(x.UpdateInterval)
	ack := x.reply(e, envelope.Ack)
	ack.AckPayload = &envelope.AckPayload{State: "ACCEPTED", NextUpdateBy: r.NextUpdateBy}
	return r, x.deliver(ctx, r, ack)
}
func (x *Executor) reply(task envelope.Envelope, kind envelope.Kind) envelope.Envelope {
	e := task
	e.MessageID = envelope.NewID()
	e.InReplyTo = task.MessageID
	e.Timestamp = x.Now().UTC()
	e.Kind = kind
	e.From, e.To = task.To, task.From
	e.TaskPayload = nil
	return e
}

// Heartbeat persists progress before sending. It never declares completion on a send failure.
func (x *Executor) Heartbeat(ctx context.Context, task envelope.Envelope, state string, pct int, blocker string) error {
	if err := x.validate(); err != nil {
		return err
	}
	runs, err := x.Checkpoint.Load()
	if err != nil {
		return err
	}
	r, ok := runs[task.Run]
	if !ok || r.Fence != task.Fence || r.OwnerSession != x.Transport.WhoAmI() {
		return fmt.Errorf("stale executor")
	}
	if err := x.checkTask(r, task); err != nil {
		return err
	}
	if r.Terminal() {
		return fmt.Errorf("executor run already completed")
	}
	if pct < r.ProgressPct {
		return fmt.Errorf("progress cannot regress within a fence")
	}
	if !r.Deadline.After(x.Now()) {
		return fmt.Errorf("TASK deadline exceeded: %w", context.DeadlineExceeded)
	}
	e := x.reply(task, envelope.Heartbeat)
	e.HeartbeatPayload = &envelope.HeartbeatPayload{State: state, ProgressPct: pct, Blocker: blocker, NextUpdateBy: x.Now().Add(x.UpdateInterval)}
	if err = e.Validate(); err != nil {
		return err
	}
	if r.Status == fsm.ResultReady && state != string(fsm.ResultReady) {
		return fmt.Errorf("result readiness is irreversible")
	}
	r.Status = fsm.Status(state)
	r.ProgressPct = pct
	r.Blocker = blocker
	r.NextUpdateBy = e.HeartbeatPayload.NextUpdateBy
	return x.deliver(ctx, r, e)
}

func (x *Executor) checkTask(r fsm.Run, e envelope.Envelope) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if e.Kind != envelope.Task || e.To != x.Transport.WhoAmI() || e.To != r.OwnerSession || e.Task != r.Task || e.Run != r.RunID || e.Attempt != r.Attempt || e.TargetSHA != r.TargetSHA || e.Fence != r.Fence ||
		(r.CoordinatorSession != "" && e.From != r.CoordinatorSession) ||
		(r.Worktree != "" && e.TaskPayload.Worktree != r.Worktree) ||
		(!r.Deadline.IsZero() && !e.TaskPayload.Deadline.Equal(r.Deadline)) ||
		e.TaskPayload.PayloadRef != r.PayloadRef {
		return fmt.Errorf("checkpoint task identity mismatch")
	}
	return nil
}

func hasID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// deliver commits the state and message together, before the first transport call.
// The caller serializes executor operations and holds the writer lock.
func (x *Executor) deliver(ctx context.Context, r fsm.Run, e envelope.Envelope) error {
	runs, err := x.Checkpoint.Load()
	if err != nil {
		return fmt.Errorf("load delivery checkpoint: %w", err)
	}
	queued := false
	for _, pending := range r.Outbox {
		if pending.MessageID == e.MessageID {
			queued = true
			break
		}
	}
	if !queued {
		r.Outbox = append(r.Outbox, e)
	}
	runs[r.RunID] = r
	if err = x.Checkpoint.Save(runs); err != nil {
		return fmt.Errorf("persist delivery outbox: %w", err)
	}
	return x.flush(ctx, r.RunID)
}

// flush preserves FIFO order and keeps RESULT until a coordinator commit receipt.
func (x *Executor) flush(ctx context.Context, id string) error {
	runs, err := x.Checkpoint.Load()
	if err != nil {
		return fmt.Errorf("load replay checkpoint: %w", err)
	}
	r, ok := runs[id]
	if !ok || r.OwnerSession != x.Transport.WhoAmI() {
		return fmt.Errorf("replay owner mismatch")
	}
	pending := append([]envelope.Envelope(nil), r.Outbox...)
	for index, e := range pending {
		// Legacy checkpoints and recovery must never replay a previous writer's frames.
		if e.From != r.OwnerSession || e.Fence != r.Fence || e.Task != r.Task || e.Run != r.RunID || e.Attempt != r.Attempt || e.TargetSHA != r.TargetSHA {
			return fmt.Errorf("replay message identity mismatch")
		}
		// An expired lease cannot acknowledge current liveness. Renew as a new
		// protocol message, leaving transport retries of that message immutable.
		expired := (e.AckPayload != nil && e.AckPayload.State == "ACCEPTED" && !e.AckPayload.NextUpdateBy.After(x.Now())) ||
			(e.HeartbeatPayload != nil && !e.HeartbeatPayload.NextUpdateBy.After(x.Now()))
		if expired {
			oldID := e.MessageID
			e.MessageID = envelope.NewID()
			e.Timestamp = x.Now().UTC()
			next := x.Now().Add(x.UpdateInterval)
			if e.AckPayload != nil {
				cp := *e.AckPayload
				cp.NextUpdateBy = next
				e.AckPayload = &cp
			}
			if e.HeartbeatPayload != nil {
				cp := *e.HeartbeatPayload
				cp.NextUpdateBy = next
				e.HeartbeatPayload = &cp
			}
			for i := range r.Outbox {
				if r.Outbox[i].MessageID == oldID {
					r.Outbox[i] = e
				}
			}
			r.NextUpdateBy = next
			runs[id] = r
			if err = x.Checkpoint.Save(runs); err != nil {
				return fmt.Errorf("persist renewed lease: %w", err)
			}
		}
		if err = x.send(ctx, e); err != nil {
			// RESULT_READY cannot be revoked by a transport outage.
			if r.Status != fsm.ResultReady {
				r.Status = fsm.Blocked
			}
			r.Blocker = "TELEPATHY_UNAVAILABLE"
			runs[id] = r
			if saveErr := x.Checkpoint.Save(runs); saveErr != nil {
				return fmt.Errorf("persist blocked delivery: %w", saveErr)
			}
			return err
		}
		if e.Kind != envelope.Result {
			out := r.Outbox[:0]
			for _, queued := range r.Outbox {
				if queued.MessageID != e.MessageID {
					out = append(out, queued)
				}
			}
			r.Outbox = out
		}
		restored := index == len(pending)-1 && r.Blocker == "TELEPATHY_UNAVAILABLE"
		if restored {
			// Commit restored state with the final dequeue: a crash must not
			// leave BLOCKED behind after its last replayable message is removed.
			r.Blocker = ""
			for _, sent := range pending {
				if sent.AckPayload != nil && sent.AckPayload.State == "ACCEPTED" && r.Status != fsm.ResultReady {
					r.Status = fsm.Accepted
				}
				if sent.HeartbeatPayload != nil {
					if r.Status != fsm.ResultReady || sent.HeartbeatPayload.State == string(fsm.ResultReady) {
						r.Status = fsm.Status(sent.HeartbeatPayload.State)
						r.Blocker = sent.HeartbeatPayload.Blocker
					}
				}
			}
		}
		if e.Kind != envelope.Result || restored {
			runs[id] = r
			if err = x.Checkpoint.Save(runs); err != nil {
				return fmt.Errorf("persist delivered message and application state: %w", err)
			}
		}
	}
	return nil
}

func (x *Executor) send(ctx context.Context, e envelope.Envelope) error {
	var last error
	for n := 0; n <= x.Policy.MaxRetries; n++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n > 0 {
			timer := time.NewTimer(x.Policy.DelayFor(n - 1))
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		child, cancel := context.WithTimeout(ctx, x.SendTimeout)
		last = x.Transport.Send(child, e.To, e)
		cancel()
		if last == nil {
			return nil
		}
	}
	return fmt.Errorf("transport retries exhausted; checkpoint saved: %w", last)
}
