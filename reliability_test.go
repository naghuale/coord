package coord

import (
	"context"
	"coord/envelope"
	"coord/fsm"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAbsoluteDeadlineEnforcedWithoutTick(t *testing.T) {
	f := setup(t)
	f.assign(t)
	f.handle(t, f.ack())
	f.handle(t, f.heartbeat("RESULT_READY"))
	*f.now = f.now.Add(2 * time.Hour)
	e := f.reply(envelope.Result)
	e.ResultPayload = &envelope.ResultPayload{Outcome: "SUCCESS"}
	if err := f.c.Handle(context.Background(), e); err == nil {
		t.Fatal("late result accepted")
	}
	f.status(t, fsm.Failed)
}

func TestDelayedHeartbeatCannotRegressProgress(t *testing.T) {
	f := setup(t)
	f.assign(t)
	f.handle(t, f.ack())
	newer := f.heartbeat("STARTED")
	newer.Timestamp = f.now.Add(10 * time.Second)
	newer.HeartbeatPayload.ProgressPct = 70
	*f.now = f.now.Add(20 * time.Second)
	f.handle(t, newer)
	older := f.heartbeat("BLOCKED")
	older.Timestamp = newer.Timestamp.Add(-time.Second)
	older.HeartbeatPayload.ProgressPct = 10
	before, _ := f.s.Get("run")
	if err := f.c.Handle(context.Background(), older); err == nil {
		t.Fatal("delayed heartbeat accepted")
	}
	after, _ := f.s.Get("run")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("delayed heartbeat changed run")
	}
}

func TestMissingSeenMapDoesNotPanic(t *testing.T) {
	f := setup(t)
	f.assign(t)
	r, _ := f.s.Get("run")
	r.Seen = nil
	if err := f.c.put(r); err != nil {
		t.Fatal(err)
	}
	f.handle(t, f.ack())
	f.status(t, fsm.Accepted)
}

func TestRepeatedAckDoesNotResetStartedRun(t *testing.T) {
	f := setup(t)
	f.assign(t)
	f.handle(t, f.ack())
	f.handle(t, f.heartbeat("STARTED"))
	before, _ := f.s.Get("run")
	f.handle(t, f.ack())
	after, _ := f.s.Get("run")
	if after.Status != before.Status || after.NextUpdateBy != before.NextUpdateBy || after.ProgressPct != before.ProgressPct {
		t.Fatal("repeat ACK changed application state")
	}
}

func TestControlRejectsTrailingJSONBeforeMutation(t *testing.T) {
	f := setup(t)
	f.assign(t)
	r, _ := f.s.Get("run")
	r.Status = fsm.Stalled
	r.RecoveryNeeded = true
	if err := f.c.put(r); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/claim", strings.NewReader(`{"run":"run","fence":1,"claim":"owner"} {}`))
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	f.c.ControlHandler("token").ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", response.Code)
	}
	r, _ = f.s.Get("run")
	if r.RecoveryClaim != "" {
		t.Fatal("invalid request mutated run")
	}
}

func TestRestartRejectsUnknownCanonicalStatus(t *testing.T) {
	f := setup(t)
	f.assign(t)
	r, _ := f.s.Get("run")
	r.Status = "CORRUPT"
	if err := f.c.put(r); err != nil {
		t.Fatal(err)
	}
	if _, err := New(f.tp, f.s, f.c.cfg); err == nil {
		t.Fatal("corrupt canonical state accepted")
	}
}

func TestCommittedResultReceiptSurvivesTransportFailureAndRestart(t *testing.T) {
	f := setup(t)
	f.assign(t)
	f.handle(t, f.ack())
	f.handle(t, f.heartbeat("RESULT_READY"))
	result := f.reply(envelope.Result)
	result.ResultPayload = &envelope.ResultPayload{Outcome: "SUCCESS", Summary: "done"}
	f.tp.fail = true
	if err := f.c.Handle(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	r, _ := f.s.Get("run")
	if r.Status != fsm.Done || len(r.Outbox) != 1 || r.Outbox[0].Kind != envelope.Kind("RESULT_ACK") || r.Outbox[0].InReplyTo != result.MessageID {
		t.Fatalf("receipt not durable: %+v", r)
	}
	receiptID := r.Outbox[0].MessageID
	restored, err := New(f.tp, f.s, f.c.cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.tp.fail = false
	*f.now = f.now.Add(time.Second)
	if err = restored.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	last := f.tp.sent[len(f.tp.sent)-1]
	if last.MessageID != receiptID {
		t.Fatal("terminal receipt not replayed")
	}
	raw, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = envelope.Decode(raw); err != nil {
		t.Fatal(err)
	}
	if err = restored.Handle(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	if f.tp.sent[len(f.tp.sent)-1].InReplyTo != result.MessageID {
		t.Fatal("duplicate result has no receipt")
	}
}

func TestNewReceiptDoesNotInheritEarlierSendBackoff(t *testing.T) {
	f := setup(t)
	f.assign(t)
	f.handle(t, f.ack())
	f.handle(t, f.heartbeat("RESULT_READY"))
	r, _ := f.s.Get("run")
	r.SendRetryAt = f.now.Add(time.Hour)
	r.SendRetries = 3
	if err := f.c.put(r); err != nil {
		t.Fatal(err)
	}
	e := f.reply(envelope.Result)
	e.ResultPayload = &envelope.ResultPayload{Outcome: "SUCCESS"}
	f.handle(t, e)
	last := f.tp.sent[len(f.tp.sent)-1]
	if last.Kind != envelope.ResultAck || last.InReplyTo != e.MessageID {
		t.Fatal("receipt inherited unrelated transport backoff")
	}
}
