package store

import (
	"coord/assignment"
	"coord/envelope"
	"coord/fsm"
	"coord/roles"
	"coord/task"
	"crypto/ed25519"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type agentResultFixture struct {
	s       *CanonicalStore
	p       *canonicalMemory
	run     fsm.Run
	assign  assignment.Record
	private ed25519.PrivateKey
	now     time.Time
	clock   *time.Time
}

func newAgentResultFixture(t *testing.T, role roles.Role) *agentResultFixture {
	t.Helper()
	state := resultState(role)
	runID := "run-review"
	switch role {
	case roles.Auditor:
		runID = "run-audit"
	case roles.Executor:
		runID = "run-executor"
		state = executorState()
	}

	run := state.Runs[runID]
	run.TaskMessages = []string{"task-delivery"}
	state.Runs[runID] = run
	now := time.Unix(5000, 0).UTC()
	clock := &now
	private := sessionTestPrivateKey(1)
	p := &canonicalMemory{state: state}
	s, err := NewCanonicalWithClock(p, func() time.Time { return *clock })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	coordinator := assignment.Principal{SessionID: run.CoordinatorSession, AgentID: "coord-agent", Generation: 1}
	if err := s.RegisterAgentSession(coordinator.SessionID, coordinator.AgentID, sessionTestPublicKey(0x7a)); err != nil {
		t.Fatal(err)
	}
	r := assignment.Record{
		AssignmentID: "agent-result-assignment", State: assignment.Offered,
		Issuer: coordinator.SessionID, IssuerAgentID: coordinator.AgentID, IssuerGeneration: coordinator.Generation,
		CurrentCoordinator: coordinator.SessionID, CoordinatorAgentID: coordinator.AgentID, CoordinatorGeneration: coordinator.Generation,
		HandoffEvidence: []assignment.Handoff{}, Assignee: run.OwnerSession, SessionID: run.OwnerSession,
		StableAgentID: run.RoleSelection.AgentID, AssigneeGeneration: run.AgentSessionGeneration,
		Role: string(role), SubjectSHA: run.TargetSHA, IssuedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
		AdmissionBasis:     assignment.AdmissionBasis{Kind: "POOL", Reference: "review", TargetSessionID: run.OwnerSession, TargetAgentID: run.RoleSelection.AgentID, Role: string(role), SubjectSHA: run.TargetSHA},
		VisibilityEvidence: assignment.VisibilityEvidence{Reference: "operator-visible", SessionID: run.OwnerSession, ObservedAt: now.Add(-time.Minute)},
		Revision:           1, UpdatedAt: now,
		RunBinding: &assignment.RunBinding{TaskID: run.TaskID.String(), RunID: run.RunID, Fence: run.Fence, Attempt: run.Attempt},
	}
	if err := s.OfferAssignment(coordinator, r, "issue95-offer"); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeAssignment(r.Target(), r.AssignmentID, 1, "ACK", "issue95-ack"); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeAssignment(coordinator, r.AssignmentID, 2, "ACTIVATE", "issue95-active"); err != nil {
		t.Fatal(err)
	}
	state, err = s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	r = state.AssignmentLedger.Records[r.AssignmentID]
	if err := s.RecordAssignmentWorkStarted(r.Target(), r.AssignmentID, r.Revision, r.SubjectSHA, r.Role, r.Issuer, r.CurrentCoordinator, r.CoordinatorGeneration, "issue95-work-start"); err != nil {
		t.Fatal(err)
	}
	state, err = s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	r = state.AssignmentLedger.Records[r.AssignmentID]
	run = state.Runs[runID]
	return &agentResultFixture{s: s, p: p, run: run, assign: r, private: private, now: now, clock: clock}
}

func sessionTestPublicKey(fill byte) ed25519.PublicKey {
	return sessionTestPrivateKey(fill).Public().(ed25519.PublicKey)
}

func (f *agentResultFixture) frame(messageID, verdict, outcome string) testAuthenticatedFrame {
	payload := &envelope.AgentResultPayload{
		AssignmentID: f.assign.AssignmentID, Issuer: f.assign.Issuer,
		CoordinatorGeneration: f.assign.CoordinatorGeneration, ExpectedRevision: f.s.state.Tasks[f.run.TaskID].Revision,
		Outcome: outcome, Verdict: verdict, Summary: "result summary",
	}
	if f.run.RoleSelection.Profile.Role != roles.Executor {
		payload.Findings = []envelope.AgentFinding{}
		payload.InputArtifactRefs = []string{}
		payload.OutputArtifactRefs = []string{}
	} else {
		payload.OutputArtifactRefs = []string{}
	}
	e := envelope.Envelope{
		SchemaVersion: envelope.SchemaVersionAgentResult, MessageID: messageID, Timestamp: f.now,
		From: f.run.OwnerSession, To: f.run.CoordinatorSession, Kind: envelope.Result, InReplyTo: "task-delivery",
		Task: f.run.TaskID.String(), Run: f.run.RunID, Attempt: f.run.Attempt,
		TargetSHA: f.run.TargetSHA, Fence: f.run.Fence, AgentResultPayload: payload,
	}
	raw, _ := envelope.Sign(e, f.private)
	return testAuthenticatedFrame{
		envelope: e, address: f.run.OwnerSession, key: f.private.Public().(ed25519.PublicKey),
		privateKey: f.private, raw: raw, gen: f.run.AgentSessionGeneration, to: f.run.CoordinatorSession,
	}
}

func (f *agentResultFixture) apply(frame testAuthenticatedFrame) (InboxReceipt, bool, error) {
	return f.s.ApplyAgentCommand(frame, f.run.TaskID, frame.envelope.AgentResultPayload.ExpectedRevision, nil)
}

func TestAgentResultV2AcceptanceCommitsTaskInboxOutboxAndAssignmentAtomically(t *testing.T) {
	f := newAgentResultFixture(t, roles.Reviewer)
	before := f.p.saveCalls
	frame := f.frame("result-v2", "REVIEW_ACCEPTED", "")
	if err := frame.envelope.Validate(); err != nil {
		t.Fatalf("fixture envelope invalid: %v (%+v)", err, frame.envelope)
	}
	if !frame.key.Equal(ed25519.PublicKey(f.s.state.AgentSessions[frame.address].PublicKey)) {
		t.Fatalf("fixture session key mismatch")
	}
	receipt, replayed, err := f.apply(frame)
	if err != nil || replayed {
		t.Fatalf("accepted result replay=%v err=%v", replayed, err)
	}
	state, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if f.p.saveCalls != before+1 || len(receipt.EventIDs) != 1 || receipt.AgentCommandDigest == "" || receipt.AssignmentID != f.assign.AssignmentID {
		t.Fatalf("non-atomic accepted result: saves=%d receipt=%+v", f.p.saveCalls-before, receipt)
	}
	if state.Tasks[f.run.TaskID].Revision != receipt.CommittedRevision || state.AssignmentLedger.Records[f.assign.AssignmentID].State != assignment.Completed || len(state.Outbox[f.run.RunID].Messages) != 1 {
		t.Fatalf("missing atomic effects: task=%+v assignment=%+v outbox=%+v", state.Tasks[f.run.TaskID], state.AssignmentLedger.Records[f.assign.AssignmentID], state.Outbox[f.run.RunID])
	}
	ack := state.Outbox[f.run.RunID].Messages[0].Envelope
	if ack.Kind != envelope.ResultAck || ack.SchemaVersion != envelope.SchemaVersionAgentResult || ack.ResultAckPayload.Disposition != "ACCEPTED" || ack.ResultAckPayload.Outcome != "SUCCESS" || ack.ResultAckPayload.AssignmentID != f.assign.AssignmentID {
		t.Fatalf("wrong durable response: %+v", ack)
	}
	saves := f.p.saveCalls
	replay, wasReplay, err := f.apply(frame)
	if err != nil || !wasReplay || !reflect.DeepEqual(replay, receipt) || f.p.saveCalls != saves {
		t.Fatalf("exact redelivery replay=%v err=%v saves=%d/%d receipt=%+v", wasReplay, err, f.p.saveCalls, saves, replay)
	}
	altered := frame
	altered.envelope.AgentResultPayload = &envelope.AgentResultPayload{AssignmentID: f.assign.AssignmentID, Issuer: f.assign.Issuer, CoordinatorGeneration: 1, ExpectedRevision: receipt.ExpectedRevision, Verdict: "CHANGES_REQUIRED", Summary: "altered", Findings: []envelope.AgentFinding{}, InputArtifactRefs: []string{}, OutputArtifactRefs: []string{}}
	altered.raw, _ = envelope.Sign(altered.envelope, f.private)
	if _, _, err := f.apply(altered); !errors.Is(err, ErrAgentCommandReplayMismatch) {
		t.Fatalf("same message with changed signed payload error=%v", err)
	}
}

func TestAgentResultNoAcceptancePersistsReceiptOnlyAndKeepsAssignmentActive(t *testing.T) {
	f := newAgentResultFixture(t, roles.Auditor)
	frame := f.frame("audit-blocked-v2", "AUDIT_BLOCKED", "")
	before, _ := f.s.CanonicalSnapshot()
	receipt, replayed, err := f.apply(frame)
	if err != nil || replayed || len(receipt.EventIDs) != 0 || receipt.CommittedRevision != receipt.ExpectedRevision {
		t.Fatalf("no-acceptance receipt=%+v replay=%v err=%v", receipt, replayed, err)
	}
	after, _ := f.s.CanonicalSnapshot()
	if after.Tasks[f.run.TaskID].Revision != before.Tasks[f.run.TaskID].Revision || after.Runs[f.run.RunID].Status != fsm.Started || after.AssignmentLedger.Records[f.assign.AssignmentID].State != assignment.Active || len(after.Outbox[f.run.RunID].Messages) != 0 {
		t.Fatal("no-acceptance verdict changed Task, Run, Assignment or canonical outbox")
	}
}

func TestA95NoAcceptanceVerdictsRejectVoidAndExpiredAssignmentsWithoutWrite(t *testing.T) {
	cases := []struct {
		name    string
		role    roles.Role
		verdict string
	}{
		{"review-not-evaluated", roles.Reviewer, "NOT_EVALUATED"},
		{"audit-incomplete", roles.Auditor, "AUDIT_INCOMPLETE"},
		{"audit-blocked", roles.Auditor, "AUDIT_BLOCKED"},
	}
	for _, state := range []string{"VOID", "EXPIRE"} {
		for _, tc := range cases {
			t.Run(state+"/"+tc.name, func(t *testing.T) {
				f := newAgentResultFixture(t, tc.role)
				if state == "EXPIRE" {
					*f.clock = f.assign.ValidUntil.Add(time.Second)
				}
				if err := f.s.ChangeAssignment(f.assign.Coordinator(), f.assign.AssignmentID, f.assign.Revision, state, "a95-terminal"); err != nil {
					t.Fatal(err)
				}
				frame := f.frame("a95-"+state+"-"+tc.name, tc.verdict, "")
				before, err := f.s.CanonicalSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				saves := f.p.saveCalls
				if _, replayed, err := f.apply(frame); err == nil || replayed {
					t.Fatalf("no-acceptance verdict accepted on %s assignment: replay=%v err=%v", state, replayed, err)
				}
				after, err := f.s.CanonicalSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				if f.p.saveCalls != saves || !reflect.DeepEqual(before, after) {
					t.Fatalf("rejected no-acceptance command wrote state: saves=%d/%d", f.p.saveCalls, saves)
				}
			})
		}
	}
}

func TestA95RegisteredDifferentAgentCannotSubmitAssignmentResult(t *testing.T) {
	f := newAgentResultFixture(t, roles.Reviewer)
	otherAddress := "codex:other-active-agent"
	otherPrivate := sessionTestPrivateKey(0x69)
	if err := f.s.RegisterAgentSession(otherAddress, "other-stable-agent", otherPrivate.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	otherSession, ok := f.s.state.AgentSessions[otherAddress]
	if !ok || !otherSession.Active() {
		t.Fatal("negative control requires a registered active AgentSession")
	}
	frame := f.frame("a95-registered-wrong-assignee", "REVIEW_ACCEPTED", "")
	frame.envelope.From = otherAddress
	frame.address = otherAddress
	frame.key = otherPrivate.Public().(ed25519.PublicKey)
	frame.privateKey = otherPrivate
	frame.raw, _ = envelope.Sign(frame.envelope, otherPrivate)
	before, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	saves := f.p.saveCalls
	if _, replayed, err := f.apply(frame); err == nil || replayed {
		t.Fatalf("registered different active AgentSession result replay=%v err=%v", replayed, err)
	}
	after, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if f.p.saveCalls != saves || !reflect.DeepEqual(before, after) {
		t.Fatal("registered wrong assignee rejection changed canonical state")
	}
}

func TestAgentResultV2AcceptedVerdictsMapToTypedTaskEvents(t *testing.T) {
	for _, tc := range []struct {
		role    roles.Role
		verdict string
		outcome string
		want    task.EventType
	}{
		{roles.Executor, "", "SUCCESS", task.ExecutorResultRecorded},
		{roles.Reviewer, "CHANGES_REQUIRED", "", task.ReviewResultRecorded},
		{roles.Auditor, "AUDIT_ACCEPTED", "", task.AuditResultRecorded},
	} {
		t.Run(string(tc.role)+"/"+tc.verdict+tc.outcome, func(t *testing.T) {
			f := newAgentResultFixture(t, tc.role)
			frame := f.frame("typed-"+string(tc.role), tc.verdict, tc.outcome)
			if _, _, err := f.apply(frame); err != nil {
				t.Fatal(err)
			}
			state, err := f.s.CanonicalSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			last := state.Events[len(state.Events)-1]
			if last.Type != tc.want || state.AssignmentLedger.Records[f.assign.AssignmentID].State != assignment.Completed {
				t.Fatalf("event=%+v assignment=%+v want type=%s", last, state.AssignmentLedger.Records[f.assign.AssignmentID], tc.want)
			}
		})
	}
}

func TestAgentResultV2NegativeCasesDoNotWrite(t *testing.T) {
	cases := []struct {
		name    string
		role    roles.Role
		verdict string
		mutate  func(*agentResultFixture, *testAuthenticatedFrame)
	}{
		{"unknown verdict", roles.Reviewer, "REVIEW_ACCEPTED", func(_ *agentResultFixture, f *testAuthenticatedFrame) {
			f.envelope.AgentResultPayload.Verdict = "SURPRISE"
		}},
		{"invalid submission", roles.Reviewer, "INVALID_SUBMISSION", nil},
		{"audit ineligible", roles.Auditor, "AUDIT_INELIGIBLE", nil},
		{"wrong fence", roles.Reviewer, "REVIEW_ACCEPTED", func(_ *agentResultFixture, f *testAuthenticatedFrame) { f.envelope.Fence++ }},
		{"wrong subject", roles.Reviewer, "REVIEW_ACCEPTED", func(_ *agentResultFixture, f *testAuthenticatedFrame) { f.envelope.TargetSHA = strings.Repeat("b", 40) }},
		{"wrong issuer", roles.Reviewer, "REVIEW_ACCEPTED", func(_ *agentResultFixture, f *testAuthenticatedFrame) {
			f.envelope.AgentResultPayload.Issuer = "another-issuer"
		}},
		{"wrong coordinator generation", roles.Reviewer, "REVIEW_ACCEPTED", func(_ *agentResultFixture, f *testAuthenticatedFrame) {
			f.envelope.AgentResultPayload.CoordinatorGeneration++
		}},
		{"stale assignee generation", roles.Reviewer, "REVIEW_ACCEPTED", func(_ *agentResultFixture, f *testAuthenticatedFrame) { f.gen++ }},
		{"wrong recipient", roles.Reviewer, "REVIEW_ACCEPTED", func(_ *agentResultFixture, f *testAuthenticatedFrame) { f.to = "wrong-coordinator" }},
		{"wrong assignee", roles.Reviewer, "REVIEW_ACCEPTED", func(_ *agentResultFixture, f *testAuthenticatedFrame) { f.address = "other:session" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newAgentResultFixture(t, tc.role)
			frame := fixture.frame("negative-"+strings.ReplaceAll(tc.name, " ", "-"), tc.verdict, "")
			if tc.mutate != nil {
				tc.mutate(fixture, &frame)
			}
			frame.raw, _ = envelope.Sign(frame.envelope, fixture.private)
			before, _ := fixture.s.CanonicalSnapshot()
			saves := fixture.p.saveCalls
			if _, replayed, err := fixture.apply(frame); err == nil || replayed {
				t.Fatalf("invalid command replay=%v err=%v", replayed, err)
			}
			after, _ := fixture.s.CanonicalSnapshot()
			if fixture.p.saveCalls != saves || !reflect.DeepEqual(before, after) {
				t.Fatal("rejected command changed canonical bytes/state")
			}
		})
	}
}

func TestAgentResultV2RejectsWithoutAssignmentOrOnCommitFailure(t *testing.T) {
	f := newAgentResultFixture(t, roles.Reviewer)
	state, _ := f.s.CanonicalSnapshot()
	delete(state.AssignmentLedger.Records, f.assign.AssignmentID)
	state.AssignmentLedger.Events = nil
	state.AssignmentLedger = nil
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	p := &canonicalMemory{state: state}
	s, err := NewCanonicalWithClock(p, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	frame := f.frame("no-assignment", "REVIEW_ACCEPTED", "")
	before, _ := s.CanonicalSnapshot()
	if _, _, err := s.ApplyAgentCommand(frame, f.run.TaskID, frame.envelope.AgentResultPayload.ExpectedRevision, nil); !errors.Is(err, ErrAssignmentUnknown) {
		t.Fatalf("unassigned agent result error=%v", err)
	}
	after, _ := s.CanonicalSnapshot()
	if p.saveCalls != 0 || !reflect.DeepEqual(before, after) {
		t.Fatal("unassigned agent result changed state")
	}

	g := newAgentResultFixture(t, roles.Reviewer)
	g.p.fail = true
	failedFrame := g.frame("save-failure", "REVIEW_ACCEPTED", "")
	failedBefore, _ := copyCanonicalState(g.p.state)
	if _, _, err := g.apply(failedFrame); !errors.Is(err, ErrPersistence) {
		t.Fatalf("canonical save failure error=%v", err)
	}
	if !reflect.DeepEqual(failedBefore, g.p.state) || len(g.p.state.Inbox) != 0 || len(g.p.state.Outbox[g.run.RunID].Messages) != 0 {
		t.Fatal("failed commit published receipt, event or outbox side effect")
	}
}

func TestAgentResultRejectsPayloadMutationAfterSigningWithoutWrite(t *testing.T) {
	f := newAgentResultFixture(t, roles.Reviewer)
	frame := f.frame("mutated-after-signing", "REVIEW_ACCEPTED", "")
	frame.envelope.AgentResultPayload.Summary = "changed after signature"
	before, _ := f.s.CanonicalSnapshot()
	saves := f.p.saveCalls
	if _, replayed, err := f.apply(frame); !errors.Is(err, ErrAgentCommandUntrustedSignature) || replayed {
		t.Fatalf("mutated signed payload replay=%v err=%v", replayed, err)
	}
	after, _ := f.s.CanonicalSnapshot()
	if f.p.saveCalls != saves || !reflect.DeepEqual(before, after) {
		t.Fatal("payload mutation after signing changed canonical state")
	}
}

func TestAgentResultRejectsReviewerAndAuditorRoleConflictWithoutWrite(t *testing.T) {
	for _, role := range []roles.Role{roles.Reviewer, roles.Auditor} {
		t.Run(string(role), func(t *testing.T) {
			f := newAgentResultFixture(t, role)
			priorRunID := "run-executor"
			verdict := "REVIEW_ACCEPTED"
			if role == roles.Auditor {
				priorRunID, verdict = "run-review", "AUDIT_ACCEPTED"
			}
			f.s.mu.Lock()
			prior := f.s.state.Runs[priorRunID]
			selection := *prior.RoleSelection
			selection.AgentID = f.run.RoleSelection.AgentID
			prior.RoleSelection = &selection
			if len(prior.RoleHistory) != 0 {
				prior.RoleHistory[len(prior.RoleHistory)-1] = selection
			}
			f.s.state.Runs[priorRunID] = prior
			f.s.mu.Unlock()
			frame := f.frame("role-conflict-"+string(role), verdict, "")
			before, _ := f.s.CanonicalSnapshot()
			saves := f.p.saveCalls
			if _, replayed, err := f.apply(frame); err == nil || replayed {
				t.Fatalf("role-conflicted result replay=%v err=%v", replayed, err)
			}
			after, _ := f.s.CanonicalSnapshot()
			if f.p.saveCalls != saves || !reflect.DeepEqual(before, after) {
				t.Fatal("role-conflict rejection changed canonical state")
			}
		})
	}
}

func TestAgentResultRejectsVoidExpiredAndStaleCoordinatorWithoutWrite(t *testing.T) {
	for _, terminal := range []string{"VOID", "EXPIRE"} {
		t.Run(terminal, func(t *testing.T) {
			f := newAgentResultFixture(t, roles.Reviewer)
			if terminal == "EXPIRE" {
				*f.clock = f.assign.ValidUntil.Add(time.Second)
			}
			if err := f.s.ChangeAssignment(f.assign.Coordinator(), f.assign.AssignmentID, f.assign.Revision, terminal, "test-terminal"); err != nil {
				t.Fatal(err)
			}
			frame := f.frame("terminal-"+terminal, "REVIEW_ACCEPTED", "")
			before, _ := f.s.CanonicalSnapshot()
			saves := f.p.saveCalls
			if _, replayed, err := f.apply(frame); err == nil || replayed {
				t.Fatalf("terminal assignment accepted: replay=%v err=%v", replayed, err)
			}
			after, _ := f.s.CanonicalSnapshot()
			if f.p.saveCalls != saves || !reflect.DeepEqual(before, after) {
				t.Fatal("terminal assignment rejection changed canonical state")
			}
		})
	}
	t.Run("rotated coordinator generation", func(t *testing.T) {
		f := newAgentResultFixture(t, roles.Reviewer)
		if err := f.s.RotateAgentSessionKey(f.assign.CurrentCoordinator, sessionTestPublicKey(0x55)); err != nil {
			t.Fatal(err)
		}
		frame := f.frame("stale-coordinator", "REVIEW_ACCEPTED", "")
		before, _ := f.s.CanonicalSnapshot()
		saves := f.p.saveCalls
		if _, replayed, err := f.apply(frame); err == nil || replayed {
			t.Fatalf("stale coordinator assignment accepted: replay=%v err=%v", replayed, err)
		}
		after, _ := f.s.CanonicalSnapshot()
		if f.p.saveCalls != saves || !reflect.DeepEqual(before, after) {
			t.Fatal("stale coordinator rejection changed canonical state")
		}
	})
}

func TestAgentResultV2MapsRoleVerdictsExplicitly(t *testing.T) {
	for _, tc := range []struct {
		role roles.Role
		p    envelope.AgentResultPayload
		want AgentResultDisposition
	}{
		{roles.Executor, envelope.AgentResultPayload{Outcome: "SUCCESS"}, AgentResultAccepted},
		{roles.Executor, envelope.AgentResultPayload{Outcome: "FAILURE"}, AgentResultAccepted},
		{roles.Executor, envelope.AgentResultPayload{Outcome: "PARTIAL"}, AgentResultAccepted},
		{roles.Reviewer, envelope.AgentResultPayload{Verdict: "REVIEW_ACCEPTED"}, AgentResultAccepted},
		{roles.Reviewer, envelope.AgentResultPayload{Verdict: "CHANGES_REQUIRED"}, AgentResultAccepted},
		{roles.Reviewer, envelope.AgentResultPayload{Verdict: "NOT_EVALUATED"}, AgentResultNoAcceptance},
		{roles.Auditor, envelope.AgentResultPayload{Verdict: "AUDIT_ACCEPTED"}, AgentResultAccepted},
		{roles.Auditor, envelope.AgentResultPayload{Verdict: "CHANGES_REQUIRED"}, AgentResultAccepted},
		{roles.Auditor, envelope.AgentResultPayload{Verdict: "AUDIT_INCOMPLETE"}, AgentResultNoAcceptance},
		{roles.Auditor, envelope.AgentResultPayload{Verdict: "AUDIT_BLOCKED"}, AgentResultNoAcceptance},
	} {
		disposition, err := ClassifyAgentResult(tc.role, tc.p)
		if err != nil || disposition != tc.want {
			t.Fatalf("role=%s payload=%+v disposition=%q err=%v want=%q", tc.role, tc.p, disposition, err, tc.want)
		}
	}
	for _, tc := range []struct {
		role    roles.Role
		verdict string
	}{
		{roles.Reviewer, "INVALID_SUBMISSION"}, {roles.Auditor, "AUDIT_INELIGIBLE"},
		{roles.Auditor, "UNKNOWN"}, {roles.Executor, "SUCCESS"},
	} {
		if _, err := ClassifyAgentResult(tc.role, envelope.AgentResultPayload{Verdict: tc.verdict}); err == nil {
			t.Fatalf("accepted rejected verdict %s for %s", tc.verdict, tc.role)
		}
	}
}
