package coord

import (
	"bytes"
	"context"
	"coord/agentsession"
	"coord/assignment"
	"coord/checkpoint"
	"coord/envelope"
	"coord/fsm"
	"coord/retry"
	"coord/roles"
	"coord/store"
	"coord/task"
	"coord/taskid"
	"coord/transport"
	"coord/watchdog"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
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
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close test resource: %v", err)
		}
	})
	r, _ := s.Get("run")
	if r.Status != fsm.Done || r.Result.Outcome != "SUCCESS" {
		t.Fatal("restart lost result")
	}
}

func TestAgentResultResponseIsStableAcrossRedelivery(t *testing.T) {
	command := envelope.Envelope{
		SchemaVersion: envelope.SchemaVersionAgentResult, MessageID: "agent-result-42",
		Timestamp: time.Unix(100, 0).UTC(), From: "codex:worker", To: "opencode:coord",
		Kind: envelope.Result, InReplyTo: "task-delivery", Task: "task_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Run: "run-42", Attempt: 1, TargetSHA: strings.Repeat("a", 40), Fence: 1,
		AgentResultPayload: &envelope.AgentResultPayload{AssignmentID: "assignment-42"},
	}
	committedAt := time.Unix(200, 0).UTC()
	first, err := agentResultResponse(command, "ACCEPTED", committedAt)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := agentResultResponse(command, "ACCEPTED", committedAt)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatalf("replayed response=%+v err=%v; first=%+v", replay, err, first)
	}
	if first.MessageID != command.MessageID+".result-ack" || first.ResultAckPayload.AssignmentID != "assignment-42" || first.ResultAckPayload.Disposition != "ACCEPTED" {
		t.Fatalf("response lost command binding: %+v", first)
	}
}

func TestAuthenticatedAgentCommandIngressRemainsDisabledByDefault(t *testing.T) {
	if AuthenticatedAgentIngressEnabled {
		t.Fatal("production authenticated agent command ingress must remain disabled before explicit cutover")
	}
}

type ingressTestPersistence struct{ state store.CanonicalState }

func (p *ingressTestPersistence) Lock() (func() error, error) {
	return func() error { return nil }, nil
}
func (p *ingressTestPersistence) Load() (store.CanonicalState, error)   { return p.state, nil }
func (p *ingressTestPersistence) Save(state store.CanonicalState) error { p.state = state; return nil }

type ingressTestByteClient struct{ incoming chan []byte }

func (c *ingressTestByteClient) Send(context.Context, string, []byte) error { return nil }
func (c *ingressTestByteClient) Incoming(context.Context) <-chan []byte     { return c.incoming }

type ingressTestKeyResolver struct {
	key   transport.VerificationKey
	calls *int
}

func (r ingressTestKeyResolver) ResolveVerificationKey(context.Context, string) (transport.VerificationKey, error) {
	if r.calls != nil {
		*r.calls++
	}
	return r.key, nil
}

type ingressTestTransport struct {
	*fakeTransport
	receiver    *transport.AuthenticatedAdapter
	session     string
	configured  bool
	legacyReads int
	authReads   int
	authInbox   <-chan transport.AuthenticatedFrame
	legacyRead  func(context.Context) <-chan envelope.Envelope
}

func (t *ingressTestTransport) WhoAmI() string { return t.session }
func (t *ingressTestTransport) Receive(ctx context.Context) <-chan envelope.Envelope {
	t.legacyReads++
	if t.legacyRead != nil {
		return t.legacyRead(ctx)
	}
	return t.fakeTransport.Receive(ctx)
}
func (t *ingressTestTransport) AuthenticatedIngressConfigured() bool { return t.configured }
func (t *ingressTestTransport) ReceiveAuthenticated(ctx context.Context) <-chan transport.AuthenticatedFrame {
	t.authReads++
	if t.receiver != nil {
		return t.receiver.ReceiveAuthenticated(ctx)
	}
	return t.authInbox
}

func newAuthenticatedReviewFixture(t *testing.T) (*store.CanonicalStore, *canonicalAgentEnvelope, ed25519.PrivateKey) {
	t.Helper()
	state := store.EmptyCanonicalState()
	state.SchemaVersion = 4 // First AgentSession write exercises v4-to-v5 migration.
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	now := time.Unix(100, 0).UTC()
	stagePlan := task.StagePlan{Stages: []task.Stage{task.Execution, task.Review}, PolicyVersion: task.StagePlanPolicyVersion}
	stage := task.Review
	state.Tasks[id] = task.Task{ID: id, Title: "Task", OwnerID: "owner", Revision: 3, LastEventSeq: 3,
		StagePlan: stagePlan, CurrentStage: &stage, Status: task.InProgress, Fence: 2,
		CreatedAt: now, UpdatedAt: now.Add(2 * time.Second)}
	sha := []byte(strings.Repeat("a", 40))
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x31}, ed25519.SeedSize))
	public := private.Public().(ed25519.PublicKey)
	coordPrivate := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	coordPublic := coordPrivate.Public().(ed25519.PublicKey)
	execSelection := roles.Selection{AgentID: "agent-executor", Session: "executor-session", Profile: roles.Profile{Role: roles.Executor}}
	reviewSelection := roles.Selection{AgentID: "agent-reviewer", Session: "review-session", Profile: roles.Profile{Role: roles.Reviewer}}
	state.Runs["run-executor"] = fsm.Run{Task: "task-label", TaskID: id, RunID: "run-executor", Attempt: 1, Fence: 1,
		TargetSHA: string(sha), OwnerSession: execSelection.Session, CoordinatorSession: "coord-session", Worktree: "worktree",
		Deadline: now.Add(time.Hour), AssignedAt: now, LastUpdate: now, Status: fsm.Done,
		Result: &envelope.ResultPayload{Outcome: "SUCCESS", Summary: "done"}, RequiredRole: roles.Requirement{Role: roles.Executor},
		RoleSelection: &execSelection, RoleHistory: []roles.Selection{execSelection}, AgentSessionGeneration: 1}
	state.Runs["run-review"] = fsm.Run{Task: "task-label", TaskID: id, RunID: "run-review", Attempt: 1, Fence: 2,
		TargetSHA: string(sha), OwnerSession: reviewSelection.Session, CoordinatorSession: "coord-session", Worktree: "worktree",
		Deadline: now.Add(time.Hour), AssignedAt: now, LastUpdate: now.Add(2 * time.Second), Status: fsm.Started,
		TaskMessages: []string{"task-delivery"}, RequiredRole: roles.Requirement{Role: roles.Reviewer},
		RoleSelection: &reviewSelection, RoleHistory: []roles.Selection{reviewSelection}, AgentSessionGeneration: 1}
	state.Events = []task.DomainEvent{
		{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1, Type: task.TaskCreated, ActorID: "owner", CreatedAt: now},
		{ID: task.EventID(id, 2), TaskID: id, RunID: "run-executor", EventSeq: 2, TaskRevision: 2, TaskFence: 1, Type: task.TaskOwnershipTransferred, CreatedAt: now.Add(time.Second)},
		{ID: task.EventID(id, 3), TaskID: id, RunID: "run-review", EventSeq: 3, TaskRevision: 3, TaskFence: 2, Type: task.TaskOwnershipTransferred, CreatedAt: now.Add(2 * time.Second)},
	}
	for _, binding := range []struct {
		address, agent string
		key            ed25519.PublicKey
	}{
		{"executor-session", "agent-executor", coordPublic},
		{"review-session", "agent-reviewer", public},
	} {
		state.AgentSessions[binding.address] = agentsession.Session{Address: binding.address, AgentID: binding.agent, Generation: 1, PublicKey: binding.key, RegisteredAt: now, UpdatedAt: now}
		state.AgentSessionEvents = append(state.AgentSessionEvents, agentsession.Event{ID: agentsession.EventID(binding.address, 1), Address: binding.address,
			AgentID: binding.agent, Sequence: 1, Type: agentsession.Registered, Generation: 1, PublicKey: binding.key, CreatedAt: now})
	}
	p := &ingressTestPersistence{state: state}
	canonical, err := store.NewCanonicalWithClock(p, func() time.Time { return now.Add(time.Hour / 2) })
	if err != nil {
		t.Fatal(err)
	}
	coordPrincipal := assignment.Principal{SessionID: "coord-session", AgentID: "coord-agent", Generation: 1}
	if err := canonical.RegisterAgentSession(coordPrincipal.SessionID, coordPrincipal.AgentID, coordPublic); err != nil {
		t.Fatal(err)
	}
	r := assignment.Record{AssignmentID: "ingress-assignment", State: assignment.Offered,
		Issuer: coordPrincipal.SessionID, IssuerAgentID: coordPrincipal.AgentID, IssuerGeneration: 1,
		CurrentCoordinator: coordPrincipal.SessionID, CoordinatorAgentID: coordPrincipal.AgentID, CoordinatorGeneration: 1,
		HandoffEvidence: []assignment.Handoff{}, Assignee: reviewSelection.Session, SessionID: reviewSelection.Session,
		StableAgentID: reviewSelection.AgentID, AssigneeGeneration: 1, Role: string(roles.Reviewer), SubjectSHA: string(sha),
		IssuedAt: now, ValidUntil: now.Add(2 * time.Hour),
		AdmissionBasis:     assignment.AdmissionBasis{Kind: "POOL", Reference: "test", TargetSessionID: reviewSelection.Session, TargetAgentID: reviewSelection.AgentID, Role: string(roles.Reviewer), SubjectSHA: string(sha)},
		VisibilityEvidence: assignment.VisibilityEvidence{Reference: "visible", SessionID: reviewSelection.Session, ObservedAt: now}, Revision: 1, UpdatedAt: now,
		RunBinding: &assignment.RunBinding{TaskID: id.String(), RunID: "run-review", Fence: 2, Attempt: 1}}
	if err := r.Validate(); err != nil {
		t.Fatalf("invalid integration assignment fixture: %v (%+v)", err, r)
	}
	if err := canonical.OfferAssignment(coordPrincipal, r, "offer"); err != nil {
		t.Fatal(err)
	}
	if err := canonical.ChangeAssignment(r.Target(), r.AssignmentID, 1, "ACK", "ack"); err != nil {
		t.Fatal(err)
	}
	if err := canonical.ChangeAssignment(coordPrincipal, r.AssignmentID, 2, "ACTIVATE", "activate"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := canonical.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	r = snapshot.AssignmentLedger.Records[r.AssignmentID]
	if err := canonical.RecordAssignmentWorkStarted(r.Target(), r.AssignmentID, r.Revision, r.SubjectSHA, r.Role, r.Issuer, r.CurrentCoordinator, r.CoordinatorGeneration, "started"); err != nil {
		t.Fatal(err)
	}
	return canonical, &canonicalAgentEnvelope{Task: id, Assignment: r, SHA: string(sha), Now: now.Add(time.Hour / 2), Session: reviewSelection.Session}, private
}

type canonicalAgentEnvelope struct {
	Task       taskid.ID
	Assignment assignment.Record
	SHA        string
	Now        time.Time
	Session    string
}

func TestAuthenticatedAgentResultRunsOnlyThroughExplicitHarness(t *testing.T) {
	canonical, fixture, private := newAuthenticatedReviewFixture(t)
	initial, err := canonical.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	command := envelope.Envelope{
		SchemaVersion: envelope.SchemaVersionAgentResult, MessageID: "coordinator-ingress-result",
		Timestamp: fixture.Now, From: fixture.Session, To: fixture.Assignment.CurrentCoordinator,
		Kind: envelope.Result, InReplyTo: "task-delivery", Task: fixture.Task.String(),
		Run: fixture.Assignment.RunBinding.RunID, Attempt: fixture.Assignment.RunBinding.Attempt,
		TargetSHA: fixture.SHA, Fence: fixture.Assignment.RunBinding.Fence,
		AgentResultPayload: &envelope.AgentResultPayload{
			AssignmentID: fixture.Assignment.AssignmentID, Issuer: fixture.Assignment.Issuer,
			CoordinatorGeneration: fixture.Assignment.CoordinatorGeneration,
			ExpectedRevision:      initial.Tasks[fixture.Task].Revision,
			Verdict:               "REVIEW_ACCEPTED", Summary: "review complete",
			Findings: []envelope.AgentFinding{}, InputArtifactRefs: []string{}, OutputArtifactRefs: []string{},
		},
	}
	raw, err := envelope.Sign(command, private)
	if err != nil {
		t.Fatal(err)
	}
	incoming := make(chan []byte, 1)
	incoming <- raw
	byteClient := &ingressTestByteClient{incoming: incoming}
	signed := &transport.SignedClient{Client: byteClient, Resolver: ingressTestKeyResolver{key: transport.VerificationKey{PublicKey: private.Public().(ed25519.PublicKey), Generation: 1}}}
	receiver := &transport.AuthenticatedAdapter{Client: signed, Session: command.To}
	tp := &ingressTestTransport{fakeTransport: &fakeTransport{in: make(chan envelope.Envelope)}, receiver: receiver, session: command.To}
	cfg := DefaultConfig()
	cfg.Now = func() time.Time { return fixture.Now.Add(time.Minute) }
	coordinator, err := New(tp, canonical, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var frame transport.AuthenticatedFrame
	select {
	case frame = <-receiver.ReceiveAuthenticated(ctx):
	case <-time.After(time.Second):
		t.Fatal("explicit harness did not receive authenticated frame")
	}
	if err := coordinator.handleAuthenticatedFrame(ctx, frame); err != nil {
		t.Fatal(err)
	}
	snapshot, err := canonical.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.AssignmentLedger.Records[fixture.Assignment.AssignmentID].State != assignment.Completed || snapshot.Tasks[fixture.Task].Revision != command.AgentResultPayload.ExpectedRevision+1 {
		t.Fatal("explicit authenticated ingress did not apply the canonical result")
	}
	if len(tp.sent) != 1 || tp.sent[0].Kind != envelope.ResultAck || tp.sent[0].ResultAckPayload.Disposition != "ACCEPTED" {
		t.Fatalf("durable result response not delivered: %+v", tp.sent)
	}
}

func TestRunWithReadinessKeepsLegacyReceiverWhenAuthenticatedIngressIsOff(t *testing.T) {
	f := setup(t)
	probe := &ingressTestTransport{fakeTransport: f.tp, session: "coord", configured: true, authInbox: make(chan transport.AuthenticatedFrame)}
	f.c.tp = probe
	ctx, cancel := context.WithCancel(context.Background())
	err := f.c.RunWithReadiness(ctx, cancel)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunWithReadiness error=%v", err)
	}
	if probe.legacyReads != 1 || probe.authReads != 0 {
		t.Fatalf("receiver selection legacy=%d authenticated=%d", probe.legacyReads, probe.authReads)
	}
}

func TestRunWithReadinessProcessesLegacyV1ResultAsBefore(t *testing.T) {
	f := setup(t)
	f.assign(t)
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x27}, ed25519.SeedSize))
	peer := private.Public().(ed25519.PublicKey)
	bytesIn := &ingressTestByteClient{incoming: make(chan []byte, 4)}
	result := f.reply(envelope.Result)
	result.ResultPayload = &envelope.ResultPayload{Outcome: "SUCCESS", Artifacts: []string{"blob://legacy-result"}}
	for _, e := range []envelope.Envelope{f.ack(), f.heartbeat("STARTED"), f.heartbeat("RESULT_READY"), result} {
		raw, err := envelope.Sign(e, private)
		if err != nil {
			t.Fatal(err)
		}
		bytesIn.incoming <- raw
	}
	close(bytesIn.incoming)
	resolverCalls := 0
	signed := &transport.SignedClient{
		Client: bytesIn, Peers: map[string]ed25519.PublicKey{"exec": peer},
		Resolver: ingressTestKeyResolver{key: transport.VerificationKey{PublicKey: peer, Generation: 1}, calls: &resolverCalls},
	}
	legacyAdapter := &transport.Adapter{Client: signed, Session: "coord"}
	authenticatedAdapter := &transport.AuthenticatedAdapter{Client: signed, Session: "coord"}
	probe := &ingressTestTransport{
		fakeTransport: f.tp, receiver: authenticatedAdapter, session: "coord", configured: true,
		legacyRead: legacyAdapter.Receive, authInbox: make(chan transport.AuthenticatedFrame),
	}
	f.c.tp = probe
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.c.RunWithReadiness(ctx, nil) }()
	deadline := time.After(time.Second)
	for {
		if run, ok := f.s.Get("run"); ok && run.Status == fsm.Done {
			cancel()
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatal("legacy version-1 RESULT was not applied by normal receive path")
		case <-time.After(time.Millisecond):
		}
	}
	if err := <-done; !errors.Is(err, context.Canceled) && !errors.Is(err, ErrTransportClosed) {
		t.Fatalf("RunWithReadiness error=%v", err)
	}
	if probe.legacyReads != 1 || probe.authReads != 0 || resolverCalls != 0 {
		t.Fatalf("receiver selection legacy=%d authenticated=%d AgentSession resolver=%d", probe.legacyReads, probe.authReads, resolverCalls)
	}
}

func TestSchemaFourMigrationKeepsLegacyIngressAndDoesNotEnableAgentIngress(t *testing.T) {
	f := setup(t)
	f.assign(t)
	f.handle(t, f.ack())
	f.handle(t, f.heartbeat("STARTED"))
	f.handle(t, f.heartbeat("RESULT_READY"))
	legacyRun, ok := f.s.Get("run")
	if !ok {
		t.Fatal("legacy Run fixture missing")
	}
	state := store.EmptyCanonicalState()
	state.SchemaVersion = 4
	state.Runs = map[string]fsm.Run{"run": legacyRun}
	persistence := &ingressTestPersistence{state: state}
	canonical, err := store.NewCanonicalWithClock(persistence, func() time.Time { return *f.now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = canonical.Close() })
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x5a}, ed25519.SeedSize))
	if err := canonical.RegisterAgentSession("registered-agent", "registered-agent-id", key.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	before, err := canonical.CanonicalSnapshot()
	if err != nil || before.SchemaVersion != 5 || !before.AgentSessions["registered-agent"].Active() {
		t.Fatalf("fixture did not migrate to schema 5 with active AgentSession: schema=%d err=%v", before.SchemaVersion, err)
	}
	probe := &ingressTestTransport{
		fakeTransport: &fakeTransport{in: make(chan envelope.Envelope, 1)}, session: "coord",
		configured: true, authInbox: make(chan transport.AuthenticatedFrame),
	}
	cfg := DefaultConfig()
	cfg.Now = func() time.Time { return *f.now }
	c, err := New(probe, canonical, cfg)
	if err != nil {
		t.Fatal(err)
	}
	result := f.reply(envelope.Result)
	result.ResultPayload = &envelope.ResultPayload{Outcome: "SUCCESS", Artifacts: []string{"blob://legacy"}}
	probe.in <- result
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.RunWithReadiness(ctx, nil) }()
	deadline := time.After(time.Second)
	for {
		if run, exists := canonical.Get("run"); exists && run.Status == fsm.Done {
			cancel()
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatal("legacy v1 result failed after schema 4 to 5 migration")
		case <-time.After(time.Millisecond):
		}
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunWithReadiness error=%v", err)
	}
	after, err := canonical.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.SchemaVersion != 5 || len(after.Inbox) != 0 || probe.legacyReads != 1 || probe.authReads != 0 {
		t.Fatalf("migration enabled/changed ingress: schema=%d inbox=%v legacy=%d authenticated=%d", after.SchemaVersion, after.Inbox, probe.legacyReads, probe.authReads)
	}
	if len(probe.sent) != 1 || probe.sent[0].SchemaVersion != envelope.SchemaVersionLegacyAgent || probe.sent[0].Kind != envelope.ResultAck || probe.sent[0].ResultAckPayload.AssignmentID != "" || probe.sent[0].ResultAckPayload.Disposition != "" {
		t.Fatalf("legacy receipt changed after migration: %+v", probe.sent)
	}
}

func TestLegacyPeerReceiveRejectsV2WithoutApplyingCanonicalCommand(t *testing.T) {
	canonical, fixture, private := newAuthenticatedReviewFixture(t)
	before, err := canonical.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	command := envelope.Envelope{
		SchemaVersion: envelope.SchemaVersionAgentResult, MessageID: "v2-on-legacy-peer-path",
		Timestamp: fixture.Now, From: fixture.Session, To: fixture.Assignment.CurrentCoordinator,
		Kind: envelope.Result, InReplyTo: "task-delivery", Task: fixture.Task.String(),
		Run: fixture.Assignment.RunBinding.RunID, Attempt: fixture.Assignment.RunBinding.Attempt,
		TargetSHA: fixture.SHA, Fence: fixture.Assignment.RunBinding.Fence,
		AgentResultPayload: &envelope.AgentResultPayload{
			AssignmentID: fixture.Assignment.AssignmentID, Issuer: fixture.Assignment.Issuer,
			CoordinatorGeneration: fixture.Assignment.CoordinatorGeneration,
			ExpectedRevision:      before.Tasks[fixture.Task].Revision,
			Verdict:               "REVIEW_ACCEPTED", Summary: "review complete",
			Findings: []envelope.AgentFinding{}, InputArtifactRefs: []string{}, OutputArtifactRefs: []string{},
		},
	}
	raw, err := envelope.Sign(command, private)
	if err != nil {
		t.Fatal(err)
	}
	incoming := make(chan []byte, 1)
	incoming <- raw
	close(incoming)
	resolverCalls := 0
	public := private.Public().(ed25519.PublicKey)
	signed := &transport.SignedClient{
		Client: &ingressTestByteClient{incoming: incoming},
		Peers:  map[string]ed25519.PublicKey{fixture.Session: public},
		Resolver: ingressTestKeyResolver{
			key: transport.VerificationKey{PublicKey: public, Generation: 1}, calls: &resolverCalls,
		},
	}
	legacyAdapter := &transport.Adapter{Client: signed, Session: fixture.Assignment.CurrentCoordinator}
	authenticatedAdapter := &transport.AuthenticatedAdapter{Client: signed, Session: fixture.Assignment.CurrentCoordinator}
	tp := &ingressTestTransport{
		fakeTransport: &fakeTransport{in: make(chan envelope.Envelope)},
		receiver:      authenticatedAdapter, session: fixture.Assignment.CurrentCoordinator,
		configured: true, legacyRead: legacyAdapter.Receive,
	}
	cfg := DefaultConfig()
	cfg.Now = func() time.Time { return fixture.Now.Add(time.Minute) }
	c, err := New(tp, canonical, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Handle(ctx, command); !errors.Is(err, ErrUnsupportedLegacyEnvelopeSchema) {
		t.Fatalf("v2 envelope direct legacy path error=%v", err)
	}
	if err := c.RunWithReadiness(ctx, nil); !errors.Is(err, ErrTransportClosed) {
		t.Fatalf("legacy peer stream error=%v", err)
	}
	after, err := canonical.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if resolverCalls != 0 || tp.legacyReads != 1 || tp.authReads != 0 {
		t.Fatalf("routing called resolver=%d legacy=%d authenticated=%d", resolverCalls, tp.legacyReads, tp.authReads)
	}
	c.Metrics.mu.Lock()
	rejected := c.Metrics.counters["rejected_messages_total"]
	c.Metrics.mu.Unlock()
	if rejected != 1 {
		t.Fatalf("legacy peer-signed v2 envelope was not explicitly rejected: rejected=%d", rejected)
	}
	if !reflect.DeepEqual(before, after) || len(after.Inbox) != 0 || after.AssignmentLedger.Records[fixture.Assignment.AssignmentID].State != assignment.Active {
		t.Fatal("v2 frame on default peer path changed canonical state")
	}
}

func TestAuthenticatedIngressRejectsLegacyV1Result(t *testing.T) {
	f := setup(t)
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x19}, ed25519.SeedSize))
	e := envelope.Envelope{
		SchemaVersion: envelope.SchemaVersionLegacyAgent, MessageID: "legacy-authenticated-result",
		Timestamp: *f.now, From: "codex:worker", To: "coord", Kind: envelope.Result, InReplyTo: "task-delivery",
		Task: "task_01ARZ3NDEKTSV4RRFFQ69G5FAV", Run: "run-1", Attempt: 1,
		TargetSHA: strings.Repeat("a", 40), Fence: 1,
		ResultPayload: &envelope.ResultPayload{Outcome: "SUCCESS", Summary: "legacy result"},
	}
	raw, err := envelope.Sign(e, private)
	if err != nil {
		t.Fatal(err)
	}
	byteClient := &ingressTestByteClient{incoming: make(chan []byte, 1)}
	byteClient.incoming <- raw
	signed := &transport.SignedClient{Client: byteClient, Resolver: ingressTestKeyResolver{key: transport.VerificationKey{PublicKey: private.Public().(ed25519.PublicKey), Generation: 1}}}
	adapter := &transport.AuthenticatedAdapter{Client: signed, Session: "coord"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var frame transport.AuthenticatedFrame
	select {
	case frame = <-adapter.ReceiveAuthenticated(ctx):
	case <-time.After(time.Second):
		t.Fatal("authenticated adapter did not verify legacy test frame")
	}
	f.c.tp = &ingressTestTransport{fakeTransport: f.tp, session: "coord"}
	if err := f.c.handleAuthenticatedFrame(ctx, frame); !errors.Is(err, ErrLegacyEnvelopeOnAuthenticatedIngress) {
		t.Fatalf("legacy v1 RESULT authenticated-ingress error=%v", err)
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
	if next.MessageID == old.MessageID || next.Run != old.Run || next.Attempt != old.Attempt || next.Fence != old.Fence || next.To != old.To {
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
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close test resource: %v", err)
		}
	})
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
	t.Cleanup(server.Close)
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
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
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

// TestCanonicalExecutorResultGateFailsClosedWithoutCanonicalStore verifies R117/R124:
// a task-scoped Run's RESULT envelope is rejected when the coordinator does not
// hold a CanonicalStore — no fallback to the legacy Apply path is attempted.
func TestCanonicalExecutorResultGateFailsClosedWithoutCanonicalStore(t *testing.T) {
	f := setup(t) // legacy store.Store, not CanonicalStore
	f.assign(t)

	// Inject a TaskID onto the run so the canonical RESULT gate activates.
	taskID := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err := f.s.Update(func(runs map[string]fsm.Run) error {
		r := runs["run"]
		r.TaskID = taskID
		runs["run"] = r
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	e := f.reply(envelope.Result)
	e.ResultPayload = &envelope.ResultPayload{Outcome: "SUCCESS", Summary: "done"}
	err := f.c.Handle(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "fail-closed") {
		t.Fatalf("task-scoped RESULT on legacy store was not rejected fail-closed: err=%v", err)
	}
}
