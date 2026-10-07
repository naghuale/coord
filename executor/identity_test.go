package executor

import (
	"context"
	"coord/envelope"
	"coord/fsm"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestMismatchedTaskCannotMutateCheckpoint(t *testing.T) {
	for _, method := range []string{"heartbeat", "result"} {
		t.Run(method, func(t *testing.T) {
			x, task := fixture(t)
			runs, err := x.Checkpoint.Load()
			if err != nil {
				t.Fatal(err)
			}
			r := runs[task.Run]
			r.Status = fsm.ResultReady
			runs[task.Run] = r
			if err = x.Checkpoint.Save(runs); err != nil {
				t.Fatal(err)
			}
			before, err := x.Checkpoint.Load()
			if err != nil {
				t.Fatal(err)
			}
			task.Attempt++
			x.Transport = recordingTransport{send: func(envelope.Envelope) error { t.Fatal("sent mismatched task"); return nil }}
			if method == "heartbeat" {
				err = x.Heartbeat(context.Background(), task, "RESULT_READY", 100, "")
			} else {
				err = x.Result(context.Background(), task, envelope.ResultPayload{Outcome: "SUCCESS"})
			}
			if err == nil {
				t.Fatal("mismatched task accepted")
			}
			after, err := x.Checkpoint.Load()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected task mutated checkpoint")
			}
		})
	}
}
func TestAcceptRejectsExpiredTaskBeforeGit(t *testing.T) {
	x, task := fixture(t)
	later := x.Now().Add(2 * time.Hour)
	x.Now = func() time.Time { return later }
	x.Transport = recordingTransport{send: func(envelope.Envelope) error { t.Fatal("expired TASK acknowledged"); return nil }}
	before, err := x.Checkpoint.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = x.Accept(context.Background(), task); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected task deadline before git access: %v", err)
	}
	after, err := x.Checkpoint.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("expired task mutated checkpoint")
	}
}
func TestDuplicateTaskRemainsAcceptableAfterHeadAdvances(t *testing.T) {
	x, task := fixture(t)
	withWorktree(t, &task)
	if err := x.Checkpoint.Save(map[string]fsm.Run{}); err != nil {
		t.Fatal(err)
	}
	x.Transport = recordingTransport{send: func(envelope.Envelope) error { return nil }}
	if _, err := x.Accept(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	commitForTest(t, task.TaskPayload.Worktree)
	if _, err := x.Accept(context.Background(), task); err != nil {
		t.Fatalf("duplicate rejected after legitimate commit: %v", err)
	}
}
