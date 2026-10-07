package executor

import (
	"context"
	"coord"
	"coord/checkpoint"
	"coord/envelope"
	"coord/fsm"
	"coord/retry"
	"coord/store"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recordingTransport struct {
	send func(envelope.Envelope) error
}

func (t recordingTransport) WhoAmI() string { return "exec" }
func (t recordingTransport) Send(_ context.Context, _ string, e envelope.Envelope) error {
	return t.send(e)
}
func (t recordingTransport) Receive(context.Context) <-chan envelope.Envelope { return nil }

func fixture(t *testing.T) (*Executor, envelope.Envelope) {
	t.Helper()
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	task := envelope.Envelope{SchemaVersion: 1, MessageID: envelope.NewID(), Timestamp: now, From: "coord", To: "exec", Kind: envelope.Task, Task: "task", Run: "run", Attempt: 1, Fence: 1, TargetSHA: strings.Repeat("a", 40), TaskPayload: &envelope.TaskPayload{Worktree: "unused", Deadline: now.Add(time.Hour)}}
	x := &Executor{Checkpoint: checkpoint.File{Path: filepath.Join(t.TempDir(), "executor.json")}, Policy: retry.Policy{BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, Multiplier: 2, MaxRetries: 0}, UpdateInterval: time.Minute, SendTimeout: time.Second, Now: func() time.Time { return now }}
	r := fsm.Run{Task: task.Task, RunID: task.Run, Attempt: task.Attempt, Fence: task.Fence, TargetSHA: task.TargetSHA, OwnerSession: "exec", Status: fsm.Accepted, Deadline: task.TaskPayload.Deadline, TaskMessages: []string{task.MessageID}}
	if err := x.Checkpoint.Save(map[string]fsm.Run{r.RunID: r}); err != nil {
		t.Fatal(err)
	}
	return x, task
}

func withWorktree(t *testing.T, task *envelope.Envelope) {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	task.TargetSHA = strings.TrimSpace(string(out))
	task.TaskPayload.Worktree = dir
}

func TestResponsesDurableBeforeFirstSend(t *testing.T) {
	for _, kind := range []envelope.Kind{envelope.Ack, envelope.Heartbeat} {
		t.Run(string(kind), func(t *testing.T) {
			x, task := fixture(t)
			if kind == envelope.Ack {
				withWorktree(t, &task)
				if err := x.Checkpoint.Save(map[string]fsm.Run{}); err != nil {
					t.Fatal(err)
				}
			}
			sent := false
			x.Transport = recordingTransport{send: func(e envelope.Envelope) error {
				sent = true
				runs, err := x.Checkpoint.Load()
				if err != nil {
					t.Fatal(err)
				}
				r := runs[task.Run]
				if len(r.Outbox) != 1 || r.Outbox[0].MessageID != e.MessageID {
					t.Errorf("message absent from durable outbox before send: %+v", r.Outbox)
				}
				return nil
			}}
			var err error
			if kind == envelope.Ack {
				_, err = x.Accept(context.Background(), task)
			} else {
				err = x.Heartbeat(context.Background(), task, "STARTED", 25, "")
			}
			if err != nil {
				t.Fatal(err)
			}
			if !sent {
				t.Fatal("no send")
			}
			runs, err := x.Checkpoint.Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(runs[task.Run].Outbox) != 0 {
				t.Fatal("delivered response remains queued")
			}
		})
	}
}

func TestRecoveryClearsOldGenerationMessages(t *testing.T) {
	x, task := fixture(t)
	withWorktree(t, &task)
	runs, err := x.Checkpoint.Load()
	if err != nil {
		t.Fatal(err)
	}
	r := runs[task.Run]
	r.TargetSHA = task.TargetSHA
	r.OwnerSession = "old"
	r.Status = fsm.ResultReady
	r.Blocker = "old blocker"
	r.Result = &envelope.ResultPayload{Outcome: "SUCCESS"}
	r.Outbox = []envelope.Envelope{{MessageID: "old", From: "old", Fence: 1}}
	runs[task.Run] = r
	if err = x.Checkpoint.Save(runs); err != nil {
		t.Fatal(err)
	}
	task.Fence = 2
	x.Transport = recordingTransport{send: func(e envelope.Envelope) error {
		if e.Fence != 2 || e.From != "exec" {
			t.Errorf("sent old identity: %+v", e)
		}
		return nil
	}}
	if _, err = x.Accept(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	runs, err = x.Checkpoint.Load()
	if err != nil {
		t.Fatal(err)
	}
	r = runs[task.Run]
	if len(r.Outbox) != 0 || r.Result != nil || r.Blocker != "" || r.Status != fsm.Accepted {
		t.Fatalf("old generation leaked: %+v", r)
	}
}

func TestReadinessAndResultSurviveOutageInOrder(t *testing.T) {
	x, task := fixture(t)
	x.Transport = offline{}
	if err := x.Heartbeat(context.Background(), task, "RESULT_READY", 100, ""); err == nil {
		t.Fatal("expected outage")
	}
	// Readiness is irreversible even when its transport send fails.
	if err := x.Result(context.Background(), task, envelope.ResultPayload{Outcome: "SUCCESS", Summary: "done"}); err == nil {
		t.Fatal("expected outage")
	}
	runs, err := x.Checkpoint.Load()
	if err != nil {
		t.Fatal(err)
	}
	r := runs[task.Run]
	if r.Status != fsm.ResultReady || r.Result == nil || len(r.Outbox) != 2 {
		t.Fatalf("readiness/result lost: %+v", r)
	}
	var kinds []envelope.Kind
	x.Transport = recordingTransport{send: func(e envelope.Envelope) error { kinds = append(kinds, e.Kind); return nil }}
	if err = x.Replay(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 2 || kinds[0] != envelope.Heartbeat || kinds[1] != envelope.Result {
		t.Fatalf("replay order: %v", kinds)
	}
	runs, err = x.Checkpoint.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs[task.Run].Outbox) != 1 || runs[task.Run].Outbox[0].Kind != envelope.Result {
		t.Fatal("RESULT must remain until coordinator receipt")
	}
}

func TestFailedHeartbeatDoesNotOverwriteSavedResult(t *testing.T) {
	x, task := fixture(t)
	runs, err := x.Checkpoint.Load()
	if err != nil {
		t.Fatal(err)
	}
	r := runs[task.Run]
	r.Status = fsm.ResultReady
	result := x.reply(task, envelope.Result)
	result.ResultPayload = &envelope.ResultPayload{Outcome: "SUCCESS"}
	r.Result = result.ResultPayload
	r.Outbox = []envelope.Envelope{result}
	runs[task.Run] = r
	if err = x.Checkpoint.Save(runs); err != nil {
		t.Fatal(err)
	}
	x.Transport = offline{}
	if err = x.Heartbeat(context.Background(), task, "RESULT_READY", 100, ""); err == nil {
		t.Fatal("expected outage")
	}
	runs, err = x.Checkpoint.Load()
	if err != nil {
		t.Fatal(err)
	}
	r = runs[task.Run]
	if r.Status != fsm.ResultReady || len(r.Outbox) != 2 || r.Outbox[0].MessageID != result.MessageID {
		t.Fatalf("saved result overwritten: %+v", r)
	}
}

func TestCanceledSendRemainsReplayable(t *testing.T) {
	x, task := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	x.Transport = recordingTransport{send: func(envelope.Envelope) error { return ctx.Err() }}
	if err := x.Heartbeat(ctx, task, "STARTED", 10, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	runs, err := x.Checkpoint.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs[task.Run].Outbox) != 1 {
		t.Fatal("canceled send lost")
	}
}

func TestReplayRenewsExpiredResponsesBeforeSending(t *testing.T) {
	for _, kind := range []envelope.Kind{envelope.Ack, envelope.Heartbeat} {
		t.Run(string(kind), func(t *testing.T) {
			x, task := fixture(t)
			runs, err := x.Checkpoint.Load()
			if err != nil {
				t.Fatal(err)
			}
			r := runs[task.Run]
			e := x.reply(task, kind)
			deadline := x.Now().Add(time.Minute)
			if kind == envelope.Ack {
				e.AckPayload = &envelope.AckPayload{State: "ACCEPTED", NextUpdateBy: deadline}
			} else {
				e.HeartbeatPayload = &envelope.HeartbeatPayload{State: "STARTED", NextUpdateBy: deadline, ProgressPct: 25}
			}
			r.Outbox = []envelope.Envelope{e}
			runs[task.Run] = r
			if err = x.Checkpoint.Save(runs); err != nil {
				t.Fatal(err)
			}
			later := x.Now().Add(2 * time.Minute)
			x.Now = func() time.Time { return later }
			x.Transport = recordingTransport{send: func(sent envelope.Envelope) error {
				if sent.MessageID == e.MessageID || sent.InReplyTo != e.InReplyTo || sent.Fence != e.Fence {
					t.Errorf("invalid renewal: %+v", sent)
				}
				if err := sent.Validate(); err != nil {
					t.Fatal(err)
				}
				next := time.Time{}
				if sent.AckPayload != nil {
					next = sent.AckPayload.NextUpdateBy
				} else {
					next = sent.HeartbeatPayload.NextUpdateBy
				}
				if !next.After(later) {
					t.Fatal("expired replay")
				}
				saved, err := x.Checkpoint.Load()
				if err != nil {
					t.Fatal(err)
				}
				if saved[task.Run].Outbox[0].MessageID != sent.MessageID {
					t.Fatal("renewal not durable")
				}
				return nil
			}}
			if err = x.Replay(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReplayRejectsStaleCheckpointMessage(t *testing.T) {
	x, task := fixture(t)
	runs, err := x.Checkpoint.Load()
	if err != nil {
		t.Fatal(err)
	}
	r := runs[task.Run]
	r.Fence = 2
	e := x.reply(task, envelope.Heartbeat)
	e.HeartbeatPayload = &envelope.HeartbeatPayload{State: "STARTED", NextUpdateBy: x.Now().Add(time.Minute)}
	r.Outbox = []envelope.Envelope{e}
	runs[task.Run] = r
	if err = x.Checkpoint.Save(runs); err != nil {
		t.Fatal(err)
	}
	x.Transport = recordingTransport{send: func(envelope.Envelope) error { t.Fatal("stale message sent"); return nil }}
	if err = x.Replay(context.Background()); err == nil {
		t.Fatal("stale checkpoint accepted")
	}
}

func TestReplayRestoresLocalStatusAfterOutage(t *testing.T) {
	x, task := fixture(t)
	x.Transport = offline{}
	if err := x.Heartbeat(context.Background(), task, "STARTED", 25, ""); err == nil {
		t.Fatal("expected outage")
	}
	x.Transport = recordingTransport{send: func(envelope.Envelope) error { return nil }}
	if err := x.Replay(context.Background()); err != nil {
		t.Fatal(err)
	}
	runs, err := x.Checkpoint.Load()
	if err != nil {
		t.Fatal(err)
	}
	if r := runs[task.Run]; r.Status != fsm.Started || r.Blocker != "" {
		t.Fatalf("transport blocker remained: %+v", r)
	}
}

func TestResultReadinessCannotRegressLocally(t *testing.T) {
	x, task := fixture(t)
	x.Transport = recordingTransport{send: func(envelope.Envelope) error { return nil }}
	if err := x.Heartbeat(context.Background(), task, "RESULT_READY", 100, ""); err != nil {
		t.Fatal(err)
	}
	if err := x.Heartbeat(context.Background(), task, "STARTED", 50, ""); err == nil {
		t.Fatal("readiness regressed")
	}
}

type coordinatorTransport struct{ task envelope.Envelope }

func (t *coordinatorTransport) WhoAmI() string { return "coord" }
func (t *coordinatorTransport) Send(_ context.Context, _ string, e envelope.Envelope) error {
	t.task = e
	return nil
}
func (t *coordinatorTransport) Receive(context.Context) <-chan envelope.Envelope { return nil }

func TestCoordinatorCommitsQueuedResultAfterExecutorRestart(t *testing.T) {
	x, task := fixture(t)
	withWorktree(t, &task)
	if err := x.Checkpoint.Save(map[string]fsm.Run{}); err != nil {
		t.Fatal(err)
	}
	canonical := checkpoint.File{Path: filepath.Join(t.TempDir(), "canonical.json")}
	s, err := store.New(canonical)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close test resource: %v", err)
		}
	})
	tp := &coordinatorTransport{}
	now := x.Now()
	cfg := coord.DefaultConfig()
	cfg.Now = func() time.Time { return now }
	c, err := coord.New(tp, s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := fsm.Run{Task: task.Task, RunID: task.Run, Attempt: task.Attempt, Fence: task.Fence, TargetSHA: task.TargetSHA, OwnerSession: task.To, Worktree: task.TaskPayload.Worktree, Deadline: task.TaskPayload.Deadline}
	if err = c.Assign(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	task = tp.task
	x.Transport = offline{}
	if _, err = x.Accept(context.Background(), task); err == nil {
		t.Fatal("expected unavailable ACK")
	}
	for _, state := range []string{"STARTED", "RESULT_READY"} {
		if err = x.Heartbeat(context.Background(), task, state, 100, ""); err == nil {
			t.Fatal("expected unavailable heartbeat")
		}
	}
	if err = x.Result(context.Background(), task, envelope.ResultPayload{Outcome: "SUCCESS", Summary: "durable result"}); err == nil {
		t.Fatal("expected unavailable result")
	}
	now = now.Add(2 * time.Minute)
	// Reconstruct the executor using only its disk checkpoint after an outage.
	restarted := *x
	restarted.Now = func() time.Time { return now }
	restarted.Transport = recordingTransport{send: func(e envelope.Envelope) error { return c.Handle(context.Background(), e) }}
	if err = restarted.Replay(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get(task.Run)
	if !ok || got.Status != fsm.Done || got.Result == nil || got.Result.Summary != "durable result" {
		t.Fatalf("result not committed: %+v", got)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.New(canonical)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reloaded.Close(); err != nil {
			t.Errorf("close test resource: %v", err)
		}
	})
	got, ok = reloaded.Get(task.Run)
	if !ok || got.Status != fsm.Done || got.Result == nil {
		t.Fatal("coordinator restart lost completion")
	}
}

func commitForTest(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "work")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
}
