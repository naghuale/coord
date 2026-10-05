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
	if x.Transport == nil || x.Now == nil || x.UpdateInterval <= 0 || x.SendTimeout <= 0 {
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
	cmd := exec.CommandContext(ctx, "git", "-C", e.TaskPayload.Worktree, "rev-parse", "HEAD")
	b, err := cmd.Output()
	if err != nil {
		return fsm.Run{}, err
	}
	if strings.TrimSpace(string(b)) != e.TargetSHA {
		return fsm.Run{}, fmt.Errorf("base SHA mismatch")
	}
	runs, err := x.Checkpoint.Load()
	if err != nil {
		return fsm.Run{}, err
	}
	r, exists := runs[e.Run]
	if exists && (r.Task != e.Task || r.Attempt != e.Attempt || r.TargetSHA != e.TargetSHA || r.Fence > e.Fence) {
		return fsm.Run{}, fmt.Errorf("checkpoint identity mismatch")
	}
	if exists && r.Fence == e.Fence && r.OwnerSession != e.To {
		return fsm.Run{}, fmt.Errorf("checkpoint owner mismatch")
	}
	if !exists {
		r = fsm.Run{Task: e.Task, RunID: e.Run, Attempt: e.Attempt, TargetSHA: e.TargetSHA, ProgressPct: e.TaskPayload.ProgressPct}
	}
	newAssignment := !exists || r.Fence != e.Fence
	r.Fence = e.Fence
	r.OwnerSession = e.To
	r.Worktree = e.TaskPayload.Worktree
	r.Deadline = e.TaskPayload.Deadline
	r.PayloadRef = e.TaskPayload.PayloadRef
	if newAssignment {
		r.Status = fsm.Accepted
	}
	r.TaskMessages = []string{e.MessageID}
	r.Seen = map[string]bool{}
	r.NextUpdateBy = x.Now().Add(x.UpdateInterval)
	runs[e.Run] = r
	if err = x.Checkpoint.Save(runs); err != nil {
		return r, err
	}
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
	e := x.reply(task, envelope.Heartbeat)
	e.HeartbeatPayload = &envelope.HeartbeatPayload{State: state, ProgressPct: pct, Blocker: blocker, NextUpdateBy: x.Now().Add(x.UpdateInterval)}
	if err = e.Validate(); err != nil {
		return err
	}
	r.Status = fsm.Status(state)
	r.ProgressPct = pct
	r.Blocker = blocker
	r.NextUpdateBy = e.HeartbeatPayload.NextUpdateBy
	runs[r.RunID] = r
	if err = x.Checkpoint.Save(runs); err != nil {
		return err
	}
	return x.deliver(ctx, r, e)
}
func (x *Executor) deliver(ctx context.Context, r fsm.Run, e envelope.Envelope) error {
	var last error
	for n := 0; n <= x.Policy.MaxRetries; n++ {
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
	runs, err := x.Checkpoint.Load()
	if err != nil {
		return err
	}
	r.Status = fsm.Blocked
	r.Blocker = "TELEPATHY_UNAVAILABLE"
	r.Outbox = []envelope.Envelope{e}
	runs[r.RunID] = r
	if err = x.Checkpoint.Save(runs); err != nil {
		return err
	}
	return fmt.Errorf("transport retries exhausted; checkpoint saved: %w", last)
}
