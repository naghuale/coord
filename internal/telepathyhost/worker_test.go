//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd || illumos

// This helper uses flock as a live-writer token and is intentionally unavailable
// on unsupported OSes; fail-closed ownership behavior is tested in internal/filelock.

package telepathyhost

import (
	"context"
	"coord/checkpoint"
	"coord/envelope"
	"coord/fsm"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type workerTransport struct {
	dropReceipt       bool
	failHeartbeatOn   <-chan struct{}
	startedHeartbeats int
	self              string
	in                chan envelope.Envelope
	sent              []envelope.Envelope
}

func (t *workerTransport) WhoAmI() string                                   { return t.self }
func (t *workerTransport) Receive(context.Context) <-chan envelope.Envelope { return t.in }
func (t *workerTransport) Send(_ context.Context, _ string, e envelope.Envelope) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if e.Kind == envelope.Heartbeat && e.HeartbeatPayload.State == "STARTED" {
		t.startedHeartbeats++
		if t.failHeartbeatOn != nil && t.startedHeartbeats > 1 {
			select {
			case <-t.failHeartbeatOn:
				return errors.New("lease renewal failed")
			default:
			}
		}
	}
	t.sent = append(t.sent, e)
	if e.Kind == envelope.Result && !t.dropReceipt {
		receipt := e
		receipt.Kind = envelope.ResultAck
		receipt.MessageID = envelope.NewID()
		receipt.InReplyTo = e.MessageID
		receipt.From, receipt.To = e.To, e.From
		receipt.ResultPayload = nil
		receipt.ResultAckPayload = &envelope.ResultAckPayload{Outcome: e.ResultPayload.Outcome}
		t.in <- receipt
	}
	return nil
}
func fixture(t *testing.T) (Config, envelope.Envelope) {
	t.Helper()
	dir, resolveErr := filepath.EvalSymlinks(t.TempDir())
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	for _, args := range [][]string{{"init", "--quiet", dir}, {"-C", dir, "-c", "user.name=CoordTest", "-c", "user.email=coord@test.invalid", "commit", "--allow-empty", "--quiet", "-m", "fixture"}} {
		if b, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", b, err)
		}
	}
	sha, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	c := Config{Session: "worker", Coordinator: "host", State: filepath.Join(dir, "state"), Task: fsm.Run{Task: "task", RunID: "run", Attempt: 1, Fence: 1, OwnerSession: "worker", Worktree: dir, TargetSHA: strings.TrimSpace(string(sha)), Deadline: time.Now().Add(time.Minute)}, Prompt: "test prompt", Command: []string{"/bin/sh", "-c", "printf x >> execution-count; cat"}}
	task := envelope.Envelope{SchemaVersion: 1, MessageID: envelope.NewID(), Timestamp: time.Now(), From: "host", To: "worker", Kind: envelope.Task, Task: "task", Run: "run", Attempt: 1, Fence: 1, TargetSHA: c.Task.TargetSHA, TaskPayload: &envelope.TaskPayload{Worktree: dir, Deadline: c.Task.Deadline}}
	return c, task
}
func TestWorkerDuplicateExecutesOnceAndRequiresReceipt(t *testing.T) {
	c, task := fixture(t)
	tp := &workerTransport{self: c.Session, in: make(chan envelope.Envelope, 16)}
	wrong := task
	wrong.TargetSHA = strings.Repeat("0", 40)
	tp.in <- wrong
	ping := task
	ping.Kind = envelope.Ping
	ping.TaskPayload = nil
	ping.MessageID = envelope.NewID()
	ping.PingPayload = &envelope.PingPayload{Nonce: "probe-nonce"}
	tp.in <- ping
	tp.in <- task
	tp.in <- task
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := worker(ctx, c, tp)
	if err != nil {
		t.Fatal(err)
	}
	if len(tp.sent) == 0 || tp.sent[0].Kind != envelope.Pong || tp.sent[0].InReplyTo != ping.MessageID || tp.sent[0].PongPayload.Nonce != ping.PingPayload.Nonce {
		t.Fatal("probe not correlated")
	}
	if result.Status != fsm.Done || len(result.Outbox) != 0 || result.Result.Summary != c.Prompt {
		t.Fatalf("%+v", result)
	}
	count, err := os.ReadFile(filepath.Join(c.Task.Worktree, "execution-count"))
	if err != nil {
		t.Fatal(err)
	}
	if string(count) != "x" {
		t.Fatalf("executed %q", count)
	}
	if _, err = worker(ctx, c, tp); err != nil {
		t.Fatal(err)
	}
	count, err = os.ReadFile(filepath.Join(c.Task.Worktree, "execution-count"))
	if err != nil || string(count) != "x" {
		t.Fatalf("restart executed: %s %v", count, err)
	}
}
func TestWorkerRefusesAmbiguousRestart(t *testing.T) {
	c, task := fixture(t)
	cp := checkpoint.File{Path: filepath.Join(c.State, "executor.json")}
	r := c.Task
	r.Status = fsm.Started
	r.CoordinatorSession = "host"
	if err := cp.Save(map[string]fsm.Run{r.RunID: r}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(c.State, "task.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	tp := &workerTransport{self: c.Session, in: make(chan envelope.Envelope, 1)}
	if _, err = worker(context.Background(), c, tp); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("restart: %v", err)
	}
	if _, err = os.Stat(filepath.Join(c.Task.Worktree, "execution-count")); !os.IsNotExist(err) {
		t.Fatal("command started")
	}
}
func TestBoundRejectsReassignedTask(t *testing.T) {
	c, task := fixture(t)
	if !bound(c, task) {
		t.Fatal("valid assignment rejected")
	}
	task.TaskPayload.Worktree = t.TempDir()
	if bound(c, task) {
		t.Fatal("unauthorized worktree accepted")
	}
}

func TestWorkerRestartsPendingResultWithoutReexecution(t *testing.T) {
	c, task := fixture(t)
	tp := &workerTransport{self: c.Session, in: make(chan envelope.Envelope, 16), dropReceipt: true}
	tp.in <- task
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	_, err := worker(ctx, c, tp)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("missing receipt must keep worker pending: %v", err)
	}
	cp := checkpoint.File{Path: filepath.Join(c.State, "executor.json")}
	saved, err := cp.Load()
	if err != nil {
		t.Fatal(err)
	}
	pending := saved[c.Task.RunID]
	if pending.Status != fsm.ResultReady || pending.Result == nil || len(pending.Outbox) == 0 {
		t.Fatalf("pending result lost: %+v", pending)
	}
	tp = &workerTransport{self: c.Session, in: make(chan envelope.Envelope, 16)}
	retry, cancelRetry := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRetry()
	result, err := worker(retry, c, tp)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != fsm.Done || len(result.Outbox) != 0 || result.ResultMessageID != pending.ResultMessageID {
		t.Fatalf("receipt identity changed: %+v", result)
	}
	count, err := os.ReadFile(filepath.Join(c.Task.Worktree, "execution-count"))
	if err != nil || string(count) != "x" {
		t.Fatalf("result replay reexecuted command: %q %v", count, err)
	}
}

func TestWorkerAbortStopsWriterProcessGroup(t *testing.T) {
	c, task := fixture(t)
	marker := filepath.Join(c.Task.Worktree, "writer-count")
	setWriterHelperEnv(t, marker)
	t.Cleanup(func() { stopWriterHelper(t, marker) })
	c.Command = []string{os.Args[0], "-test.run=^TestWriterProcessHelper$"}
	tp := &workerTransport{self: c.Session, in: make(chan envelope.Envelope, 4)}
	tp.in <- task
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := worker(ctx, c, tp); done <- err }()
	waitForFileSizeOrError(t, marker, 2, done)
	assertWriterActive(t, marker)
	abort := task
	abort.MessageID = envelope.NewID()
	abort.InReplyTo = task.MessageID
	abort.Kind = envelope.Abort
	abort.TaskPayload = nil
	abort.AbortPayload = &envelope.AbortPayload{Reason: "owner stopped task"}
	tp.in <- abort
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "coordinator aborted task") {
			t.Fatalf("worker abort result = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("worker did not stop after ABORT")
	}
	assertWriterStopped(t, marker)
}

func TestWorkerLeaseLossStopsWriterProcessGroup(t *testing.T) {
	c, task := fixture(t)
	marker := filepath.Join(c.Task.Worktree, "writer-count")
	setWriterHelperEnv(t, marker)
	t.Cleanup(func() { stopWriterHelper(t, marker) })
	c.Command = []string{os.Args[0], "-test.run=^TestWriterProcessHelper$"}
	loseLease := make(chan struct{})
	tp := &workerTransport{self: c.Session, in: make(chan envelope.Envelope, 4), failHeartbeatOn: loseLease}
	tp.in <- task
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := workerWithIntervals(ctx, c, tp, 500*time.Millisecond, time.Hour); done <- err }()
	waitForFileSizeOrError(t, marker, 1, done)
	assertWriterActive(t, marker)
	close(loseLease)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "heartbeat delivery failed; stopping writer") {
			t.Fatalf("worker lease-loss result = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("worker did not stop after lease renewal failed")
	}
	assertWriterStopped(t, marker)
}

func TestWriterProcessHelper(t *testing.T) {
	mode := os.Getenv("COORD_WRITER_HELPER_MODE")
	if mode == "" {
		return
	}
	marker := os.Getenv("COORD_WRITER_HELPER_MARKER")
	if mode == "parent" {
		child := exec.Command(os.Args[0], "-test.run=^TestWriterProcessHelper$")
		child.Env = helperEnv("child", marker)
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		if err := child.Wait(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if mode != "child" {
		t.Fatalf("unknown helper mode %q", mode)
	}
	writerLock, err := os.OpenFile(marker+".alive", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestFile(t, writerLock)
	if err := syscall.Flock(int(writerLock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	// This descendant deliberately keeps writing until the cleanup sentinel;
	// a PID-only kill of its parent must leave observable work.
	for {
		if _, err := os.Stat(marker + ".stop"); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		file, err := os.OpenFile(marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.WriteString("x"); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err = file.Close(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func setWriterHelperEnv(t *testing.T, marker string) {
	t.Helper()
	t.Setenv("COORD_WRITER_HELPER_MODE", "parent")
	t.Setenv("COORD_WRITER_HELPER_MARKER", marker)
}

func helperEnv(mode, marker string) []string {
	var env []string
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "COORD_WRITER_HELPER_MODE=") && !strings.HasPrefix(value, "COORD_WRITER_HELPER_MARKER=") {
			env = append(env, value)
		}
	}
	return append(env, "COORD_WRITER_HELPER_MODE="+mode, "COORD_WRITER_HELPER_MARKER="+marker)
}

func stopWriterHelper(t *testing.T, marker string) {
	t.Helper()
	if err := os.WriteFile(marker+".stop", nil, 0600); err != nil {
		t.Errorf("stop helper child: %v", err)
		return
	}
	if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
		return
	} else if err != nil {
		t.Errorf("inspect helper marker during cleanup: %v", err)
		return
	}
	if err := waitForWriterLockRelease(marker, 2*time.Second); err != nil {
		t.Errorf("helper writer lock remained held during cleanup: %v", err)
	}
	if err := waitForMarkerStable(marker, 100*time.Millisecond, 2*time.Second); err != nil {
		t.Errorf("helper writer did not stop during cleanup: %v", err)
	}
}

func waitForFileSizeOrError(t *testing.T, path string, min int, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("worker exited before writer marker: %v", err)
		default:
		}
		if info, err := os.Stat(path); err == nil && info.Size() >= int64(min) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("writer did not create %s", path)
}

func assertWriterStopped(t *testing.T, path string) {
	t.Helper()
	if err := waitForWriterLockRelease(path, time.Second); err != nil {
		t.Fatalf("writer still holds its liveness lock after worker return: %v", err)
	}
	if err := waitForMarkerStable(path, 100*time.Millisecond, time.Second); err != nil {
		t.Fatalf("writer continued after worker return: %v", err)
	}
}

func assertWriterActive(t *testing.T, marker string) {
	t.Helper()
	lockFile, err := os.OpenFile(marker+".alive", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestFile(t, lockFile)
	err = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return
	}
	if err == nil {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
	}
	t.Fatalf("writer liveness lock was not held before stop: %v", err)
}

func waitForWriterLockRelease(marker string, timeout time.Duration) (retErr error) {
	lockFile, err := os.OpenFile(marker+".alive", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, lockFile.Close()) }()
	deadline := time.Now().Add(timeout)
	for {
		err = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("writer liveness lock remained held")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func closeTestFile(t *testing.T, file *os.File) {
	t.Helper()
	if err := file.Close(); err != nil {
		t.Errorf("close test file: %v", err)
	}
}

func waitForMarkerStable(path string, stableFor, timeout time.Duration) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	lastSize := info.Size()
	stableSince := time.Now()
	deadline := stableSince.Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		info, err = os.Stat(path)
		if err != nil {
			return err
		}
		if info.Size() != lastSize {
			lastSize = info.Size()
			stableSince = time.Now()
			continue
		}
		if time.Since(stableSince) >= stableFor {
			return nil
		}
	}
	return errors.New("writer marker kept changing")
}
