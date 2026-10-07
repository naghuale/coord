//go:build unix

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
	"testing"
	"time"
)

type workerTransport struct {
	dropReceipt bool
	self        string
	in          chan envelope.Envelope
	sent        []envelope.Envelope
}

func (t *workerTransport) WhoAmI() string                                   { return t.self }
func (t *workerTransport) Receive(context.Context) <-chan envelope.Envelope { return t.in }
func (t *workerTransport) Send(_ context.Context, _ string, e envelope.Envelope) error {
	if err := e.Validate(); err != nil {
		return err
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
