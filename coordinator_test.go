package coord

import (
	"context"
	"coord/checkpoint"
	"coord/envelope"
	"coord/fsm"
	"coord/retry"
	"coord/store"
	"coord/watchdog"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeTransport struct {
	sent []envelope.Envelope
	in   chan envelope.Envelope
	fail bool
}

func (f *fakeTransport) WhoAmI() string                                   { return "coord" }
func (f *fakeTransport) Receive(context.Context) <-chan envelope.Envelope { return f.in }
func (f *fakeTransport) Send(_ context.Context, _ string, e envelope.Envelope) error {
	if f.fail {
		return errors.New("unavailable")
	}
	f.sent = append(f.sent, e)
	return nil
}

type fixture struct {
	c    *Coordinator
	tp   *fakeTransport
	s    *store.Store
	now  *time.Time
	path string
}

func setup(t *testing.T) fixture {
	t.Helper()
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "runs.json")
	s, err := store.New(checkpoint.File{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	tp := &fakeTransport{in: make(chan envelope.Envelope, 10)}
	cfg := DefaultConfig()
	cfg.Now = func() time.Time { return now }
	cfg.Retry = retry.Policy{BaseDelay: time.Second, MaxDelay: time.Second, Multiplier: 2, MaxRetries: 2}
	cfg.ProbeTimeout = time.Second
	c, err := New(tp, s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{c, tp, s, &now, path}
}
func (f fixture) assign(t *testing.T) {
	t.Helper()
	r := fsm.Run{Task: "CF-009", RunID: "run", Attempt: 3, Fence: 1, TargetSHA: strings.Repeat("a", 40), OwnerSession: "exec", Worktree: "/worktree", Deadline: f.now.Add(time.Hour)}
	if err := f.c.Assign(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}
func (f fixture) reply(k envelope.Kind) envelope.Envelope {
	r, _ := f.s.Get("run")
	return envelope.Envelope{SchemaVersion: 1, MessageID: envelope.NewID(), InReplyTo: r.TaskMessages[0], Timestamp: *f.now, From: r.OwnerSession, To: "coord", Kind: k, Task: r.Task, Run: r.RunID, Attempt: r.Attempt, TargetSHA: r.TargetSHA, Fence: r.Fence}
}
func (f fixture) ack() envelope.Envelope {
	e := f.reply(envelope.Ack)
	e.AckPayload = &envelope.AckPayload{State: "ACCEPTED", NextUpdateBy: f.now.Add(time.Minute)}
	return e
}
func (f fixture) heartbeat(state string) envelope.Envelope {
	e := f.reply(envelope.Heartbeat)
	e.HeartbeatPayload = &envelope.HeartbeatPayload{State: state, NextUpdateBy: f.now.Add(time.Minute), ProgressPct: 42, Blocker: "dependency"}
	return e
}
func (f fixture) handle(t *testing.T, e envelope.Envelope) {
	t.Helper()
	if err := f.c.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}
func (f fixture) status(t *testing.T, want fsm.Status) {
	t.Helper()
	r, _ := f.s.Get("run")
	if r.Status != want {
		t.Fatalf("status %s, want %s", r.Status, want)
	}
}
func TestFullLifecycleAndRestart(t *testing.T) {
	f := setup(t)
	f.assign(t)
	f.handle(t, f.ack())
	f.status(t, fsm.Accepted)
	for _, state := range []string{"STARTED", "BLOCKED", "STARTED", "RESULT_READY", "RESULT_READY"} {
		f.handle(t, f.heartbeat(state))
	}
	e := f.reply(envelope.Result)
	e.ResultPayload = &envelope.ResultPayload{Outcome: "SUCCESS", Artifacts: []string{"blob://result"}}
	f.handle(t, e)
	f.handle(t, e)
	f.status(t, fsm.Done)
	_ = f.s.Close()
	s, err := store.New(checkpoint.File{Path: f.path})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, _ := s.Get("run")
	if r.Status != fsm.Done || r.Result.Outcome != "SUCCESS" {
		t.Fatal("restart lost result")
	}
}
func TestIdentityCorrelationAndOrder(t *testing.T) {
	f := setup(t)
	f.assign(t)
	if err := f.c.Handle(context.Background(), f.heartbeat("STARTED")); err == nil {
		t.Fatal("heartbeat before ACK accepted")
	}
	f.status(t, fsm.Pending)
	for _, field := range []string{"from", "attempt", "sha", "fence", "task", "reply"} {
		t.Run(field, func(t *testing.T) {
			e := f.ack()
			switch field {
			case "from":
				e.From = "other"
			case "attempt":
				e.Attempt++
			case "sha":
				e.TargetSHA = strings.Repeat("b", 40)
			case "fence":
				e.Fence++
			case "task":
				e.Task = "other"
			case "reply":
				e.InReplyTo = "unknown"
			}
			if err := f.c.Handle(context.Background(), e); err == nil {
				t.Fatal("invalid response accepted")
			}
			f.status(t, fsm.Pending)
		})
	}
	if f.tp.sent[len(f.tp.sent)-1].Kind != envelope.Abort {
		t.Fatal("missing ABORT")
	}
	f.handle(t, f.ack())
	e := f.reply(envelope.Result)
	e.ResultPayload = &envelope.ResultPayload{Outcome: "SUCCESS"}
	if err := f.c.Handle(context.Background(), e); err == nil {
		t.Fatal("early RESULT accepted")
	}
}
func TestAckRetryPreservesAttemptAndDeduplicates(t *testing.T) {
	f := setup(t)
	f.assign(t)
	old := f.tp.sent[0]
	*f.now = f.now.Add(31 * time.Second)
	if err := f.c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.status(t, fsm.Stalled)
	next := f.tp.sent[1]
	if next.MessageID == old.MessageID || next.Attempt != old.Attempt || next.Fence != old.Fence || next.To != old.To {
		t.Fatal("retry changed ownership or reused message ID")
	}
	e := f.ack()
	f.handle(t, e)
	f.handle(t, e)
	f.status(t, fsm.Accepted)
}
func TestHeartbeatMissPongDoesNotAdvanceFSM(t *testing.T) {
	f := setup(t)
	f.assign(t)
	f.handle(t, f.ack())
	f.handle(t, f.heartbeat("STARTED"))
	*f.now = f.now.Add(61 * time.Second)
	if err := f.c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	ping := f.tp.sent[len(f.tp.sent)-1]
	if ping.Kind != envelope.Ping {
		t.Fatal("missing probe")
	}
	e := f.reply(envelope.Pong)
	e.InReplyTo = ping.MessageID
	e.PongPayload = &envelope.PongPayload{Nonce: ping.PingPayload.Nonce, State: "BUSY"}
	f.handle(t, e)
	f.status(t, fsm.Stalled)
	f.handle(t, f.heartbeat("STARTED"))
	f.status(t, fsm.Started)
}
func TestRecoveryCASAndOldOwnerRejected(t *testing.T) {
	f := setup(t)
	f.assign(t)
	old := f.ack()
	for _, d := range []time.Duration{31 * time.Second, time.Second, time.Second} {
		*f.now = f.now.Add(d)
		if err := f.c.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	r, _ := f.s.Get("run")
	if !r.RecoveryNeeded {
		t.Fatal("retry exhaustion not handed to watchdog")
	}
	if err := f.c.ClaimRecovery("run", 1, "w1"); err != nil {
		t.Fatal(err)
	}
	if err := f.c.ClaimRecovery("run", 1, "w2"); err == nil {
		t.Fatal("double claim")
	}
	if err := f.c.RecoverClaimed(context.Background(), "run", "replacement", 1, "w2"); err == nil {
		t.Fatal("wrong claimant accepted")
	}
	if err := f.c.RecoverClaimed(context.Background(), "run", "replacement", 1, "w1"); err != nil {
		t.Fatal(err)
	}
	r, _ = f.s.Get("run")
	if r.Fence != 2 || r.Attempt != 3 || r.OwnerSession != "replacement" {
		t.Fatal("bad recovery identity")
	}
	if !errors.Is(f.c.Handle(context.Background(), old), ErrStale) {
		t.Fatal("old owner accepted")
	}
	f.handle(t, f.ack())
}
func TestOutboxReplayAfterRestart(t *testing.T) {
	f := setup(t)
	f.tp.fail = true
	f.assign(t)
	r, _ := f.s.Get("run")
	if len(r.Outbox) != 1 {
		t.Fatal("lost outbox")
	}
	_ = f.s.Close()
	s, err := store.New(checkpoint.File{Path: f.path})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := New(f.tp, s, f.c.cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.tp.fail = false
	*f.now = f.now.Add(time.Second)
	if err = c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.tp.sent) != 1 || f.tp.sent[0].MessageID != r.Outbox[0].MessageID {
		t.Fatal("outbox not replayed")
	}
}

type brokenPersistence struct{}

func (brokenPersistence) Load() (map[string]fsm.Run, error) { return map[string]fsm.Run{}, nil }
func (brokenPersistence) Save(map[string]fsm.Run) error     { return errors.New("disk full") }
func TestPersistenceFailureNeverSends(t *testing.T) {
	f := setup(t)
	s, _ := store.New(brokenPersistence{})
	f.c.store = s
	r := fsm.Run{Task: "task", RunID: "run", Attempt: 1, Fence: 1, TargetSHA: strings.Repeat("a", 40), OwnerSession: "exec", Worktree: "wt", Deadline: f.now.Add(time.Hour)}
	err := f.c.Assign(context.Background(), r)
	if !errors.Is(err, store.ErrPersistence) || len(f.tp.sent) != 0 {
		t.Fatal("sent unpersisted assignment")
	}
	if _, ok := s.Get("run"); ok {
		t.Fatal("published failed commit")
	}
}
func TestHTTPWatchdogControl(t *testing.T) {
	f := setup(t)
	f.assign(t)
	*f.now = f.now.Add(31 * time.Second)
	for i := 0; i < 3; i++ {
		if err := f.c.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		*f.now = f.now.Add(time.Second)
	}
	server := httptest.NewServer(f.c.ControlHandler("token"))
	defer server.Close()
	h := watchdog.HTTPControl{URL: server.URL, Token: "token", ClaimID: "w", Client: server.Client()}
	runs, err := h.Candidates(context.Background())
	if err != nil || len(runs) != 1 {
		t.Fatalf("candidates: %v %v", runs, err)
	}
	ok, err := h.Claim(context.Background(), "run", 1)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = h.Transfer(context.Background(), "run", "new", 1); err != nil {
		t.Fatal(err)
	}
	if err = h.Release(context.Background(), "run", 1); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", server.URL+"/runs", nil)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("unauthorized access")
	}
}
func TestRunTransportClosure(t *testing.T) {
	f := setup(t)
	close(f.tp.in)
	if !errors.Is(f.c.Run(context.Background()), ErrTransportClosed) {
		t.Fatal("closed transport not detected")
	}
}
