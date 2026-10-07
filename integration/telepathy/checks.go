//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd || illumos

package main

import (
	"context"
	"coord"
	"coord/checkpoint"
	"coord/envelope"
	"coord/fsm"
	"coord/internal/filelock"
	"coord/store"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func exists(dir, name string) bool { _, err := os.Stat(filepath.Join(dir, name)); return err == nil }
func waitMarker(ctx context.Context, dir, name string) error {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if exists(dir, name) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
func mark(dir, name string, v any) error { return save(filepath.Join(dir, name), v) }
func takeWriterLock(dir string) (func() error, error) {
	release, err := filelock.Lock(filepath.Join(dir, "writer.lock"))
	if err != nil {
		return nil, fmt.Errorf("writer lock unavailable: %w", err)
	}
	return release, nil
}
func nextTask(ctx context.Context, t *tp) (envelope.Envelope, error) {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		msgs, err := t.read(ctx)
		if err != nil {
			return envelope.Envelope{}, err
		}
		for _, e := range msgs {
			if e.Kind == envelope.Task {
				return e, nil
			}
		}
		select {
		case <-ctx.Done():
			return envelope.Envelope{}, ctx.Err()
		case <-tick.C:
		}
	}
}
func sendAckAndStarted(ctx context.Context, t *tp, task envelope.Envelope, delay time.Duration, pct int) error {
	ack := reply(task, envelope.Ack)
	ack.AckPayload = &envelope.AckPayload{State: "ACCEPTED", NextUpdateBy: time.Now().Add(delay)}
	if err := t.Send(ctx, ack.To, ack); err != nil {
		return err
	}
	hb := reply(task, envelope.Heartbeat)
	hb.HeartbeatPayload = &envelope.HeartbeatPayload{State: "STARTED", ProgressPct: pct, NextUpdateBy: time.Now().Add(delay)}
	return t.Send(ctx, hb.To, hb)
}
func sendCompletion(ctx context.Context, t *tp, task envelope.Envelope) error {
	ready := reply(task, envelope.Heartbeat)
	ready.HeartbeatPayload = &envelope.HeartbeatPayload{State: "RESULT_READY", ProgressPct: 100, NextUpdateBy: time.Now().Add(time.Minute)}
	if err := t.Send(ctx, ready.To, ready); err != nil {
		return err
	}
	result := reply(task, envelope.Result)
	result.ResultPayload = &envelope.ResultPayload{Outcome: "SUCCESS", Summary: "isolated integration scenario"}
	return t.Send(ctx, result.To, result)
}
func scenarioCore(t *tp, dir string) (*coord.Coordinator, *store.Store, error) {
	s, err := store.New(checkpoint.File{Path: filepath.Join(dir, "runs.json")})
	if err != nil {
		return nil, nil, err
	}
	cfg := coord.DefaultConfig()
	cfg.AckTimeout = 3 * time.Second
	cfg.TickInterval = 50 * time.Millisecond
	cfg.ProbeTimeout = time.Second
	cfg.Retry.MaxRetries = 0
	c, err := coord.New(t, s, cfg)
	if err != nil {
		return nil, nil, errors.Join(err, s.Close())
	}
	return c, s, nil
}
func snapshotFields(r fsm.Run) map[string]any {
	return map[string]any{"status": r.Status, "fence": r.Fence, "owner": r.OwnerSession, "attempt": r.Attempt, "progress": r.ProgressPct, "sha": r.TargetSHA, "task_messages": r.TaskMessages}
}

func p0Writer(ctx context.Context, t *tp, dir string, old bool) (err error) {
	label := "b"
	if old {
		label = "a"
	}
	if err := mark(dir, "p0-"+label+"-ready", t.self); err != nil {
		return err
	}
	task, err := nextTask(ctx, t)
	if err != nil {
		return err
	}
	release, err := takeWriterLock(dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()
	t.event("writer_lock_acquired", map[string]any{"fence": task.Fence, "owner": t.self})
	if err = mark(dir, "p0-"+label+"-task.json", task); err != nil {
		return err
	}
	duration := time.Minute
	if old {
		duration = time.Second
	}
	if err = sendAckAndStarted(ctx, t, task, duration, 42); err != nil {
		return err
	}
	if err = mark(dir, "p0-"+label+"-writing", map[string]any{"pid": os.Getpid(), "fence": task.Fence}); err != nil {
		return err
	}
	if old {
		<-ctx.Done()
		return ctx.Err()
	}
	if err = waitMarker(ctx, dir, "p0-abort-observed"); err != nil {
		return err
	}
	return sendCompletion(ctx, t, task)
}
func p0Late(ctx context.Context, t *tp, dir string) error {
	b, err := os.ReadFile(filepath.Join(dir, "p0-a-task.json"))
	if err != nil {
		return err
	}
	var task envelope.Envelope
	if err = json.Unmarshal(b, &task); err != nil {
		return err
	}
	hb := reply(task, envelope.Heartbeat)
	hb.HeartbeatPayload = &envelope.HeartbeatPayload{State: "STARTED", ProgressPct: 42, NextUpdateBy: time.Now().Add(time.Minute)}
	if err = t.Send(ctx, hb.To, hb); err != nil {
		return err
	}
	t.event("stale_heartbeat_sent", map[string]any{"fence": hb.Fence})
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		msgs, err := t.read(ctx)
		if err != nil {
			return err
		}
		for _, e := range msgs {
			if e.Kind == envelope.Abort {
				if e.From != task.From || e.Fence != 2 || e.InReplyTo != hb.MessageID || e.AbortPayload.Reason != "stale fence" {
					return fmt.Errorf("unexpected ABORT")
				}
				t.event("abort_stopped_old_session", nil)
				return mark(dir, "p0-abort-observed", map[string]any{"fence": e.Fence, "old_fence": hb.Fence, "reason": e.AbortPayload.Reason})
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
func p0Coord(ctx context.Context, t *tp, dir, to, newOwner, sha string) (err error) {
	c, s, err := scenarioCore(t, dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	r := fsm.Run{Task: "P0-FENCE", RunID: "P0-FENCE-run-1", Attempt: 3, Fence: 1, TargetSHA: sha, OwnerSession: to, Worktree: dir, Deadline: time.Now().Add(time.Minute)}
	if err = c.Assign(ctx, r); err != nil {
		return err
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(child) }()
	claimed, transferred := false, false
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			current, _ := s.Get(r.RunID)
			if current.RecoveryNeeded && !claimed {
				if err = c.ClaimRecovery(r.RunID, 1, "test-supervisor"); err != nil {
					return err
				}
				claimed = true
				t.event("recovery_claimed", snapshotFields(current))
				if err = mark(dir, "p0-await-fence", snapshotFields(current)); err != nil {
					return err
				}
			}
			if claimed && !transferred && exists(dir, "p0-fenced.json") {
				if err = c.RecoverClaimed(ctx, r.RunID, newOwner, 1, "test-supervisor"); err != nil {
					return err
				}
				transferred = true
				current, _ = s.Get(r.RunID)
				t.event("ownership_transferred", snapshotFields(current))
				if err = mark(dir, "p0-transferred", snapshotFields(current)); err != nil {
					return err
				}
			}
			if current.Status == fsm.Done {
				cancel()
				<-done
				w := httptest.NewRecorder()
				c.Metrics.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
				if !strings.Contains(w.Body.String(), "coord_stale_fence_rejections_total 1\n") {
					return fmt.Errorf("stale fence metric missing")
				}
				if err = os.WriteFile(filepath.Join(dir, "metrics.txt"), w.Body.Bytes(), 0600); err != nil {
					return err
				}
				return mark(dir, "p0-done", snapshotFields(current))
			}
		}
	}
}

func p1Agent(ctx context.Context, t *tp, dir string) error {
	if err := mark(dir, "p1-agent-ready", t.self); err != nil {
		return err
	}
	task, err := nextTask(ctx, t)
	if err != nil {
		return err
	}
	if err = mark(dir, "p1-agent-checkpoint.json", map[string]any{"task": task, "progress": 42}); err != nil {
		return err
	}
	if err = sendAckAndStarted(ctx, t, task, 20*time.Second, 42); err != nil {
		return err
	}
	if err = waitMarker(ctx, dir, "p1-send-during-outage"); err != nil {
		return err
	}
	hb := reply(task, envelope.Heartbeat)
	hb.HeartbeatPayload = &envelope.HeartbeatPayload{State: "STARTED", ProgressPct: 43, NextUpdateBy: time.Now().Add(time.Minute)}
	if err = t.Send(ctx, hb.To, hb); err != nil {
		return err
	}
	if err = mark(dir, "p1-outage-heartbeat-sent", hb); err != nil {
		return err
	}
	if err = waitMarker(ctx, dir, "p1-continue"); err != nil {
		return err
	}
	return sendCompletion(ctx, t, task)
}
func p1Coord(ctx context.Context, t *tp, dir, to, sha string) (err error) {
	c, s, err := scenarioCore(t, dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	const id = "P1-RESTART-run-1"
	r, loaded := s.Get(id)
	if !loaded {
		r = fsm.Run{Task: "P1-RESTART", RunID: id, Attempt: 3, Fence: 7, TargetSHA: sha, OwnerSession: to, Worktree: dir, Deadline: time.Now().Add(time.Minute)}
		if err = c.Assign(ctx, r); err != nil {
			return err
		}
	} else {
		t.event("snapshot_loaded", snapshotFields(r))
		if err = mark(dir, "p1-restored", snapshotFields(r)); err != nil {
			return err
		}
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(child) }()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			r, _ = s.Get(id)
			if r.Status == fsm.Started && r.ProgressPct == 42 && !loaded && !exists(dir, "p1-state-ready") {
				if err = mark(dir, "p1-state-ready", snapshotFields(r)); err != nil {
					return err
				}
			}
			if loaded && r.Status == fsm.Started && r.ProgressPct == 43 && !exists(dir, "p1-heartbeat-resumed") {
				if err = mark(dir, "p1-heartbeat-resumed", snapshotFields(r)); err != nil {
					return err
				}
			}
			if r.Status == fsm.Done {
				cancel()
				<-done
				return mark(dir, "p1-done", snapshotFields(r))
			}
		}
	}
}
