// Package coord implements a durable, transport-independent coordinator.
package coord

import (
	"context"
	"coord/envelope"
	"coord/fsm"
	"coord/retry"
	"coord/roles"
	"coord/store"
	"coord/task"
	"coord/transport"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

var ErrTransportClosed = errors.New("transport closed")
var ErrStale = errors.New("noncanonical identity")

// ErrDeadlineExceeded rejects application events after the absolute run deadline.
var ErrDeadlineExceeded = errors.New("task deadline exceeded")

// ErrStaleFence also matches ErrStale for callers rejecting any stale identity.
var ErrStaleFence = fmt.Errorf("%w: stale fence", ErrStale)

// ErrFenceExhausted prevents a generation counter from wrapping to an old value.
var ErrFenceExhausted = errors.New("fence generation exhausted")

type Config struct {
	AckTimeout, ProbeTimeout, SendTimeout, TickInterval time.Duration
	Retry                                               retry.Policy
	Now                                                 func() time.Time
}

func DefaultConfig() Config {
	return Config{30 * time.Second, 5 * time.Second, 5 * time.Second, time.Second, retry.Default(), time.Now}
}

// WorkflowRuntime is implemented by deterministic canonical orchestration.
// It shares this daemon owner; it is not a second coordinator loop.
type WorkflowRuntime interface {
	Recover(context.Context) error
	Tick(context.Context) error
	Completion() error
	OwnsRun(string) bool
}

type Coordinator struct {
	workflowRecovered  bool
	CandidateProcessor *CoordinatorCandidateProcessor

	Workflow     WorkflowRuntime
	mu           sync.Mutex
	tp           transport.Transport
	store        store.RunStore
	cfg          Config
	Metrics      *Metrics
	RoleRegistry *roles.Registry
}

// Close releases coordinator-owned durable stores after Run and HTTP handlers
// have stopped. Repeated calls are safe because the underlying stores are
// idempotent.
func (c *Coordinator) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []error
	if c.RoleRegistry != nil {
		if err := c.RoleRegistry.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close role registry: %w", err))
		}
	}
	if c.store != nil {
		if err := c.store.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close run store: %w", err))
		}
	}
	return errors.Join(errs...)
}

// RecoveryPending reports whether a run still requires supervisor recovery.
func (c *Coordinator) RecoveryPending() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, run := range c.store.All() {
		if run.RecoveryNeeded {
			return true
		}
	}
	return false
}

func New(tp transport.Transport, s store.RunStore, cfg Config) (*Coordinator, error) {
	if tp == nil || s == nil || tp.WhoAmI() == "" || cfg.Now == nil || cfg.AckTimeout <= 0 || cfg.ProbeTimeout <= 0 || cfg.SendTimeout <= 0 || cfg.TickInterval <= 0 {
		return nil, fmt.Errorf("invalid coordinator configuration")
	}
	if err := cfg.Retry.Validate(); err != nil {
		return nil, err
	}
	c := &Coordinator{tp: tp, store: s, cfg: cfg, Metrics: NewMetrics()}
	for id, r := range s.All() {
		if id != r.RunID {
			return nil, fmt.Errorf("invalid canonical snapshot run %q", id)
		}
		if err := r.Validate(); err != nil {
			return nil, fmt.Errorf("invalid canonical snapshot run %q: %w", id, err)
		}
		e := c.base(envelope.Result, r)
		e.From = r.OwnerSession
		e.To = tp.WhoAmI()
		e.InReplyTo = "snapshot"
		e.ResultPayload = &envelope.ResultPayload{Outcome: "SUCCESS"}
		if err := e.Validate(); err != nil {
			return nil, fmt.Errorf("invalid canonical snapshot run %q: %w", id, err)
		}
	}
	return c, nil
}
func (c *Coordinator) base(k envelope.Kind, r fsm.Run) envelope.Envelope {
	return envelope.Envelope{SchemaVersion: 1, MessageID: envelope.NewID(), Timestamp: c.cfg.Now().UTC(), From: c.tp.WhoAmI(), To: r.OwnerSession, Kind: k, Task: r.Task, Run: r.RunID, Attempt: r.Attempt, TargetSHA: r.TargetSHA, Fence: r.Fence}
}
func (c *Coordinator) task(r fsm.Run) envelope.Envelope {
	e := c.base(envelope.Task, r)
	e.TaskPayload = &envelope.TaskPayload{Worktree: r.Worktree, PayloadRef: r.PayloadRef, Deadline: r.Deadline, ProgressPct: r.ProgressPct, AssignedRole: r.RoleSelection}
	return e
}

// Assign durably records ownership and the outbox before attempting delivery.
func (c *Coordinator) Assign(ctx context.Context, r fsm.Run) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.cfg.Now()
	r.Status = fsm.Pending
	r.AssignedAt = now
	r.LastUpdate = now
	r.NextUpdateBy = now.Add(c.cfg.AckTimeout)
	r.Seen = map[string]bool{}
	r.Retries = 0
	r.RecoveryNeeded = false
	r.RecoveryClaim = ""
	r.DeliveredAt = time.Time{}
	r.Result = nil
	r.LastHeartbeatAt = time.Time{}
	if err := c.selectRole(&r); err != nil {
		return err
	}
	r.ResumeStatus = ""
	r.ProbeID = ""
	r.ProbeNonce = ""
	r.RetryAt = time.Time{}
	r.SendRetryAt = time.Time{}
	r.SendRetries = 0
	e := c.task(r)
	if err := e.Validate(); err != nil {
		return err
	}
	r.TaskMessages = []string{e.MessageID}
	r.Outbox = []envelope.Envelope{e}
	if err := c.store.Update(func(runs map[string]fsm.Run) error {
		if _, ok := runs[r.RunID]; ok {
			return fmt.Errorf("run already exists")
		}
		runs[r.RunID] = r
		return nil
	}); err != nil {
		return err
	}
	return c.flush(ctx, r.RunID)
}
func (c *Coordinator) send(ctx context.Context, e envelope.Envelope) error {
	child, cancel := context.WithTimeout(ctx, c.cfg.SendTimeout)
	defer cancel()
	started := time.Now()
	err := c.tp.Send(child, e.To, e)
	if err == nil {
		c.Metrics.Inc(fmt.Sprintf("outbound_total{kind=%q}", e.Kind))
		c.Metrics.DeliveryLatency(time.Since(started).Seconds())
	}
	return err
}
func (c *Coordinator) flush(ctx context.Context, id string) error {
	r, ok := c.store.Get(id)
	if !ok {
		return nil
	}
	if c.cfg.Now().Before(r.SendRetryAt) {
		return nil
	}
	for _, e := range r.Outbox {
		if err := c.send(ctx, e); err != nil {
			c.Metrics.Inc("send_errors_total")
			return c.store.Update(func(runs map[string]fsm.Run) error {
				x := runs[id]
				x.SendRetryAt = c.cfg.Now().Add(c.cfg.Retry.DelayFor(x.SendRetries))
				x.SendRetries++
				runs[id] = x
				return nil
			})
		}
		if err := c.store.Update(func(runs map[string]fsm.Run) error {
			x := runs[id]
			out := x.Outbox[:0]
			for _, p := range x.Outbox {
				if p.MessageID != e.MessageID {
					out = append(out, p)
				}
			}
			x.Outbox = out
			x.SendRetryAt = time.Time{}
			x.SendRetries = 0
			if e.Kind == envelope.Task && x.DeliveredAt.IsZero() {
				x.DeliveredAt = c.cfg.Now()
			}
			runs[id] = x
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
func contains(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// Handle checks identity, correlation, deduplication and FSM before any update.
func (c *Coordinator) Handle(ctx context.Context, e envelope.Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := e.Validate(); err != nil {
		c.Metrics.Inc("malformed_messages_total")
		return err
	}
	if c.Workflow != nil && e.Run != "" && c.Workflow.OwnsRun(e.Run) {
		return fmt.Errorf("orchestrated Run requires canonical command boundary")
	}
	if e.To != c.tp.WhoAmI() {
		return fmt.Errorf("wrong recipient")
	}
	c.Metrics.Inc(fmt.Sprintf("inbound_total{kind=%q}", e.Kind))
	if e.Kind == envelope.RoleUpdate {
		if c.RoleRegistry == nil {
			return fmt.Errorf("role registry unavailable")
		}
		if err := c.RoleRegistry.Apply(e.From, e.MessageID, *e.RoleUpdatePayload, c.cfg.Now()); err != nil {
			return err
		}
		c.Metrics.Inc("role_updates_total")
		a, _ := c.RoleRegistry.Agent(e.From)
		ack := envelope.Envelope{SchemaVersion: 1, MessageID: envelope.NewID(), Timestamp: c.cfg.Now().UTC(), From: c.tp.WhoAmI(), To: e.From, Kind: envelope.RoleAck, InReplyTo: e.MessageID, RoleAckPayload: &envelope.RoleAckPayload{State: "ACCEPTED", Revision: a.Revision}}
		return c.send(ctx, ack)
	}
	if e.Kind == envelope.Ping {
		p := envelope.Envelope{SchemaVersion: 1, MessageID: envelope.NewID(), Timestamp: c.cfg.Now().UTC(), From: c.tp.WhoAmI(), To: e.From, Kind: envelope.Pong, InReplyTo: e.MessageID, PongPayload: &envelope.PongPayload{Nonce: e.PingPayload.Nonce, State: "BUSY"}}
		return c.send(ctx, p)
	}
	r, ok := c.store.Get(e.Run)
	if !ok {
		return fmt.Errorf("unknown run")
	}
	// Fence is not secret. Both older and purported future generations are invalid,
	// regardless of the sender or whether its message_id has been seen before.
	if e.Fence < r.Fence {
		c.Metrics.Inc("stale_fence_rejections_total")
		c.rejectIdentity(ctx, r, e, "stale fence")
		return ErrStaleFence
	}
	if e.Fence > r.Fence {
		c.Metrics.Inc("invalid_fence_rejections_total")
		c.rejectIdentity(ctx, r, e, "future fence")
		return ErrStale
	}
	if r.OwnerSession == "" || r.OwnerSession != e.From || r.Attempt != e.Attempt || r.TargetSHA != e.TargetSHA || r.Task != e.Task {
		c.Metrics.Inc("split_brain_events_total")
		c.rejectIdentity(ctx, r, e, "not canonical owner/attempt/task/SHA")
		return ErrStale
	}
	now := c.cfg.Now()
	if !r.Terminal() && !now.Before(r.Deadline) {
		c.failDeadline(&r, now)
		if err := c.put(r); err != nil {
			return err
		}
		return ErrDeadlineExceeded
	}
	if r.Seen == nil {
		r.Seen = map[string]bool{}
	}
	if r.Seen[e.MessageID] {
		c.Metrics.Inc("duplicate_messages_total")
		if e.Kind == envelope.Result && r.Terminal() && r.Result != nil {
			return c.repeatResultReceipt(ctx, r, e)
		}
		return nil
	}
	if r.RecoveryClaim != "" {
		return fmt.Errorf("run is fenced for recovery")
	}
	if e.Kind == envelope.Pong {
		if r.ProbeID == "" || e.InReplyTo != r.ProbeID || e.PongPayload.Nonce != r.ProbeNonce {
			return fmt.Errorf("uncorrelated PONG")
		}
		// Liveness cannot substitute for an application heartbeat.
		return c.store.Update(func(runs map[string]fsm.Run) error {
			x := runs[r.RunID]
			if x.Seen == nil {
				x.Seen = map[string]bool{}
			}
			x.Seen[e.MessageID] = true
			x.ProbeID = ""
			x.ProbeNonce = ""
			runs[x.RunID] = x
			return nil
		})
	}
	if !contains(r.TaskMessages, e.InReplyTo) {
		return fmt.Errorf("uncorrelated task response")
	}
	if e.Kind == envelope.Ack && e.AckPayload.State == "ACCEPTED" && r.Status != fsm.Pending && (r.Status != fsm.Stalled || r.ResumeStatus != fsm.Pending) {
		// A retried TASK may produce a fresh ACK after work has already started.
		// Acceptance cannot renew the progress lease or rewind application state.
		r.Seen[e.MessageID] = true
		return c.put(r)
	}
	if (e.Kind == envelope.Ack && e.AckPayload.State == "ACCEPTED" && !e.AckPayload.NextUpdateBy.After(now)) || (e.Kind == envelope.Heartbeat && !e.HeartbeatPayload.NextUpdateBy.After(now)) {
		return fmt.Errorf("expired next_update_by")
	}
	// R117/R124: canonical Executor RESULT path. All task-scoped Runs route through
	// RecordExecutorResult; the legacy Apply path is closed for them. Fail-closed:
	// if the store is not a CanonicalStore, the command is rejected with no fallback.
	if r.TaskID != "" && e.Kind == envelope.Result {
		canonical, ok := c.store.(*store.CanonicalStore)
		if !ok {
			return fmt.Errorf("canonical Executor RESULT path requires CanonicalStore: fail-closed")
		}
		if e.ResultPayload == nil {
			return fmt.Errorf("RESULT envelope missing payload")
		}
		rev, exists := canonical.TaskRevision(r.TaskID)
		if !exists {
			return fmt.Errorf("task %q not found in canonical state", r.TaskID)
		}
		executorResult := task.ExecutorResult{
			Outcome:            task.ExecutorOutcome(e.ResultPayload.Outcome),
			Summary:            e.ResultPayload.Summary,
			OutputArtifactRefs: []string{},
		}
		if _, _, err := canonical.RecordExecutorResult(e.MessageID, r.TaskID, r.RunID, rev, executorResult, now); err != nil {
			return err
		}
		return c.flush(ctx, r.RunID)
	}
	if err := r.Apply(e, now); err != nil {
		c.Metrics.Inc("fsm_errors_total")
		return err
	}
	r.Seen[e.MessageID] = true
	r.Outbox = nil
	r.SendRetryAt = time.Time{}
	r.SendRetries = 0
	if e.Kind == envelope.Result {
		r.Outbox = []envelope.Envelope{c.resultReceipt(r, e.MessageID)}
	}
	if err := c.store.Update(func(runs map[string]fsm.Run) error { runs[r.RunID] = r; return nil }); err != nil {
		return err
	}
	if e.Kind == envelope.Ack {
		if !r.DeliveredAt.IsZero() {
			c.Metrics.AckLatency(now.Sub(r.DeliveredAt).Seconds())
		}
	}
	if e.Kind == envelope.Heartbeat {
		c.Metrics.Inc("heartbeat_received_total")
	}
	if e.Kind == envelope.Result {
		return c.flush(ctx, r.RunID)
	}
	return nil
}

func (c *Coordinator) resultReceipt(r fsm.Run, resultID string) envelope.Envelope {
	e := c.base(envelope.ResultAck, r)
	e.InReplyTo = resultID
	e.ResultAckPayload = &envelope.ResultAckPayload{Outcome: r.Result.Outcome}
	return e
}

func (c *Coordinator) repeatResultReceipt(ctx context.Context, r fsm.Run, result envelope.Envelope) error {
	if result.ResultPayload.Outcome != r.Result.Outcome {
		return fmt.Errorf("duplicate result outcome mismatch")
	}
	for _, queued := range r.Outbox {
		if queued.Kind == envelope.ResultAck && queued.InReplyTo == result.MessageID {
			return c.flush(ctx, r.RunID)
		}
	}
	if len(r.Outbox) == 0 {
		r.SendRetryAt = time.Time{}
		r.SendRetries = 0
	}
	r.Outbox = append(r.Outbox, c.resultReceipt(r, result.MessageID))
	if err := c.put(r); err != nil {
		return err
	}
	return c.flush(ctx, r.RunID)
}

// An ABORT is a cooperative request; the trusted supervisor still fences writes.
func (c *Coordinator) rejectIdentity(ctx context.Context, r fsm.Run, e envelope.Envelope, reason string) {
	a := c.base(envelope.Abort, r)
	a.To = e.From
	a.InReplyTo = e.MessageID
	a.AbortPayload = &envelope.AbortPayload{Reason: reason}
	_ = c.send(ctx, a)
}

// Tick performs bounded retries. Transport retries preserve the attempt and owner.
func (c *Coordinator) Tick(ctx context.Context) error {
	if c.Workflow != nil {
		if err := c.Workflow.Tick(ctx); err != nil {
			return err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.cfg.Now()
	for id, r := range c.store.All() {
		if c.Workflow != nil && c.Workflow.OwnsRun(id) {
			continue
		}
		if r.Terminal() {
			if err := c.flush(ctx, id); err != nil {
				return err
			}
			continue
		}
		if !now.Before(r.Deadline) {
			c.failDeadline(&r, now)
			if err := c.put(r); err != nil {
				return err
			}
			continue
		}
		if r.RecoveryNeeded {
			continue
		}
		if r.Status != fsm.Stalled && !now.Before(r.NextUpdateBy) {
			if err := r.Timeout(now); err != nil {
				return err
			}
			r.RetryAt = now
			c.Metrics.Inc("stall_total")
			if r.ResumeStatus == fsm.Pending {
				c.Metrics.Inc("ack_timeouts_total")
			} else {
				c.Metrics.Inc("heartbeat_miss_total")
			}
			if err := c.put(r); err != nil {
				return err
			}
		}
		if r.Status == fsm.Stalled && !now.Before(r.RetryAt) {
			if r.Retries >= c.cfg.Retry.MaxRetries {
				r.RecoveryNeeded = true
				r.Outbox = nil
				if err := c.put(r); err != nil {
					return err
				}
				continue
			}
			var e envelope.Envelope
			if r.ResumeStatus == fsm.Pending {
				e = c.task(r)
				r.TaskMessages = append(r.TaskMessages, e.MessageID)
			} else {
				e = c.base(envelope.Ping, r)
				e.PingPayload = &envelope.PingPayload{Nonce: envelope.NewID()}
				r.ProbeID = e.MessageID
				r.ProbeNonce = e.PingPayload.Nonce
			}
			r.Outbox = []envelope.Envelope{e}
			r.SendRetryAt = time.Time{}
			r.SendRetries = 0
			r.RetryAt = now.Add(c.cfg.Retry.DelayFor(r.Retries))
			if r.RetryAt.Before(now.Add(c.cfg.ProbeTimeout)) {
				r.RetryAt = now.Add(c.cfg.ProbeTimeout)
			}
			r.Retries++
			if err := c.put(r); err != nil {
				return err
			}
		}
		if err := c.flush(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
func (c *Coordinator) put(r fsm.Run) error {
	return c.store.Update(func(runs map[string]fsm.Run) error { runs[r.RunID] = r; return nil })
}

// failDeadline makes a run terminal and clears every piece of recovery state.
// A claim held by a supervisor that never transferred ownership must not survive
// here: the run can no longer be claimed or recovered, so the snapshot would
// otherwise advertise an active recovery for a dead task.
func (c *Coordinator) failDeadline(r *fsm.Run, now time.Time) {
	r.Status = fsm.Failed
	r.Blocker = "task deadline exceeded"
	r.Outbox = nil
	r.SendRetryAt = time.Time{}
	r.SendRetries = 0
	r.RetryAt = time.Time{}
	r.RecoveryNeeded = false
	r.RecoveryClaim = ""
	// The run is terminal: there is nothing left to resume, and a leftover
	// ResumeStatus would advertise a continuation point that Handle refuses.
	r.ResumeStatus = ""
	r.LastUpdate = now
}
func (c *Coordinator) recover(ctx context.Context, id, owner string, expectedFence uint64, claim string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.store.Get(id)
	if !ok || !r.RecoveryNeeded || r.Status != fsm.Stalled || r.Fence != expectedFence || claim == "" || r.RecoveryClaim != claim || owner == "" || owner == r.OwnerSession {
		return fmt.Errorf("invalid recovery")
	}
	if r.Fence == math.MaxUint64 {
		return ErrFenceExhausted
	}
	r.OwnerSession = owner
	if err := c.selectRole(&r); err != nil {
		return err
	}
	r.Fence++
	r.LastHeartbeatAt = time.Time{}
	r.Blocker = ""
	// recover не проходит через Apply: он пересобирает Run напрямую и не
	// выходит из STALLED через FSM, поэтому здесь ResumeStatus снимается
	// отдельно. Это единственное место вне Apply.
	r.ResumeStatus = ""
	r.LastUpdate = c.cfg.Now()
	r.RecoveryClaim = ""
	r.Status = fsm.Pending
	r.NextUpdateBy = c.cfg.Now().Add(c.cfg.AckTimeout)
	r.AssignedAt = c.cfg.Now()
	r.DeliveredAt = time.Time{}
	r.SendRetryAt = time.Time{}
	r.SendRetries = 0
	r.Retries = 0
	r.RecoveryNeeded = false
	r.Seen = map[string]bool{}
	r.ProbeID = ""
	r.ProbeNonce = ""
	e := c.task(r)
	if err := e.Validate(); err != nil {
		return err
	}
	r.TaskMessages = []string{e.MessageID}
	r.Outbox = []envelope.Envelope{e}
	if err := c.put(r); err != nil {
		return err
	}
	c.Metrics.Inc("watchdog_restarts_total")
	return c.flush(ctx, id)
}
func (c *Coordinator) Run(ctx context.Context) error {
	return c.RunWithReadiness(ctx, nil)
}

// RunWithReadiness initializes the transport receive stream, invokes onReady,
// and then runs the coordinator loop. onReady is called exactly once, after
// Receive has been acquired and before any incoming message is handled.
func (c *Coordinator) RecoverWorkflow(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.workflowRecovered || c.Workflow == nil {
		return nil
	}
	if err := c.Workflow.Recover(ctx); err != nil {
		return err
	}
	c.workflowRecovered = true
	return nil
}

func (c *Coordinator) RunWithReadiness(ctx context.Context, onReady func()) error {
	if err := c.RecoverWorkflow(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	inbox := c.tp.Receive(ctx)
	if inbox == nil {
		return fmt.Errorf("transport receive returned nil channel")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if onReady != nil {
		onReady()
	}
	if err := c.processCoordinatorCandidate(ctx); err != nil {
		return err
	}
	tick := time.NewTicker(c.cfg.TickInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e, ok := <-inbox:
			if !ok {
				return ErrTransportClosed
			}
			if err := c.Handle(ctx, e); err != nil {
				if errors.Is(err, store.ErrPersistence) || errors.Is(err, roles.ErrPersistence) {
					return err
				}
				c.Metrics.Inc("rejected_messages_total")
			}
			if err := c.processCoordinatorCandidate(ctx); err != nil {
				return err
			}
		case <-tick.C:
			if err := c.processCoordinatorCandidate(ctx); err != nil {
				return err
			}
			if err := c.Tick(ctx); err != nil {
				return err
			}
		}
	}
}
