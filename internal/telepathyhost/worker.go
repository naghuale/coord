//go:build unix

package telepathyhost

import (
	"bytes"
	"context"
	"coord/checkpoint"
	"coord/envelope"
	"coord/executor"
	"coord/fsm"
	"coord/internal/atomicfile"
	"coord/retry"
	"coord/transport"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

func readBinding(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}
func bound(c Config, e envelope.Envelope) bool {
	r := c.Task
	if e.From != c.Coordinator || e.To != c.Session || e.Task != r.Task || e.Run != r.RunID || e.Attempt != r.Attempt || e.Fence != r.Fence || e.TargetSHA != r.TargetSHA {
		return false
	}
	if e.Kind == envelope.Task {
		p := e.TaskPayload
		return p != nil && p.Worktree == r.Worktree && p.PayloadRef == r.PayloadRef && p.Deadline.Equal(r.Deadline) && p.AssignedRole == nil && p.ProgressPct == 0
	}
	return true
}

type cappedOutput struct{ bytes.Buffer }

func (b *cappedOutput) Write(p []byte) (int, error) {
	n := len(p)
	left := (32 << 10) - b.Len()
	if len(p) > left {
		p = p[:left]
	}
	_, err := b.Buffer.Write(p)
	return n, err
}

type command struct {
	cmd            *exec.Cmd
	done           chan error
	stdout, stderr *cappedOutput
	finished       atomic.Bool
	stopOnce       sync.Once
	stopErr        error
}

func startCommand(ctx context.Context, c Config) (*command, error) {
	cmd := exec.CommandContext(ctx, c.Command[0], c.Command[1:]...)
	cmd.Dir = c.Task.Worktree
	cmd.Stdin = strings.NewReader(c.Prompt)
	out, diag := &cappedOutput{}, &cappedOutput{}
	cmd.Stdout = out
	cmd.Stderr = diag
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	running := &command{cmd: cmd, done: make(chan error, 1), stdout: out, stderr: diag}
	cmd.Cancel = running.stop
	cmd.WaitDelay = 250 * time.Millisecond
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		err := cmd.Wait()
		running.finished.Store(true)
		running.done <- err
	}()
	return running, nil
}
func (c *command) stop() error {
	c.stopOnce.Do(func() {
		if c.cmd == nil || c.cmd.Process == nil || c.finished.Load() {
			return
		}
		c.stopErr = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(c.stopErr, syscall.ESRCH) {
			c.stopErr = nil
		}
	})
	return c.stopErr
}
func worker(ctx context.Context, c Config, tp transport.Transport) (result fsm.Run, err error) {
	return workerWithIntervals(ctx, c, tp, time.Minute, 130*time.Second)
}

func workerWithIntervals(ctx context.Context, c Config, tp transport.Transport, heartbeatInterval, retryInterval time.Duration) (result fsm.Run, err error) {
	if heartbeatInterval <= 0 || retryInterval <= 0 {
		return result, fmt.Errorf("worker intervals must be positive")
	}
	// Resolving links prevents aliases from taking independent advisory locks.
	worktree, err := filepath.EvalSymlinks(c.Task.Worktree)
	if err != nil {
		return result, err
	}
	if worktree != c.Task.Worktree {
		return result, fmt.Errorf("worktree must be its resolved absolute path")
	}
	release, err := (checkpoint.File{Path: filepath.Join(worktree, ".coord-writer")}).Lock()
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, release()) }()
	cp := checkpoint.File{Path: filepath.Join(c.State, "executor.json")}
	releaseCP, err := cp.Lock()
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, releaseCP()) }()
	x := &executor.Executor{Transport: tp, Checkpoint: cp, Now: time.Now, UpdateInterval: 3 * time.Minute, SendTimeout: 12 * time.Second, Policy: retry.Policy{BaseDelay: 130 * time.Second, MaxDelay: 5 * time.Minute, Multiplier: 2, MaxRetries: 0}}
	saved, err := cp.Load()
	if err != nil {
		return result, err
	}
	if len(saved) > 1 {
		return result, fmt.Errorf("worker owns exactly one run")
	}
	taskPath := filepath.Join(c.State, "task.json")
	var task envelope.Envelope
	if len(saved) > 0 {
		result = saved[c.Task.RunID]
		raw, loadErr := os.ReadFile(taskPath)
		if loadErr != nil {
			return result, loadErr
		}
		task, err = envelope.Decode(raw)
		if err != nil || !bound(c, task) {
			return result, fmt.Errorf("saved task does not match configured binding")
		}
		if result.OwnerSession != c.Session {
			return result, fmt.Errorf("checkpoint owner differs")
		}
		if result.Terminal() {
			return result, nil
		}
		if result.Status != fsm.ResultReady || result.Result == nil {
			return result, fmt.Errorf("interrupted execution is ambiguous: fence old writer and recover explicitly; automatic rerun refused")
		}
		// A saved RESULT is safe to replay without re-executing the command.
		_ = x.Replay(ctx)
	}
	life, cancel := context.WithDeadline(ctx, c.Task.Deadline)
	inbox := tp.Receive(life)
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	retryTick := time.NewTicker(retryInterval)
	defer retryTick.Stop()
	var running *command
	var finished <-chan error
	defer func() {
		cancel()
		if running != nil {
			err = errors.Join(err, running.stop())
			if finished != nil {
				err = errors.Join(err, <-finished)
			}
		}
	}()
	complete := func(cause error) error {
		payload := envelope.ResultPayload{Outcome: "SUCCESS", Summary: strings.TrimSpace(running.stdout.String())}
		if cause != nil {
			payload.Outcome = "FAILURE"
			payload.Summary = strings.TrimSpace(running.stderr.String())
			if payload.Summary == "" {
				payload.Summary = cause.Error()
			}
		}
		if heartbeatErr := x.Heartbeat(life, task, "RESULT_READY", 100, ""); heartbeatErr != nil {
			// The heartbeat is already durable; Result appends behind it and preserves
			// readiness even when transport delivery failed.
			saved, loadErr := cp.Load()
			if loadErr != nil || saved[task.Run].Status != fsm.ResultReady {
				return errors.Join(heartbeatErr, loadErr)
			}
		}
		sendErr := x.Result(life, task, payload)
		saved, loadErr := cp.Load()
		if loadErr != nil {
			return loadErr
		}
		if saved[task.Run].Result == nil {
			return sendErr
		}
		return nil
	}
	for {
		select {
		case <-life.Done():
			return result, life.Err()
		case commandErr := <-finished:
			finished = nil
			if err = complete(commandErr); err != nil {
				return result, err
			}
			running = nil
			saved, loadErr := cp.Load()
			if loadErr != nil {
				return result, loadErr
			}
			result = saved[c.Task.RunID]
		case <-heartbeat.C:
			if running != nil {
				if err = x.Heartbeat(life, task, "STARTED", 0, ""); err != nil {
					return result, fmt.Errorf("heartbeat delivery failed; stopping writer: %w", err)
				}
			}
		case <-retryTick.C:
			if task.Run != "" {
				_ = x.Replay(life)
			}
		case e, ok := <-inbox:
			if !ok {
				return result, fmt.Errorf("telepathy receive closed")
			}
			if !bound(c, e) {
				continue
			}
			switch e.Kind {
			case envelope.Task:
				if task.Run != "" {
					if result.Status == fsm.ResultReady {
						_ = x.Replay(life)
						continue
					}
					// Duplicate assignment never launches a second command.
					if _, err = x.Accept(life, e); err != nil {
						return result, err
					}
					continue
				}
				task = e
				raw, marshalErr := json.Marshal(task)
				if marshalErr != nil {
					return result, marshalErr
				}
				if err = atomicfile.Write(taskPath, raw); err != nil {
					return result, err
				}
				if _, err = x.Accept(life, task); err != nil {
					return result, err
				}
				if err = x.Heartbeat(life, task, "STARTED", 0, ""); err != nil {
					return result, err
				}
				running, err = startCommand(life, c)
				if err != nil {
					// Persist a normal FAILURE result even when exec cannot start.
					running = &command{stdout: &cappedOutput{}, stderr: &cappedOutput{}}
					if finishErr := complete(err); finishErr != nil {
						return result, errors.Join(err, finishErr)
					}
					running = nil
				} else {
					finished = running.done
				}
			case envelope.ResultAck:
				if err = x.Receipt(life, e); err != nil {
					continue
				}
				saved, loadErr := cp.Load()
				if loadErr != nil {
					return result, loadErr
				}
				result = saved[c.Task.RunID]
				if result.Terminal() {
					return result, nil
				}
			case envelope.Ping:
				pong := e
				pong.Kind = envelope.Pong
				pong.MessageID = envelope.NewID()
				pong.InReplyTo = e.MessageID
				pong.Timestamp = time.Now().UTC()
				pong.From, pong.To = e.To, e.From
				pong.PingPayload = nil
				pong.PongPayload = &envelope.PongPayload{Nonce: e.PingPayload.Nonce, State: "IDLE"}
				if running != nil {
					pong.PongPayload.State = "STARTED"
				}
				if err = tp.Send(life, pong.To, pong); err != nil {
					return result, err
				}
			case envelope.Abort:
				return result, fmt.Errorf("coordinator aborted task: %s", e.AbortPayload.Reason)
			}
			saved, loadErr := cp.Load()
			if loadErr != nil {
				return result, loadErr
			}
			result = saved[c.Task.RunID]
		}
	}
}
