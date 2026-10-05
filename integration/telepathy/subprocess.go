//go:build unix

package main

import (
	"context"
	"coord/envelope"
	"coord/fsm"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func p2Agent(ctx context.Context, t *tp, dir, worker string) error {
	if err := mark(dir, "p2-agent-ready", t.self); err != nil {
		return err
	}
	task, err := nextTask(ctx, t)
	if err != nil {
		return err
	}
	input := map[string]any{"task": task.Task, "run": task.Run, "attempt": task.Attempt, "target_sha": task.TargetSHA, "fence": task.Fence, "progress_pct": 0, "resume_mode": "RESUME"}
	path := filepath.Join(dir, "worker-input.json")
	if err = save(path, input); err != nil {
		return err
	}
	if err = mark(dir, "bridge-checkpoint.json", map[string]any{"task": task, "progress_pct": 0}); err != nil {
		return err
	}
	if err = sendAckAndStarted(ctx, t, task, time.Minute, 0); err != nil {
		return err
	}
	first := exec.CommandContext(ctx, "python3", worker, "--input", path, "--pause-after", "2")
	first.Stdout = os.Stdout
	first.Stderr = os.Stderr
	if err = first.Start(); err != nil {
		return err
	}
	waited := make(chan error, 1)
	go func() { waited <- first.Wait() }()
	if err = waitMarker(ctx, dir, "p2-worker-halfway"); err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(dir, "worker-checkpoint.json"))
	if err != nil {
		return err
	}
	var progress struct {
		Progress int `json:"progress_pct"`
		Units    int `json:"committed_units"`
	}
	if err = json.Unmarshal(b, &progress); err != nil {
		return err
	}
	if progress.Progress != 50 || progress.Units != 2 {
		return fmt.Errorf("subprocess did not checkpoint 50%%")
	}
	hb := reply(task, envelope.Heartbeat)
	hb.HeartbeatPayload = &envelope.HeartbeatPayload{State: "STARTED", ProgressPct: 50, NextUpdateBy: time.Now().Add(time.Minute)}
	if err = t.Send(ctx, hb.To, hb); err != nil {
		return err
	}
	if err = mark(dir, "p2-ready-to-kill", map[string]any{"pid": first.Process.Pid, "progress": 50}); err != nil {
		return err
	}
	select {
	case err = <-waited:
	case <-ctx.Done():
		return ctx.Err()
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return fmt.Errorf("expected subprocess SIGKILL, got %v", err)
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		return fmt.Errorf("subprocess was not killed with SIGKILL")
	}
	// Reload from disk after the crash rather than trusting the pre-crash sample.
	b, err = os.ReadFile(filepath.Join(dir, "worker-checkpoint.json"))
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, &progress); err != nil {
		return err
	}
	if progress.Progress != 50 || progress.Units != 2 {
		return fmt.Errorf("post-crash checkpoint mismatch")
	}
	t.event("subprocess_checkpoint_loaded", map[string]any{"progress": progress.Progress, "units": progress.Units})
	t.event("subprocess_killed", map[string]any{"pid": first.Process.Pid, "progress": 50})
	if err = mark(dir, "bridge-checkpoint.json", map[string]any{"task": task, "progress_pct": 50, "resume_mode": "RESUME"}); err != nil {
		return err
	}
	input["progress_pct"] = progress.Progress
	if err = save(path, input); err != nil {
		return err
	}
	second := exec.CommandContext(ctx, "python3", worker, "--input", path)
	second.Stdout = os.Stdout
	second.Stderr = os.Stderr
	if err = second.Run(); err != nil {
		return err
	}
	t.event("subprocess_resumed", map[string]any{"pid": second.Process.Pid, "progress": 100})
	if err = mark(dir, "p2-agent-completed", map[string]any{"killed_pid": first.Process.Pid, "replacement_pid": second.Process.Pid}); err != nil {
		return err
	}
	return sendCompletion(ctx, t, task)
}
func p2Coord(ctx context.Context, t *tp, dir, to, sha string) error {
	c, s, err := scenarioCore(t, dir)
	if err != nil {
		return err
	}
	defer s.Close()
	r := fsm.Run{Task: "P2-SUBPROCESS", RunID: "P2-SUBPROCESS-run-1", Attempt: 3, Fence: 1, TargetSHA: sha, OwnerSession: to, Worktree: dir, Deadline: time.Now().Add(time.Minute)}
	if err = c.Assign(ctx, r); err != nil {
		return err
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(child) }()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			r, _ = s.Get(r.RunID)
			if r.ProgressPct == 50 && !exists(dir, "p2-progress-accepted") {
				if err = mark(dir, "p2-progress-accepted", snapshotFields(r)); err != nil {
					return err
				}
			}
			if r.Status == fsm.Done {
				cancel()
				<-done
				return mark(dir, "p2-done", snapshotFields(r))
			}
		}
	}
}
