//go:build unix

package main

import (
	"bytes"
	"context"
	"coord"
	"coord/agentsession"
	"coord/assignment"
	"coord/checkpoint"
	"coord/envelope"
	"coord/fsm"
	"coord/internal/coorddconfig"
	"coord/internal/externalaction"
	"coord/internal/orchestration"
	"coord/roles"
	"coord/store"
	"coord/task"
	"coord/taskid"
	"coord/transport"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const disabledSubject = "0123456789012345678901234567890123456789"

type dormantFixture struct {
	s    *store.CanonicalStore
	file checkpoint.CanonicalFile
	now  time.Time
}

func dormantState(t *testing.T, path string) dormantFixture {
	t.Helper()
	now := time.Now().UTC().Add(-time.Minute)
	state := store.EmptyCanonicalState()
	for i, suffix := range []string{"V", "W"} {
		id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FA" + suffix)
		session := []string{"session-workflow", "session-ordinary"}[i]
		runID := []string{"workflow-run", "ordinary-run"}[i]
		agent := "agent-" + session
		created := now.Add(-time.Minute)
		stage := task.Execution
		state.Tasks[id] = task.Task{ID: id, Title: runID, OwnerID: "owner", Revision: 2, LastEventSeq: 2, StagePlan: task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion}, CurrentStage: &stage, Status: task.InProgress, Fence: 1, CreatedAt: created, UpdatedAt: now}
		key := ed25519.PublicKey(strings.Repeat(string(rune('a'+i)), 32))
		state.AgentSessions[session] = agentsession.Session{Address: session, AgentID: agent, Generation: 1, PublicKey: key, RegisteredAt: now, UpdatedAt: now}
		state.AgentSessionEvents = append(state.AgentSessionEvents, agentsession.Event{ID: agentsession.EventID(session, 1), Address: session, AgentID: agent, Sequence: 1, Type: agentsession.Registered, Generation: 1, PublicKey: key, CreatedAt: now})
		sel := roles.Selection{AgentID: agent, Session: session, Profile: roles.Profile{Role: roles.Executor}}
		state.Runs[runID] = fsm.Run{Task: runID, TaskID: id, RunID: runID, Attempt: 1, TargetSHA: disabledSubject, OwnerSession: session, AgentSessionGeneration: 1, Fence: 1, Worktree: "fixture-worktree", Deadline: now.Add(8 * time.Hour), NextUpdateBy: now.Add(time.Hour), Status: fsm.Started, RequiredRole: roles.Requirement{Role: roles.Executor}, RoleSelection: &sel, RoleHistory: []roles.Selection{sel}}
		state.Events = append(state.Events, task.DomainEvent{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1, Type: task.TaskCreated, ActorID: "owner", CreatedAt: created}, task.DomainEvent{ID: task.EventID(id, 2), TaskID: id, RunID: runID, EventSeq: 2, TaskRevision: 2, TaskFence: 1, Type: task.TaskOwnershipTransferred, CreatedAt: now})
	}
	file := checkpoint.CanonicalFile{Path: path}
	if err := file.Save(state); err != nil {
		t.Fatal(err)
	}
	s, err := store.NewCanonicalWithClock(file, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	binding := externalaction.Binding{ProjectID: "coord", TaskID: "task_01ARZ3NDEKTSV4RRFFQ69G5FAV", RunID: "workflow-run", AssignmentID: "assignment-workflow", SessionID: "session-workflow", SubjectSHA: disabledSubject, Generation: 1, Fence: 1}
	for _, id := range []string{"pending-delivery", "started-delivery"} {
		a := externalaction.New(id, externalaction.Deliver, binding, "", json.RawMessage(`{"assignment":"assignment-workflow"}`), now)
		if err := s.EnqueueAction(a); err != nil {
			t.Fatal(err)
		}
		if id == "started-delivery" {
			claim, err := s.ClaimAction(id, "old-coordd", now, 30*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.StartAction(id, "old-coordd", claim.Claim.Epoch, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dormantFixture{s, file, now}
}

func canonicalBytes(t *testing.T, s *store.CanonicalStore) []byte {
	t.Helper()
	snap, err := s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The enabled path exists only in this _test.go harness. No production config,
// environment variable, flag or caller supplies an enablement value.
func enabledOrchestrationHarness(c *coord.Coordinator, s *store.CanonicalStore, a orchestration.Adapter, now time.Time) {
	c.Workflow = orchestration.New(s, a, "explicit-test-harness", func() time.Time { return now })
}

type disabledAdapterProbe struct{ calls int }

func (p *disabledAdapterProbe) SourceID() string { return "issue96-test-adapter" }
func (p *disabledAdapterProbe) Execute(context.Context, externalaction.Action) (externalaction.Result, error) {
	p.calls++
	return externalaction.Result{Outcome: "DELIVERED", Evidence: "test-harness"}, nil
}
func (p *disabledAdapterProbe) Observe(context.Context, externalaction.Action) (externalaction.Result, error) {
	p.calls++
	return externalaction.Result{}, errors.New("test-harness observation")
}
func (p *disabledAdapterProbe) Acknowledge(context.Context, externalaction.Receipt) error {
	p.calls++
	return nil
}

type routingProbeTransport struct {
	idleTransport
	recipientChecks int
}

func (p *routingProbeTransport) WhoAmI() string { p.recipientChecks++; return p.session }

func disabledCoordinator(t *testing.T, f dormantFixture) (*coord.Coordinator, *routingProbeTransport) {
	t.Helper()
	tp := &routingProbeTransport{idleTransport: idleTransport{session: "opencode-123", in: make(chan envelope.Envelope)}}
	cfg := coord.DefaultConfig()
	cfg.Now = func() time.Time { return f.now.Add(time.Minute) }
	c, err := coord.New(tp, f.s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = configureOrchestration(c, f.s, coorddconfig.Config{}, io.Discard); err != nil {
		t.Fatal(err)
	}
	return c, tp
}

func TestOrchestrationDisabledTickAndRecoveryAreReadOnly(t *testing.T) {
	f := dormantState(t, filepath.Join(t.TempDir(), "canonical.json"))
	c, _ := disabledCoordinator(t, f)
	d, ok := c.Workflow.(disabledOrchestration)
	if !ok {
		t.Fatalf("production composition enabled orchestration: %T", c.Workflow)
	}
	probe := &disabledAdapterProbe{}
	d.runtime.Adapter = probe
	for _, op := range []struct {
		name string
		run  func(context.Context) error
	}{{"recovery", c.RecoverWorkflow}, {"tick", c.Tick}} {
		t.Run(op.name, func(t *testing.T) {
			before := canonicalBytes(t, f.s)
			var operationErr error
			for repeat := 0; repeat < 3; repeat++ {
				if operationErr = op.run(context.Background()); operationErr != nil {
					break
				}
				if operationErr = d.Recover(context.Background()); operationErr != nil {
					break
				}
			}
			if probe.calls != 0 {
				t.Fatalf("disabled %s executed adapter: %d", op.name, probe.calls)
			}
			if !bytes.Equal(before, canonicalBytes(t, f.s)) {
				t.Fatalf("disabled %s mutated canonical state", op.name)
			}
			if operationErr != nil {
				t.Fatalf("disabled %s returned unexpected error: %v", op.name, operationErr)
			}
		})
	}
	if err := c.Workflow.Completion(); err != nil {
		t.Fatalf("dormant actions blocked unrelated service readiness: %v", err)
	}
}

func TestOrchestrationDisabledKeepsRunFencesAndOrdinaryRuns(t *testing.T) {
	f := dormantState(t, filepath.Join(t.TempDir(), "canonical.json"))
	c, tp := disabledCoordinator(t, f)
	before := canonicalBytes(t, f.s)
	msg := envelope.Envelope{SchemaVersion: 1, MessageID: "stale-workflow-result", InReplyTo: "assignment-workflow", Timestamp: f.now, From: "session-workflow", To: tp.session, Kind: envelope.Result, Task: "workflow-run", Run: "workflow-run", Attempt: 1, TargetSHA: disabledSubject, Fence: 1, ResultPayload: &envelope.ResultPayload{Outcome: "SUCCESS"}}
	routeBefore := tp.recipientChecks
	if err := c.Handle(context.Background(), msg); err == nil || !strings.Contains(err.Error(), "orchestrated Run requires canonical command boundary") {
		t.Fatalf("disabled facade allowed legacy handling: %v", err)
	}
	if !bytes.Equal(before, canonicalBytes(t, f.s)) {
		t.Fatal("disabled facade allowed legacy mutation")
	}
	if tp.recipientChecks != routeBefore {
		t.Fatal("legacy handler received orchestration Run")
	}
	if !c.Workflow.OwnsRun("workflow-run") {
		t.Fatal("disabled facade dropped orchestration Run fence")
	}
	for _, id := range []string{"ordinary-run", "unknown-run"} {
		if c.Workflow.OwnsRun(id) {
			t.Fatalf("disabled facade fenced non-orchestration Run %s", id)
		}
	}

	// Ordinary Run messages still reach the existing canonical command guard;
	// disabling orchestration must not misclassify them as workflow messages.
	msg.Run, msg.Task, msg.From, msg.MessageID = "ordinary-run", "ordinary-run", "session-ordinary", "ordinary-result"
	if err := c.Handle(context.Background(), msg); err != nil && strings.Contains(err.Error(), "orchestrated Run requires canonical command boundary") {
		t.Fatalf("disabled facade blocked ordinary Run handling: %v", err)
	}
	if tp.recipientChecks == routeBefore {
		t.Fatal("legacy handler did not receive ordinary Run")
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}

}

func TestOrchestrationExplicitTestHarnessStillExecutes(t *testing.T) {
	f := dormantState(t, filepath.Join(t.TempDir(), "canonical.json"))
	c, _ := disabledCoordinator(t, f)
	before := canonicalBytes(t, f.s)
	probe := &disabledAdapterProbe{}
	enabledOrchestrationHarness(c, f.s, probe, f.now.Add(time.Minute))
	if err := c.RecoverWorkflow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if probe.calls == 0 || bytes.Equal(before, canonicalBytes(t, f.s)) {
		t.Fatal("explicit test harness did not execute orchestration")
	}
}

// Receive is acquired only after production composition and recovery. Cancel
// there, so real startup/restart tests need no sleeps or live agent process.
type dormantStartupTransport struct {
	cancel   context.CancelFunc
	in       chan envelope.Envelope
	received bool
}

func (p *dormantStartupTransport) WhoAmI() string { return "opencode-123" }
func (p *dormantStartupTransport) Send(context.Context, string, envelope.Envelope) error {
	return errors.New("unexpected startup delivery")
}
func (p *dormantStartupTransport) Receive(context.Context) <-chan envelope.Envelope {
	p.received = true
	p.cancel()
	return p.in
}

func TestOrchestrationNormalStartAndRestartPreserveSnapshot(t *testing.T) {
	// Environment values cannot opt into execution. Fixture requests are
	// separately rejected before locks/transport in production.
	t.Setenv("COORD_ORCHESTRATION_ENABLED", "true")
	t.Setenv("ORCHESTRATION_ENABLED", "true")
	cfg := testConfig(t)

	if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	f := dormantState(t, filepath.Join(cfg.StateDir, "canonical.json"))
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.file.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, pass := range []string{"normal-start", "restart"} {
		t.Run(pass, func(t *testing.T) {
			projectionPath := filepath.Join(cfg.StateDir, "operations", "current.json")
			preservedProjection := []byte(`{"sourceRevision":0,"generatorVersion":"pre-disable-evidence","files":{}}`)
			if pass == "restart" {
				if err := os.MkdirAll(filepath.Dir(projectionPath), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(projectionPath, preservedProjection, 0600); err != nil {
					t.Fatal(err)
				}
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tp := &dormantStartupTransport{cancel: cancel, in: make(chan envelope.Envelope)}
			err := runWithWriter(ctx, cfg, func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
				return tp, func() error { return nil }, nil
			}, io.Discard)
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if !tp.received {
				t.Fatal("normal daemon startup did not reach transport receive")
			}
			after, err := os.ReadFile(f.file.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("%s changed preserved orchestration snapshot/events", pass)
			}
			projection, projectionErr := os.ReadFile(projectionPath)
			if pass == "normal-start" {
				if !os.IsNotExist(projectionErr) {
					t.Fatalf("disabled production published projection: %v", projectionErr)
				}
			} else if projectionErr != nil || !bytes.Equal(projection, preservedProjection) {
				t.Fatalf("disabled restart changed existing projection: %v", projectionErr)
			}

		})
	}
}

func forbiddenFixtureConfig(t *testing.T, cfg coorddconfig.Config) coorddconfig.Config {
	t.Helper()
	target := externalaction.GitHubTarget{Repository: "naghuale/coord", PullRequestNumber: 64, BaseBranch: "main", HeadBranch: "feature/issue96", ExpectedHead: disabledSubject, ExpectedState: "OPEN"}
	payload := externalaction.GitHubActionPayload{Operation: externalaction.ObservePullRequest, ProjectID: "coord", OriginAssignmentID: "fixture", ScopeDigest: strings.Repeat("a", 64), Target: target}
	a, err := externalaction.NewGitHub("fixture-observation", payload, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	payload, _ = a.GitHubPayload()
	cfg.GitHubObservationFixtures = []externalaction.GitHubFixture{{Operation: externalaction.ObservePullRequest, Target: target, ExternalID: "pr-64", State: "OPEN", Marker: &payload.Marker, PayloadDigest: payload.Marker.PayloadDigest}}
	return cfg
}

func TestOrchestrationNoProductionEnablementConfig(t *testing.T) {
	if orchestrationEnabled {
		t.Fatal("production orchestration enabled before acceptance")
	}
	cfg := testConfig(t)
	t.Run("fixture-request", func(t *testing.T) {
		requested := forbiddenFixtureConfig(t, cfg)
		path := writeTestConfig(t, requested)
		called := false
		var stderr bytes.Buffer
		code := mainRunWithWriter([]string{"-config", path}, func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
			called = true
			return nil, nil, errors.New("unexpected transport")
		}, &stderr)
		if code != 2 || called || !strings.Contains(stderr.String(), "DISABLE_PENDING_ACCEPTANCE") {
			t.Fatalf("production fixture request accepted: code=%d called=%v stderr=%s", code, called, stderr.String())
		}
		if _, err := os.Stat(cfg.StateDir); !os.IsNotExist(err) {
			t.Fatalf("production fixture request touched namespace: %v", err)
		}
	})
	t.Run("cli-request", func(t *testing.T) {
		path := writeTestConfig(t, cfg)
		called := false
		var stderr bytes.Buffer
		code := mainRunWithWriter([]string{"-config", path, "--orchestration-enabled", "true"}, func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
			called = true
			return nil, nil, errors.New("unexpected transport")
		}, &stderr)
		if code != 2 || called || !strings.Contains(stderr.String(), "usage:") {
			t.Fatalf("production enablement CLI accepted: code=%d called=%v stderr=%s", code, called, stderr.String())
		}
	})
	// Unknown enablement fields are rejected by the production strict decoder,
	// before namespace/transport startup, rather than silently changing defaults.
	for _, name := range []string{"orchestrationEnabled", "orchestration_enabled"} {
		t.Run(name, func(t *testing.T) {
			path := writeTestConfig(t, cfg)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var obj map[string]json.RawMessage
			if err = json.Unmarshal(raw, &obj); err != nil {
				t.Fatal(err)
			}
			obj[name] = json.RawMessage(`true`)
			raw, err = json.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			called := false
			var stderr bytes.Buffer
			code := mainRunWithWriter([]string{"-config", path}, func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
				called = true
				return nil, nil, errors.New("unexpected transport")
			}, &stderr)
			if code != 2 || called || !strings.Contains(stderr.String(), "unknown field") {
				t.Fatalf("unauthorized enablement config accepted: code=%d called=%v stderr=%s", code, called, stderr.String())
			}
		})
	}
}

func TestOrchestrationDisabledPreservesTaskAndLedgerReads(t *testing.T) {
	f := dormantState(t, filepath.Join(t.TempDir(), "canonical.json"))
	issuer := assignment.Principal{SessionID: "claude:issuer", AgentID: "issuer", Generation: 1}
	target := assignment.Principal{SessionID: "codex:analyst", AgentID: "analyst", Generation: 1}
	for _, p := range []assignment.Principal{issuer, target} {
		if err := f.s.RegisterAgentSession(p.SessionID, p.AgentID, ed25519.PublicKey(strings.Repeat("z", 32))); err != nil {
			t.Fatal(err)
		}
	}
	r := assignment.Record{AssignmentID: "readable-assignment", State: assignment.Offered, Issuer: issuer.SessionID, IssuerAgentID: issuer.AgentID, IssuerGeneration: 1, CurrentCoordinator: issuer.SessionID, CoordinatorAgentID: issuer.AgentID, CoordinatorGeneration: 1, HandoffEvidence: []assignment.Handoff{}, Assignee: target.SessionID, StableAgentID: target.AgentID, SessionID: target.SessionID, AssigneeGeneration: 1, Role: "ANALYST", SubjectSHA: disabledSubject, IssuedAt: f.now, ValidUntil: f.now.Add(time.Hour), AdmissionBasis: assignment.AdmissionBasis{Kind: "POOL", Reference: "owner-fixture", TargetSessionID: target.SessionID, TargetAgentID: target.AgentID, Role: "ANALYST", SubjectSHA: disabledSubject}, VisibilityEvidence: assignment.VisibilityEvidence{Reference: "visible-fixture", SessionID: target.SessionID, ObservedAt: f.now}, Revision: 1, UpdatedAt: f.now}
	if err := f.s.OfferAssignment(issuer, r, "fixture"); err != nil {
		t.Fatal(err)
	}
	// A genuine legacy Run (no canonical TaskID) is a reverse control. Its
	// existing result handling may advance that Run without touching workflow/ledger.
	initial, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	legacy := initial.Runs["ordinary-run"]
	legacy.TaskID = ""
	legacy.RunID = "legacy-run"
	legacy.Task = "legacy-task"
	legacy.TaskMessages = []string{"legacy-task-message"}
	legacy.Status = fsm.ResultReady
	if err = f.s.UpdateCanonical(func(state *store.CanonicalState) error { state.Runs[legacy.RunID] = legacy; return nil }); err != nil {
		t.Fatal(err)
	}
	c, tp := disabledCoordinator(t, f)
	probe := &disabledAdapterProbe{}
	c.Workflow.(disabledOrchestration).runtime.Adapter = probe
	diskBefore, err := os.ReadFile(f.file.Path)
	if err != nil {
		t.Fatal(err)
	}
	before := canonicalBytes(t, f.s)
	for repeat := 0; repeat < 3; repeat++ {
		if err := c.RecoverWorkflow(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := c.Workflow.Recover(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := c.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	msg := envelope.Envelope{SchemaVersion: 1, MessageID: "mixed-workflow-result", InReplyTo: "assignment-workflow", Timestamp: f.now, From: "session-workflow", To: tp.session, Kind: envelope.Result, Task: "workflow-run", Run: "workflow-run", Attempt: 1, TargetSHA: disabledSubject, Fence: 1, ResultPayload: &envelope.ResultPayload{Outcome: "SUCCESS"}}
	routeBefore := tp.recipientChecks
	if err := c.Handle(context.Background(), msg); err == nil || !strings.Contains(err.Error(), "orchestrated Run requires canonical command boundary") {
		t.Fatalf("mixed snapshot leaked workflow Run into legacy handler: %v", err)
	}
	if tp.recipientChecks != routeBefore {
		t.Fatal("mixed snapshot legacy handler received workflow Run")
	}
	msg.Run, msg.Task, msg.From, msg.MessageID = "ordinary-run", "ordinary-run", "session-ordinary", "mixed-ordinary-result"
	if err := c.Handle(context.Background(), msg); err == nil || err.Error() != "uncorrelated task response" {
		t.Fatalf("mixed snapshot changed ordinary Run routing: %v", err)
	}
	if tp.recipientChecks == routeBefore {
		t.Fatal("mixed snapshot blocked ordinary legacy routing")
	}
	if probe.calls != 0 {
		t.Fatalf("mixed snapshot executed adapter: %d", probe.calls)
	}
	snap, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Tasks) != 2 || len(f.s.All()) != 3 {
		t.Fatal("disabling orchestration broke Task/Run reads")
	}
	status, err := store.InspectAssignment(snap, r.AssignmentID, f.now)
	if err != nil || status.State != assignment.Offered || status.SubjectSHA != disabledSubject {
		t.Fatalf("disabling orchestration broke ledger status reads: %+v %v", status, err)
	}
	if !bytes.Equal(before, canonicalBytes(t, f.s)) {
		t.Fatal("disabled service/status reads changed canonical bytes")
	}
	diskAfter, err := os.ReadFile(f.file.Path)
	if err != nil || !bytes.Equal(diskBefore, diskAfter) {
		t.Fatalf("mixed snapshot checkpoint changed: %v", err)
	}
	workflowBefore, _ := json.Marshal(snap.Orchestration)
	ledgerBefore, _ := json.Marshal(snap.AssignmentLedger)
	tasksBefore, _ := json.Marshal(snap.Tasks)
	if c.Workflow.OwnsRun(legacy.RunID) {
		t.Fatal("mixed snapshot fenced genuine legacy Run")
	}
	msg.Task, msg.Run, msg.From, msg.MessageID, msg.InReplyTo = legacy.Task, legacy.RunID, legacy.OwnerSession, "legacy-result", "legacy-task-message"
	sentBefore := tp.sent
	if err := c.Handle(context.Background(), msg); err != nil {
		t.Fatalf("mixed snapshot changed legacy result handling: %v", err)
	}
	afterLegacy, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !afterLegacy.Runs[legacy.RunID].Terminal() || tp.sent <= sentBefore {
		t.Fatal("mixed snapshot did not process/acknowledge legacy result")
	}
	workflowAfter, _ := json.Marshal(afterLegacy.Orchestration)
	ledgerAfter, _ := json.Marshal(afterLegacy.AssignmentLedger)
	tasksAfter, _ := json.Marshal(afterLegacy.Tasks)
	if !bytes.Equal(workflowBefore, workflowAfter) || !bytes.Equal(ledgerBefore, ledgerAfter) || !bytes.Equal(tasksBefore, tasksAfter) {
		t.Fatal("legacy result changed dormant workflow, Tasks or ledger")
	}
	if probe.calls != 0 {
		t.Fatal("legacy result unexpectedly executed orchestration adapter")
	}

}

func TestOrchestrationDisabledStillRejectsUnsupportedSchema(t *testing.T) {
	for _, kind := range []string{"unsupported-schema", "snapshot-enablement", "corrupt-orchestration"} {
		t.Run(kind, func(t *testing.T) {
			cfg := testConfig(t)
			if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(cfg.StateDir, "canonical.json")
			bad := store.EmptyCanonicalState()
			if kind == "unsupported-schema" {
				bad.SchemaVersion = 999
			}
			if kind == "corrupt-orchestration" {
				bad.Orchestration.Actions = nil
			}
			raw, err := json.Marshal(bad)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "snapshot-enablement" {
				var obj map[string]json.RawMessage
				if err = json.Unmarshal(raw, &obj); err != nil {
					t.Fatal(err)
				}
				obj["orchestration_enabled"] = json.RawMessage(`true`)
				raw, err = json.Marshal(obj)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			called := false
			var stderr bytes.Buffer
			err = runWithWriter(ctx, cfg, func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
				called = true
				return nil, nil, errors.New("unexpected transport")
			}, &stderr)
			if called || !strings.Contains(stderr.String(), "canonical state is not ready") {
				t.Fatalf("%s was not fail-closed: called=%v err=%v stderr=%s", kind, called, err, stderr.String())
			}
			// Rejection is diagnostic, never lazy repair, migration or legacy fallback.
			want := map[string]string{"unsupported-schema": "unsupported canonical schema", "snapshot-enablement": "unknown field", "corrupt-orchestration": "missing orchestration collections"}[kind]
			if !strings.Contains(stderr.String(), want) {
				t.Fatalf("%s missing distinct diagnostic: %s", kind, stderr.String())
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(raw, after) {
				t.Fatalf("%s snapshot was rewritten: %v", kind, err)
			}
		})
	}
}
