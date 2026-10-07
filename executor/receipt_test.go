package executor

import (
	"context"
	"coord/envelope"
	"coord/fsm"
	"reflect"
	"testing"
)

func resultAndReceipt(t *testing.T) (*Executor, envelope.Envelope, envelope.Envelope) {
	t.Helper()
	x, task := fixture(t)
	runs, err := x.Checkpoint.Load()
	if err != nil {
		t.Fatal(err)
	}
	r := runs[task.Run]
	r.Status = fsm.ResultReady
	r.CoordinatorSession = task.From
	runs[task.Run] = r
	if err = x.Checkpoint.Save(runs); err != nil {
		t.Fatal(err)
	}
	var result envelope.Envelope
	x.Transport = recordingTransport{send: func(e envelope.Envelope) error { result = e; return nil }}
	if err = x.Result(context.Background(), task, envelope.ResultPayload{Outcome: "SUCCESS", Summary: "done"}); err != nil {
		t.Fatal(err)
	}
	ack := result
	ack.Kind = envelope.ResultAck
	ack.MessageID = envelope.NewID()
	ack.InReplyTo = result.MessageID
	ack.From, ack.To = result.To, result.From
	ack.ResultPayload = nil
	ack.ResultAckPayload = &envelope.ResultAckPayload{Outcome: "SUCCESS"}
	return x, task, ack
}
func TestReceiptDurablyCompletesExecutorAndIsIdempotent(t *testing.T) {
	x, task, ack := resultAndReceipt(t)
	runs, err := x.Checkpoint.Load()
	if err != nil {
		t.Fatal(err)
	}
	if runs[task.Run].Status != fsm.ResultReady || len(runs[task.Run].Outbox) != 1 {
		t.Fatal("transport delivery declared completion")
	}
	if err = x.Receipt(context.Background(), ack); err != nil {
		t.Fatal(err)
	}
	restarted := *x
	if err = restarted.Receipt(context.Background(), ack); err != nil {
		t.Fatal(err)
	}
	runs, err = x.Checkpoint.Load()
	if err != nil {
		t.Fatal(err)
	}
	r := runs[task.Run]
	if r.Status != fsm.Done || len(r.Outbox) != 0 || r.Result == nil {
		t.Fatalf("completion not saved: %+v", r)
	}
}
func TestForgedOrStaleReceiptCannotRemoveResult(t *testing.T) {
	for _, field := range []string{"sender", "reply", "fence", "outcome"} {
		t.Run(field, func(t *testing.T) {
			x, _, ack := resultAndReceipt(t)
			before, err := x.Checkpoint.Load()
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "sender":
				ack.From = "other"
			case "reply":
				ack.InReplyTo = "unrelated"
			case "fence":
				ack.Fence++
			case "outcome":
				ack.ResultAckPayload.Outcome = "PARTIAL"
			}
			if err = x.Receipt(context.Background(), ack); err == nil {
				t.Fatal("invalid receipt accepted")
			}
			after, err := x.Checkpoint.Load()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("invalid receipt removed pending result")
			}
		})
	}
}
func TestHeartbeatCannotReopenCompletedExecutor(t *testing.T) {
	x, task, ack := resultAndReceipt(t)
	if err := x.Receipt(context.Background(), ack); err != nil {
		t.Fatal(err)
	}
	if err := x.Heartbeat(context.Background(), task, "STARTED", 20, ""); err == nil {
		t.Fatal("completed executor reopened")
	}
}
func TestRepeatedResultKeepsMessageIdentity(t *testing.T) {
	x, task, _ := resultAndReceipt(t)
	before, err := x.Checkpoint.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err = x.Result(context.Background(), task, envelope.ResultPayload{Outcome: "SUCCESS", Summary: "done"}); err != nil {
		t.Fatal(err)
	}
	after, err := x.Checkpoint.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(after[task.Run].Outbox) != 1 || after[task.Run].Outbox[0].MessageID != before[task.Run].Outbox[0].MessageID {
		t.Fatal("result retry changed identity")
	}
}
