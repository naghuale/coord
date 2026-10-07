//go:build unix

// A local CLI-agent example. It uses real coordinator/executor checkpoints,
// but does not implement a production daemon or automatic recovery.
package main

import (
	"bytes"
	"context"
	"coord"
	"coord/checkpoint"
	"coord/envelope"
	"coord/executor"
	"coord/fsm"
	"coord/retry"
	"coord/store"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type options struct {
	Worktree, StateDir, Prompt string
	Timeout, Heartbeat         time.Duration
	Command                    []string
}

type hostTransport struct {
	task     envelope.Envelope
	receipts []envelope.Envelope
}

func (t *hostTransport) WhoAmI() string                                   { return "local-coordinator" }
func (t *hostTransport) Receive(context.Context) <-chan envelope.Envelope { return nil }
func (t *hostTransport) Send(_ context.Context, _ string, e envelope.Envelope) error {
	switch e.Kind {
	case envelope.Task:
		t.task = e
	case envelope.ResultAck:
		t.receipts = append(t.receipts, e)
	default:
		return fmt.Errorf("local example cannot deliver %s", e.Kind)
	}
	return nil
}

type agentTransport struct{ coordinator *coord.Coordinator }

func (t agentTransport) WhoAmI() string                                   { return "local-executor" }
func (t agentTransport) Receive(context.Context) <-chan envelope.Envelope { return nil }
func (t agentTransport) Send(ctx context.Context, _ string, e envelope.Envelope) error {
	// This in-process example receives the application commit synchronously.
	// A real byte transport only acknowledges delivery and receives RESULT_ACK later.
	return t.coordinator.Handle(ctx, e)
}

type cappedBuffer struct {
	bytes.Buffer
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := (64 << 10) - b.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, err := b.Buffer.Write(p)
	return original, err
}
func (b *cappedBuffer) text() string {
	text := strings.TrimSpace(b.String())
	if b.truncated {
		text += "\n[output truncated at 64 KiB]"
	}
	return text
}

func run(ctx context.Context, cfg options) (result fsm.Run, err error) {
	if cfg.Worktree == "" || cfg.StateDir == "" || cfg.Prompt == "" || len(cfg.Command) == 0 || cfg.Timeout <= 0 || cfg.Heartbeat <= 0 || cfg.Heartbeat > time.Duration(math.MaxInt64/3) {
		return fsm.Run{}, fmt.Errorf("worktree, state, prompt, command and positive bounded timeouts are required")
	}
	worktree, err := filepath.Abs(cfg.Worktree)
	if err != nil {
		return fsm.Run{}, fmt.Errorf("resolve worktree: %w", err)
	}
	worktree, err = filepath.EvalSymlinks(worktree)
	if err != nil {
		return fsm.Run{}, fmt.Errorf("resolve worktree links: %w", err)
	}
	release, err := (checkpoint.File{Path: filepath.Join(worktree, ".coord-writer")}).Lock()
	if err != nil {
		return fsm.Run{}, fmt.Errorf("acquire writer lock: %w", err)
	}
	defer func() {
		if closeErr := release(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("release writer lock: %w", closeErr))
		}
	}()
	head := exec.CommandContext(ctx, "git", "-C", worktree, "rev-parse", "HEAD")
	rawSHA, err := head.Output()
	if err != nil {
		return fsm.Run{}, fmt.Errorf("read worktree HEAD: %w", err)
	}
	stateDir, err := filepath.Abs(cfg.StateDir)
	if err != nil {
		return fsm.Run{}, fmt.Errorf("resolve state: %w", err)
	}
	canonical, err := store.New(checkpoint.File{Path: filepath.Join(stateDir, "runs.json")})
	if err != nil {
		return fsm.Run{}, fmt.Errorf("open canonical store: %w", err)
	}
	defer func() {
		if closeErr := canonical.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close canonical store: %w", closeErr))
		}
	}()
	checkpointFile := checkpoint.File{Path: filepath.Join(stateDir, "executor.json")}
	releaseCheckpoint, err := checkpointFile.Lock()
	if err != nil {
		return fsm.Run{}, fmt.Errorf("acquire executor checkpoint lock: %w", err)
	}
	defer func() {
		if closeErr := releaseCheckpoint(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("release executor checkpoint: %w", closeErr))
		}
	}()
	host := &hostTransport{}
	coordinator, err := coord.New(host, canonical, coord.DefaultConfig())
	if err != nil {
		return fsm.Run{}, err
	}
	taskContext, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	deadline, _ := taskContext.Deadline()
	r := fsm.Run{Task: "local-agent-task", RunID: envelope.NewID(), Attempt: 1, Fence: 1, TargetSHA: strings.TrimSpace(string(rawSHA)), OwnerSession: "local-executor", Worktree: worktree, PayloadRef: "stdin", Deadline: deadline}
	if err = coordinator.Assign(taskContext, r); err != nil {
		return r, err
	}
	task := host.task
	agent := executor.Executor{Transport: agentTransport{coordinator}, Checkpoint: checkpointFile, Policy: retry.Policy{BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, Multiplier: 1}, UpdateInterval: cfg.Heartbeat * 3, SendTimeout: time.Second, Now: time.Now}
	if _, err = agent.Accept(taskContext, task); err != nil {
		return r, err
	}
	if err = agent.Heartbeat(taskContext, task, "STARTED", 0, ""); err != nil {
		return r, err
	}

	finish := func(payload envelope.ResultPayload, cause error) (fsm.Run, error) {
		cleanup, stopCleanup := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stopCleanup()
		if !time.Now().Before(deadline) {
			if tickErr := coordinator.Tick(cleanup); tickErr != nil {
				return r, errors.Join(cause, tickErr)
			}
			failed, _ := canonical.Get(r.RunID)
			return failed, errors.Join(cause, context.DeadlineExceeded)
		}
		if err := agent.Heartbeat(cleanup, task, "RESULT_READY", 100, ""); err != nil {
			return r, errors.Join(cause, err)
		}
		if err := agent.Result(cleanup, task, payload); err != nil {
			return r, errors.Join(cause, err)
		}
		for _, receipt := range host.receipts {
			if err := agent.Receipt(cleanup, receipt); err != nil {
				return r, errors.Join(cause, err)
			}
		}
		committed, _ := canonical.Get(r.RunID)
		local, loadErr := checkpointFile.Load()
		if loadErr != nil {
			return committed, errors.Join(cause, loadErr)
		}
		if local[r.RunID].Status != committed.Status || !local[r.RunID].Terminal() || len(local[r.RunID].Outbox) != 0 {
			return committed, errors.Join(cause, fmt.Errorf("result remains pending coordinator receipt"))
		}
		return committed, cause
	}

	commandContext, stopCommand := context.WithCancel(taskContext)
	defer stopCommand()
	cmd := exec.CommandContext(commandContext, cfg.Command[0], cfg.Command[1:]...)
	cmd.Dir = worktree
	cmd.Stdin = strings.NewReader(cfg.Prompt)
	stdout, stderr := &cappedBuffer{}, &cappedBuffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// Commands must stay in this process group. Daemonizing/setsid commands
	// require a supervisor suited to that agent, rather than this local example.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	killGroup := func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	cmd.Cancel = killGroup
	cmd.WaitDelay = 250 * time.Millisecond
	if err = cmd.Start(); err != nil {
		return finish(envelope.ResultPayload{Outcome: "FAILURE", Summary: "agent could not start"}, fmt.Errorf("start agent: %w", err))
	}
	done := make(chan error, 1)
	go func() { waitErr := cmd.Wait(); done <- errors.Join(waitErr, killGroup()) }()
	tick := time.NewTicker(cfg.Heartbeat)
	defer tick.Stop()
	for {
		select {
		case commandErr := <-done:
			if commandErr != nil {
				summary := stderr.text()
				if summary == "" {
					summary = "agent command failed"
				}
				return finish(envelope.ResultPayload{Outcome: "FAILURE", Summary: summary}, fmt.Errorf("agent command: %w", errors.Join(commandErr, commandContext.Err())))
			}
			return finish(envelope.ResultPayload{Outcome: "SUCCESS", Summary: stdout.text()}, nil)
		case <-taskContext.Done():
			stopCommand()
			waitErr := <-done
			return finish(envelope.ResultPayload{Outcome: "FAILURE", Summary: "agent canceled"}, errors.Join(taskContext.Err(), waitErr))
		case <-tick.C:
			heartbeatErr := coordinator.Tick(taskContext)
			if heartbeatErr == nil {
				heartbeatErr = agent.Heartbeat(taskContext, task, "STARTED", 0, "")
			}
			if heartbeatErr != nil {
				stopCommand()
				waitErr := <-done
				return finish(envelope.ResultPayload{Outcome: "FAILURE", Summary: "coord heartbeat failed"}, errors.Join(heartbeatErr, waitErr))
			}
		}
	}
}

func main() {
	worktree := flag.String("worktree", "", "Git worktree to execute in")
	state := flag.String("state", "state/local-agent", "separate runtime state directory")
	prompt := flag.String("prompt", "", "text passed to the agent on stdin")
	timeout := flag.Duration("timeout", 10*time.Minute, "absolute task timeout")
	heartbeat := flag.Duration("heartbeat", 15*time.Second, "heartbeat interval")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	result, err := run(ctx, options{Worktree: *worktree, StateDir: *state, Prompt: *prompt, Timeout: *timeout, Heartbeat: *heartbeat, Command: flag.Args()})
	if result.RunID != "" {
		report := struct {
			Run    string                  `json:"run"`
			Status fsm.Status              `json:"status"`
			Result *envelope.ResultPayload `json:"result,omitempty"`
		}{result.RunID, result.Status, result.Result}
		if encodeErr := json.NewEncoder(os.Stdout).Encode(report); encodeErr != nil {
			err = errors.Join(err, encodeErr)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
