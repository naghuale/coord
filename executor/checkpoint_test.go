package executor

import (
	"context"
	"coord/checkpoint"
	"coord/envelope"
	"coord/fsm"
	"coord/retry"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type offline struct{}

func (offline) WhoAmI() string                                        { return "exec" }
func (offline) Send(context.Context, string, envelope.Envelope) error { return errors.New("offline") }
func (offline) Receive(context.Context) <-chan envelope.Envelope      { return nil }
func TestUnavailableTransportCheckpointsProgress(t *testing.T) {
	now := time.Now()
	f := checkpoint.File{Path: filepath.Join(t.TempDir(), "executor.json")}
	r := fsm.Run{Task: "t", RunID: "r", Attempt: 3, Fence: 1, OwnerSession: "exec", Status: fsm.Started}
	if err := f.Save(map[string]fsm.Run{"r": r}); err != nil {
		t.Fatal(err)
	}
	x := Executor{Transport: offline{}, Checkpoint: f, Policy: retry.Policy{BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, Multiplier: 2, MaxRetries: 2}, UpdateInterval: time.Minute, SendTimeout: time.Second, Now: func() time.Time { return now }}
	r.ProgressPct = 42
	e := envelope.Envelope{MessageID: "hb", From: "exec", Fence: r.Fence, Task: r.Task, Run: r.RunID, Attempt: r.Attempt, TargetSHA: r.TargetSHA}
	if err := x.deliver(context.Background(), r, e); err == nil {
		t.Fatal("offline send succeeded")
	}
	runs, err := f.Load()
	if err != nil {
		t.Fatal(err)
	}
	got := runs["r"]
	if got.Status != fsm.Blocked || got.Blocker != "TELEPATHY_UNAVAILABLE" || got.ProgressPct != 42 || len(got.Outbox) != 1 {
		t.Fatalf("lost checkpoint: %+v", got)
	}
}
