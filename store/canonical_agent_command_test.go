package store

import (
	"coord/envelope"
	"coord/fsm"
	"coord/roles"
	"coord/task"
	"coord/taskid"
	"crypto/ed25519"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

type testAuthenticatedFrame struct {
	envelope   envelope.Envelope
	address    string
	key        ed25519.PublicKey
	privateKey ed25519.PrivateKey
	raw        []byte
	gen        uint64
	to         string
}

type aliasingAgentCommandFrame struct {
	testAuthenticatedFrame
	rawBytes []byte
}

func (f aliasingAgentCommandFrame) RawSignedFrame() []byte { return f.rawBytes }
func (f aliasingAgentCommandFrame) SenderAddress() string {
	for i := range f.rawBytes {
		f.rawBytes[i] ^= 0xff
	}
	return f.address
}

func (f testAuthenticatedFrame) Envelope() envelope.Envelope { return f.envelope }
func (f testAuthenticatedFrame) RawSignedFrame() []byte {
	if f.raw != nil {
		return append([]byte(nil), f.raw...)
	}
	raw, _ := envelope.Sign(f.envelope, f.privateKey)
	return raw
}
func (f testAuthenticatedFrame) SenderAddress() string { return f.address }
func (f testAuthenticatedFrame) VerifiedPublicKey() ed25519.PublicKey {
	return append(ed25519.PublicKey(nil), f.key...)
}
func (f testAuthenticatedFrame) SessionGeneration() uint64 { return f.gen }
func (f testAuthenticatedFrame) ReceivedBy() string        { return f.to }

func validAgentCommandFrame(state CanonicalState, messageID string) testAuthenticatedFrame {
	return authenticatedFrameForRun(state, "run-1", messageID, "coordd")
}

func authenticatedFrameForRun(state CanonicalState, runID, messageID, receiver string) testAuthenticatedFrame {
	run := state.Runs[runID]
	id := run.TaskID
	session := state.AgentSessions[run.OwnerSession]
	now := time.Unix(5000, 0).UTC()
	return testAuthenticatedFrame{
		envelope: envelope.Envelope{
			SchemaVersion: 1, MessageID: messageID, Timestamp: now, From: run.OwnerSession,
			To: receiver, Kind: envelope.Heartbeat, Task: id.String(), Run: run.RunID,
			Attempt: run.Attempt, TargetSHA: "0000000000000000000000000000000000000000", Fence: run.Fence,
			InReplyTo: "task-assignment", HeartbeatPayload: &envelope.HeartbeatPayload{
				State: "STARTED", NextUpdateBy: now.Add(time.Minute),
			},
		},
		address: run.OwnerSession, key: append(ed25519.PublicKey(nil), session.PublicKey...),
		privateKey: sessionTestPrivateKey(1), gen: session.Generation, to: receiver,
	}
}

func applyTestAgentCommand(clock *canonicalTestClock, messageID string) func(*CanonicalState, fsm.Run) (InboxReceipt, error) {
	return func(state *CanonicalState, run fsm.Run) (InboxReceipt, error) {
		id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
		value := state.Tasks[id]
		value.Revision++
		value.LastEventSeq++
		value.UpdatedAt = clock.Now()
		state.Tasks[id] = value
		event := task.DomainEvent{
			ID: task.EventID(id, value.LastEventSeq), TaskID: id, RunID: run.RunID,
			EventSeq: value.LastEventSeq, TaskRevision: value.Revision, TaskFence: value.Fence,
			Type: task.EventType("TEST_AGENT_COMMAND_APPLIED"), ActorID: run.RoleSelection.AgentID,
			CreatedAt: clock.Now(), Summary: "test command",
		}
		state.Events = append(state.Events, event)
		out := envelope.Envelope{
			SchemaVersion: 1, MessageID: "ack-" + event.ID, InReplyTo: messageID,
			Timestamp: clock.Now(), From: "coordd", To: run.OwnerSession, Kind: envelope.ResultAck,
			Task: id.String(), Run: run.RunID, Attempt: run.Attempt,
			TargetSHA: "0000000000000000000000000000000000000000", Fence: run.Fence,
			ResultAckPayload: &envelope.ResultAckPayload{Outcome: "SUCCESS"},
		}
		state.Outbox[run.RunID] = appendAgentCommandOutbox(state.Outbox[run.RunID], OutboxMessage{Envelope: out, EventID: event.ID})
		return InboxReceipt{
			ActorID: run.RoleSelection.AgentID, MessageID: messageID, TaskID: id,
			RunID: run.RunID, EventIDs: []string{event.ID}, ExpectedRevision: value.Revision - 1,
			CommittedRevision: value.Revision, RecordedAt: clock.Now(),
		}, nil
	}
}

func appendAgentCommandOutbox(queue OutboxQueue, message OutboxMessage) OutboxQueue {
	queue.Messages = append(queue.Messages, message)
	return queue
}

func assertAgentCommandRefused(t *testing.T, state CanonicalState, frame testAuthenticatedFrame, id taskid.ID, expectedRevision uint64, want error) {
	t.Helper()
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, &canonicalTestClock{now: time.Unix(5000, 0).UTC()})
	before, err := copyCanonicalState(p.state)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, replayed, err := s.ApplyAgentCommand(frame, id, expectedRevision, func(*CanonicalState, fsm.Run) (InboxReceipt, error) {
		called = true
		return InboxReceipt{}, nil
	})
	if !errors.Is(err, want) || replayed || called || p.saveCalls != 0 || !reflect.DeepEqual(before, p.state) {
		t.Fatalf("refusal replay=%v called=%v error=%v want=%v saves=%d", replayed, called, err, want, p.saveCalls)
	}
}

func TestAgentCommandRejectsMalformedEnvelope(t *testing.T) {
	state := taskStateWithRun()
	frame := validAgentCommandFrame(state, "malformed")
	frame.envelope.MessageID = "  "
	assertAgentCommandRefused(t, state, frame, taskid.ID(frame.envelope.Task), 2, ErrAgentCommandMalformed)
}

func TestAgentCommandRejectsWrongRecipient(t *testing.T) {
	state := taskStateWithRun()
	frame := validAgentCommandFrame(state, "wrong-recipient")
	frame.to = "another-daemon"
	assertAgentCommandRefused(t, state, frame, taskid.ID(frame.envelope.Task), 2, ErrAgentCommandWrongRecipient)
}

func TestAgentCommandRejectsFutureFence(t *testing.T) {
	state := taskStateWithRun()
	frame := validAgentCommandFrame(state, "future-fence")
	frame.envelope.Fence++
	assertAgentCommandRefused(t, state, frame, taskid.ID(frame.envelope.Task), 2, ErrAgentCommandFutureFence)
}

func TestAgentCommandRejectsAttemptMismatch(t *testing.T) {
	state := taskStateWithRun()
	frame := validAgentCommandFrame(state, "attempt-mismatch")
	frame.envelope.Attempt++
	assertAgentCommandRefused(t, state, frame, taskid.ID(frame.envelope.Task), 2, ErrAgentCommandAttemptMismatch)
}

func TestAgentCommandRejectsUntrustedSignature(t *testing.T) {
	state := taskStateWithRun()
	frame := validAgentCommandFrame(state, "bad-signature-evidence")
	frame.key = sessionTestKey(0x7f)
	assertAgentCommandRefused(t, state, frame, taskid.ID(frame.envelope.Task), 2, ErrAgentCommandUntrustedSignature)
}

func TestAgentCommandRejectsFabricatedAuthenticatedFrame(t *testing.T) {
	state := taskStateWithRun()
	frame := validAgentCommandFrame(state, "fabricated-frame")
	frame.raw = []byte("not a signed envelope")
	assertAgentCommandRefused(t, state, frame, taskid.ID(frame.envelope.Task), 2, ErrAgentCommandUntrustedSignature)
}

func TestAgentCommandRejectsEnvelopeDifferentFromSignedBytes(t *testing.T) {
	state := taskStateWithRun()
	frame := validAgentCommandFrame(state, "decoded-envelope-mismatch")
	frame.raw = frame.RawSignedFrame()
	frame.envelope.Timestamp = frame.envelope.Timestamp.Add(time.Second)
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, &canonicalTestClock{now: time.Unix(5000, 0).UTC()})
	called := false
	_, replayed, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, func(*CanonicalState, fsm.Run) (InboxReceipt, error) {
		called = true
		return InboxReceipt{}, nil
	})
	if !errors.Is(err, ErrAgentCommandUntrustedSignature) || replayed || called || p.saveCalls != 0 {
		t.Fatalf("mismatched decoded envelope replay=%v called=%v saves=%d error=%v", replayed, called, p.saveCalls, err)
	}
}

func TestAgentCommandCopiesSignedBytesBeforeReadingOtherFrameFields(t *testing.T) {
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	state := taskStateWithRun()
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, clock)
	base := validAgentCommandFrame(state, "frame-byte-copy")
	frame := aliasingAgentCommandFrame{testAuthenticatedFrame: base, rawBytes: base.RawSignedFrame()}
	receipt, replayed, err := s.ApplyAgentCommand(frame, taskid.ID(base.envelope.Task), 2, applyTestAgentCommand(clock, base.envelope.MessageID))
	if err != nil || replayed || receipt.MessageID != base.envelope.MessageID || p.saveCalls != 1 {
		t.Fatalf("frame byte copy receipt=%+v replay=%v saves=%d error=%v", receipt, replayed, p.saveCalls, err)
	}
}

func TestAgentCommandRejectsStaleSessionGeneration(t *testing.T) {
	state := taskStateWithRun()
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, clock)
	frame := validAgentCommandFrame(state, "generation")
	if err := s.RotateAgentSessionKey(frame.address, sessionTestKey(0x6a)); err != nil {
		t.Fatal(err)
	}
	before, _ := copyCanonicalState(p.state)
	saves := p.saveCalls
	_, replayed, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, func(*CanonicalState, fsm.Run) (InboxReceipt, error) { return InboxReceipt{}, nil })
	if !errors.Is(err, ErrAgentCommandStaleGeneration) || replayed || p.saveCalls != saves || !reflect.DeepEqual(before, p.state) {
		t.Fatalf("stale generation replay=%v error=%v saves=%d", replayed, err, p.saveCalls)
	}
}

func TestAgentCommandCommitsEventReceiptOutboxAtomically(t *testing.T) {
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	state := taskStateWithRun()
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, clock)
	frame := validAgentCommandFrame(state, "agent-message")
	apply := applyTestAgentCommand(clock, frame.envelope.MessageID)
	receipt, replayed, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, apply)
	if err != nil || replayed {
		t.Fatalf("receipt=%+v replayed=%v err=%v", receipt, replayed, err)
	}
	if p.saveCalls != 1 || len(p.state.Events) != 3 || len(p.state.Inbox) != 1 || len(p.state.Outbox["run-1"].Messages) != 1 {
		t.Fatalf("commit was not atomic: saves=%d events=%d inbox=%d outbox=%d", p.saveCalls, len(p.state.Events), len(p.state.Inbox), len(p.state.Outbox["run-1"].Messages))
	}
	if receipt.ActorID != "agent-executor" || receipt.EventIDs[0] != p.state.Outbox["run-1"].Messages[0].EventID {
		t.Fatalf("receipt/outbox did not use committed actor and event: %+v %+v", receipt, p.state.Outbox["run-1"].Messages[0])
	}
}

func TestAgentCommandReplayReturnsPriorReceiptWithoutWriting(t *testing.T) {
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	state := taskStateWithRun()
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, clock)
	frame := validAgentCommandFrame(state, "agent-message")
	first, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, frame.envelope.MessageID))
	if err != nil {
		t.Fatal(err)
	}
	saves := p.saveCalls
	attemptBefore, _ := s.Get(frame.envelope.Run)
	called := false
	replayed, wasReplay, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, func(*CanonicalState, fsm.Run) (InboxReceipt, error) {
		called = true
		return InboxReceipt{}, nil
	})
	attemptAfter, _ := s.Get(frame.envelope.Run)
	if err != nil || !wasReplay || called || p.saveCalls != saves || !reflect.DeepEqual(first, replayed) || attemptBefore.Attempt != attemptAfter.Attempt {
		t.Fatalf("replay receipt=%+v replay=%v called=%v saves=%d err=%v", replayed, wasReplay, called, p.saveCalls, err)
	}
}

func TestAgentCommandRejectsInvalidFrameBeforePersistence(t *testing.T) {
	state := taskStateWithRun()
	for _, tc := range []struct {
		name   string
		mutate func(*testAuthenticatedFrame)
		want   error
	}{
		{"malformed envelope", func(f *testAuthenticatedFrame) { f.envelope.MessageID = " " }, ErrAgentCommandMalformed},
		{"wrong recipient", func(f *testAuthenticatedFrame) { f.to = "other" }, ErrAgentCommandWrongRecipient},
		{"untrusted sender", func(f *testAuthenticatedFrame) { f.address = "forged" }, ErrAgentCommandUntrustedSignature},
		{"unknown run", func(f *testAuthenticatedFrame) { f.envelope.Run = "missing-run" }, ErrAgentCommandNotCurrentWriter},
		{"attempt mismatch", func(f *testAuthenticatedFrame) { f.envelope.Attempt++ }, ErrAgentCommandAttemptMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &canonicalMemory{state: state}
			s := newCanonicalWithTestClock(t, p, &canonicalTestClock{now: time.Unix(5000, 0).UTC()})
			frame := validAgentCommandFrame(state, "reject-"+tc.name)
			tc.mutate(&frame)
			before, _ := copyCanonicalState(p.state)
			_, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, func(*CanonicalState, fsm.Run) (InboxReceipt, error) {
				return InboxReceipt{}, fmt.Errorf("must not run")
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want errors.Is(_, %v)", err, tc.want)
			}
			if p.saveCalls != 0 || !reflect.DeepEqual(before, p.state) {
				t.Fatalf("refusal changed durable state: saves=%d", p.saveCalls)
			}
		})
	}
}

func TestAgentCommandRejectsUnknownTaskOrRun(t *testing.T) {
	state := taskStateWithRun()
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, &canonicalTestClock{now: time.Unix(5000, 0).UTC()})
	frame := validAgentCommandFrame(state, "unknown-task")
	unknown := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAA")
	frame.envelope.Task = unknown.String()
	_, _, err := s.ApplyAgentCommand(frame, unknown, 2, func(*CanonicalState, fsm.Run) (InboxReceipt, error) { return InboxReceipt{}, nil })
	if !errors.Is(err, ErrAgentCommandUnknownTask) || p.saveCalls != 0 {
		t.Fatalf("unknown Task error=%v saves=%d", err, p.saveCalls)
	}
	frame = validAgentCommandFrame(state, "unknown-run")
	frame.envelope.Run = "missing-run"
	_, _, err = s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, func(*CanonicalState, fsm.Run) (InboxReceipt, error) { return InboxReceipt{}, nil })
	if !errors.Is(err, ErrAgentCommandNotCurrentWriter) || p.saveCalls != 0 {
		t.Fatalf("unknown Run error=%v saves=%d", err, p.saveCalls)
	}
}

func TestAgentCommandBoundaryRejectsActorUnboundToRun(t *testing.T) {
	state := taskStateWithRun()
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, clock)
	foreignKey := sessionTestKey(0x71)
	if err := s.RegisterAgentSession("foreign-session", "agent-foreign", foreignKey); err != nil {
		t.Fatal(err)
	}
	frame := validAgentCommandFrame(state, "foreign-owner")
	frame.address, frame.envelope.From, frame.key, frame.privateKey, frame.gen = "foreign-session", "foreign-session", foreignKey, sessionTestPrivateKey(0x71), 1
	_, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, func(*CanonicalState, fsm.Run) (InboxReceipt, error) { return InboxReceipt{}, nil })
	if !errors.Is(err, ErrAgentCommandWrongSession) || p.saveCalls != 1 {
		t.Fatalf("foreign session error=%v saves=%d", err, p.saveCalls)
	}
}

func TestAgentCommandRejectsForeignOwnerSession(t *testing.T) {
	state := taskStateWithRun()
	ensureCanonicalTestAgentSession(&state, "foreign-session", "agent-foreign", time.Unix(101, 0).UTC())
	frame := validAgentCommandFrame(state, "foreign-session")
	foreign := state.AgentSessions["foreign-session"]
	frame.address, frame.envelope.From, frame.key, frame.gen = "foreign-session", "foreign-session", foreign.PublicKey, foreign.Generation
	assertAgentCommandRefused(t, state, frame, taskid.ID(frame.envelope.Task), 2, ErrAgentCommandWrongSession)
}

func TestAgentCommandRejectsStaleFence(t *testing.T) {
	state := taskStateWithRun()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	taskValue := state.Tasks[id]
	taskValue.Revision++
	taskValue.LastEventSeq++
	taskValue.Fence++
	taskValue.UpdatedAt = taskValue.UpdatedAt.Add(time.Second)
	state.Tasks[id] = taskValue
	run := state.Runs["run-1"]
	run.Fence++
	state.Runs[run.RunID] = run
	state.Events = append(state.Events, task.DomainEvent{
		ID: task.EventID(id, taskValue.LastEventSeq), TaskID: id, RunID: run.RunID,
		EventSeq: taskValue.LastEventSeq, TaskRevision: taskValue.Revision, TaskFence: taskValue.Fence,
		Type: task.TaskOwnershipTransferred, CreatedAt: taskValue.UpdatedAt,
	})
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, &canonicalTestClock{now: time.Unix(5000, 0).UTC()})
	frame := validAgentCommandFrame(state, "stale-fence")
	frame.envelope.Fence--
	_, _, err := s.ApplyAgentCommand(frame, id, taskValue.Revision, func(*CanonicalState, fsm.Run) (InboxReceipt, error) { return InboxReceipt{}, nil })
	if !errors.Is(err, ErrAgentCommandStaleFence) || p.saveCalls != 0 {
		t.Fatalf("stale Fence error=%v saves=%d", err, p.saveCalls)
	}
}

func TestAgentCommandRejectsOldSessionBeforeFenceCheckBypassedByKnownMessageID(t *testing.T) {
	previous := claimedRecoveryState()
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	p := &canonicalMemory{state: previous}
	s := newCanonicalWithTestClock(t, p, clock)
	frame := authenticatedFrameForRun(previous, "run-1", "already-committed", "coordd")
	if _, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, frame.envelope.MessageID)); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Second)
	next := ownershipTransferCandidate(t, s.state, "run-1", roles.Executor, 1, 2)
	completeRecoveryRun(&next, "recovery-session", 2)
	if err := s.UpdateCanonical(func(state *CanonicalState) error { *state = next; return nil }); err != nil {
		t.Fatal(err)
	}
	before, _ := copyCanonicalState(p.state)
	saves := p.saveCalls
	_, replayed, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, frame.envelope.MessageID))
	if !errors.Is(err, ErrAgentCommandWrongSession) || replayed || p.saveCalls != saves || !reflect.DeepEqual(before, p.state) {
		t.Fatalf("known message from old session reached Fence/replay: replay=%v error=%v saves=%d", replayed, err, p.saveCalls)
	}
}

func TestAgentCommandRejectsNonCurrentWriter(t *testing.T) {
	state := taskStateWithRun()
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, &canonicalTestClock{now: time.Unix(5000, 0).UTC()})
	frame := validAgentCommandFrame(state, "terminal-run")
	s.mu.Lock()
	run := s.state.Runs[frame.envelope.Run]
	run.Status = fsm.Done
	s.state.Runs[run.RunID] = run
	s.mu.Unlock()
	_, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, func(*CanonicalState, fsm.Run) (InboxReceipt, error) { return InboxReceipt{}, nil })
	if !errors.Is(err, ErrAgentCommandNotCurrentWriter) || p.saveCalls != 0 {
		t.Fatalf("non-current writer error=%v saves=%d", err, p.saveCalls)
	}
}

func TestAgentCommandRejectsStaleGenerationBeforeReplay(t *testing.T) {
	state := taskStateWithRun()
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, clock)
	frame := validAgentCommandFrame(state, "agent-message")
	if _, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, frame.envelope.MessageID)); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Second)
	newKey := sessionTestKey(0x6a)
	if err := s.RotateAgentSessionKey(frame.address, newKey); err != nil {
		t.Fatal(err)
	}
	before, _ := copyCanonicalState(p.state)
	saves := p.saveCalls
	_, replayed, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, frame.envelope.MessageID))
	if !errors.Is(err, ErrAgentCommandStaleGeneration) || replayed || p.saveCalls != saves || !reflect.DeepEqual(before, p.state) {
		t.Fatalf("stale-generation retry returned replay=%v error=%v saves=%d", replayed, err, p.saveCalls)
	}
}

func TestAgentCommandRevokedSessionDoesNotReturnPriorReceipt(t *testing.T) {
	state := taskStateWithRun()
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, clock)
	frame := validAgentCommandFrame(state, "agent-message")
	if _, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, frame.envelope.MessageID)); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Second)
	if err := s.ReplaceAgentSessionAddress(frame.address, "executor-new", sessionTestKey(0x72), "test revoke"); err != nil {
		t.Fatal(err)
	}
	before, _ := copyCanonicalState(p.state)
	saves := p.saveCalls
	_, replayed, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, frame.envelope.MessageID))
	if !errors.Is(err, ErrAgentCommandWrongSession) || replayed || p.saveCalls != saves || !reflect.DeepEqual(before, p.state) {
		t.Fatalf("revoked-session retry returned replay=%v error=%v saves=%d", replayed, err, p.saveCalls)
	}
}

func TestAgentCommandRejectsSessionBeforeStaleFence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		want       error
		changeBind func(*CanonicalStore, testAuthenticatedFrame, *canonicalTestClock) error
	}{
		{
			name: "rotated generation",
			want: ErrAgentCommandStaleGeneration,
			changeBind: func(s *CanonicalStore, frame testAuthenticatedFrame, _ *canonicalTestClock) error {
				return s.RotateAgentSessionKey(frame.address, sessionTestKey(0x6a))
			},
		},
		{
			name: "revoked session",
			want: ErrAgentCommandWrongSession,
			changeBind: func(s *CanonicalStore, frame testAuthenticatedFrame, _ *canonicalTestClock) error {
				return s.ReplaceAgentSessionAddress(frame.address, "executor-new", sessionTestKey(0x72), "test revoke")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := claimedRecoveryState()
			clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
			p := &canonicalMemory{state: previous}
			s := newCanonicalWithTestClock(t, p, clock)
			defer func() { _ = s.Close() }()
			frame := authenticatedFrameForRun(previous, "run-1", "stale-session-and-fence", "coordd")
			if _, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, frame.envelope.MessageID)); err != nil {
				t.Fatalf("seed receipt: %v", err)
			}
			clock.advance(time.Second)
			if err := tc.changeBind(s, frame, clock); err != nil {
				t.Fatalf("change session binding: %v", err)
			}
			next := ownershipTransferCandidate(t, s.state, "run-1", roles.Executor, 1, 2)
			completeRecoveryRun(&next, "recovery-session", 2)
			if err := s.UpdateCanonical(func(state *CanonicalState) error { *state = next; return nil }); err != nil {
				t.Fatalf("recovery transfer: %v", err)
			}
			before, err := copyCanonicalState(p.state)
			if err != nil {
				t.Fatal(err)
			}
			saves := p.saveCalls
			_, replayed, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, frame.envelope.MessageID))
			if !errors.Is(err, tc.want) || replayed || p.saveCalls != saves || !reflect.DeepEqual(before, p.state) {
				t.Fatalf("stale session + Fence returned replay=%v error=%v saves=%d", replayed, err, p.saveCalls)
			}
		})
	}
}

func TestAgentCommandReplayWithDifferentExpectedRevisionRefused(t *testing.T) {
	state := taskStateWithRun()
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, clock)
	frame := validAgentCommandFrame(state, "agent-message")
	if _, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, frame.envelope.MessageID)); err != nil {
		t.Fatal(err)
	}
	if _, replayed, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 9, applyTestAgentCommand(clock, frame.envelope.MessageID)); !errors.Is(err, ErrAgentCommandReplayMismatch) || replayed {
		t.Fatalf("different expectedRevision replay=%v err=%v", replayed, err)
	}

	if p.saveCalls != 1 {
		t.Fatalf("replay mismatch wrote state: saves=%d", p.saveCalls)
	}
}

func TestAgentCommandReplayAfterRevisionGrowthReturnsPriorReceipt(t *testing.T) {
	state := taskStateWithRun()
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, clock)
	frame := validAgentCommandFrame(state, "agent-message")
	first, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, frame.envelope.MessageID))
	if err != nil {
		t.Fatal(err)
	}
	if s.state.Tasks[first.TaskID].Revision != first.CommittedRevision {
		t.Fatal("fixture did not grow Task revision during initial command commit")
	}
	saves := p.saveCalls
	replayed, wasReplay, err := s.ApplyAgentCommand(frame, first.TaskID, first.ExpectedRevision, func(*CanonicalState, fsm.Run) (InboxReceipt, error) {
		return InboxReceipt{}, fmt.Errorf("replay invoked transition handler")
	})
	if err != nil || !wasReplay || !reflect.DeepEqual(first, replayed) || p.saveCalls != saves {
		t.Fatalf("replay after revision growth receipt=%+v replay=%v saves=%d err=%v", replayed, wasReplay, p.saveCalls, err)
	}
}

func TestAgentCommandRejectsStaleRevision(t *testing.T) {
	state := taskStateWithRun()
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, clock)
	frame := validAgentCommandFrame(state, "new-command")
	firstFrame := validAgentCommandFrame(state, "first")
	if _, _, err := s.ApplyAgentCommand(firstFrame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, firstFrame.envelope.MessageID)); err != nil {
		t.Fatal(err)
	}
	frame.envelope.MessageID = "different-message"
	_, replayed, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, frame.envelope.MessageID))
	var conflict *RevisionConflictError
	if replayed || !errors.As(err, &conflict) || p.saveCalls != 1 {
		t.Fatalf("new stale-revision command replay=%v err=%v saves=%d", replayed, err, p.saveCalls)
	}
}

func TestAgentCommandRejectsForbiddenTransition(t *testing.T) {
	state := taskStateWithRun()
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, &canonicalTestClock{now: time.Unix(5000, 0).UTC()})
	frame := validAgentCommandFrame(state, "forbidden")
	before, _ := copyCanonicalState(p.state)
	_, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, func(*CanonicalState, fsm.Run) (InboxReceipt, error) {
		return InboxReceipt{}, errors.New("transition denied")
	})
	if !errors.Is(err, ErrAgentCommandTransitionForbidden) || p.saveCalls != 0 || !reflect.DeepEqual(before, p.state) {
		t.Fatalf("forbidden transition error=%v saves=%d", err, p.saveCalls)
	}
}

func TestAgentCommandBoundaryResolvesActorInsideLock(t *testing.T) {
	state := taskStateWithRun()
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, clock)
	frame := validAgentCommandFrame(state, "lock-check")
	_, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, func(next *CanonicalState, run fsm.Run) (InboxReceipt, error) {
		if s.mu.TryLock() {
			s.mu.Unlock()
			return InboxReceipt{}, errors.New("store lock was not held while resolving persisted Run actor")
		}
		return applyTestAgentCommand(clock, frame.envelope.MessageID)(next, run)
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.state.Inbox[InboxKey("agent-executor", "lock-check")].ActorID != "agent-executor" {
		t.Fatal("InboxKey actor did not come from persisted RoleSelection")
	}
}

func TestAgentCommandBoundaryRejectsKnownRunIDWithoutAuthenticatedSession(t *testing.T) {
	state := taskStateWithRun()
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, &canonicalTestClock{now: time.Unix(5000, 0).UTC()})
	frame := validAgentCommandFrame(state, "no-session")
	frame.to = ""
	_, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, func(*CanonicalState, fsm.Run) (InboxReceipt, error) { return InboxReceipt{}, nil })
	if !errors.Is(err, ErrAgentCommandWrongRecipient) || p.saveCalls != 0 {
		t.Fatalf("unauthenticated frame error=%v saves=%d", err, p.saveCalls)
	}
}

func TestAgentCommandRejectsUnknownRunBeforeReplayLookup(t *testing.T) {
	state := taskStateWithRun()
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, clock)
	frame := validAgentCommandFrame(state, "repeat")
	if _, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, frame.envelope.MessageID)); err != nil {
		t.Fatal(err)
	}
	frame.envelope.Run = "unknown-run"
	_, replayed, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, frame.envelope.MessageID))
	if !errors.Is(err, ErrAgentCommandNotCurrentWriter) || replayed || p.saveCalls != 1 {
		t.Fatalf("unknown Run was treated as replay=%v error=%v saves=%d", replayed, err, p.saveCalls)
	}
}

func TestAgentCommandBoundaryRejectsInboxKeyBuiltFromUnverifiedActor(t *testing.T) {
	state := taskStateWithRun()
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, &canonicalTestClock{now: time.Unix(5000, 0).UTC()})
	frame := validAgentCommandFrame(state, "actor-boundary")
	frame.address = "forged-session"
	_, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, func(*CanonicalState, fsm.Run) (InboxReceipt, error) { return InboxReceipt{}, nil })
	if !errors.Is(err, ErrAgentCommandUntrustedSignature) || len(p.state.Inbox) != 0 || p.saveCalls != 0 {
		t.Fatalf("unverified actor affected Inbox: error=%v inbox=%v saves=%d", err, p.state.Inbox, p.saveCalls)
	}
}

func TestAgentCommandAppliesRoleIndependenceFromCommittedRun(t *testing.T) {
	state := canonicalRoleTransfer(t, canonicalIndependenceBase(task.Review), roles.Reviewer, "review-run", "agent-reviewer", "review-model")
	p := &canonicalMemory{state: state}
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	s := newCanonicalWithTestClock(t, p, clock)
	frame := authenticatedFrameForRun(state, "review-run", "review-command", "coordd")
	id := taskid.ID(frame.envelope.Task)
	if _, _, err := s.ApplyAgentCommand(frame, id, state.Tasks[id].Revision, applyTestAgentCommand(clock, frame.envelope.MessageID)); err != nil {
		t.Fatalf("independent Reviewer command rejected: %v", err)
	}
	if got := p.state.Inbox[InboxKey("agent-reviewer", frame.envelope.MessageID)].ActorID; got != "agent-reviewer" {
		t.Fatalf("Inbox actor=%q, want persisted Reviewer AgentID", got)
	}
}

func TestAgentCommandRejectsRoleIdentityCollisionBeforeReplay(t *testing.T) {
	state := canonicalRoleTransfer(t, canonicalIndependenceBase(task.Review), roles.Reviewer, "review-run", "agent-reviewer", "review-model")
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, &canonicalTestClock{now: time.Unix(5000, 0).UTC()})
	frame := authenticatedFrameForRun(state, "review-run", "independence", "coordd")
	s.mu.Lock()
	run := s.state.Runs["review-run"]
	run.RoleSelection.AgentID = "agent-current"
	run.RoleHistory[len(run.RoleHistory)-1].AgentID = "agent-current"
	s.state.Runs[run.RunID] = run
	session := s.state.AgentSessions[run.OwnerSession]
	session.AgentID = "agent-current"
	s.state.AgentSessions[run.OwnerSession] = session
	s.mu.Unlock()
	_, replayed, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), state.Tasks[taskid.ID(frame.envelope.Task)].Revision, func(*CanonicalState, fsm.Run) (InboxReceipt, error) { return InboxReceipt{}, nil })
	if !errors.Is(err, ErrNoIndependentReviewer) || replayed || p.saveCalls != 0 {
		t.Fatalf("identity collision replay=%v error=%v saves=%d", replayed, err, p.saveCalls)
	}
}

func TestAgentCommandRoleIdentityCollisionRejectsPriorReceiptBeforeReplay(t *testing.T) {
	state := canonicalRoleTransfer(t, canonicalIndependenceBase(task.Review), roles.Reviewer, "review-run", "agent-reviewer", "review-model")
	p := &canonicalMemory{state: state}
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	s := newCanonicalWithTestClock(t, p, clock)
	frame := authenticatedFrameForRun(state, "review-run", "independence-after-commit", "coordd")
	id := taskid.ID(frame.envelope.Task)

	if _, replayed, err := s.ApplyAgentCommand(frame, id, state.Tasks[id].Revision, applyTestAgentCommand(clock, frame.envelope.MessageID)); err != nil || replayed {
		t.Fatalf("initial independent command replay=%v error=%v", replayed, err)
	}

	s.mu.Lock()
	run := s.state.Runs["executor-current"]
	run.RoleSelection.AgentID = "agent-reviewer"
	run.RoleHistory[len(run.RoleHistory)-1].AgentID = "agent-reviewer"
	s.state.Runs[run.RunID] = run
	beforeState, err := copyCanonicalState(s.state)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	beforeDurable, err := copyCanonicalState(p.state)
	if err != nil {
		t.Fatal(err)
	}
	saves := p.saveCalls
	handlerCalled := false
	_, replayed, err := s.ApplyAgentCommand(frame, id, state.Tasks[id].Revision, func(state *CanonicalState, run fsm.Run) (InboxReceipt, error) {
		handlerCalled = true
		return applyTestAgentCommand(clock, frame.envelope.MessageID)(state, run)
	})
	if !errors.Is(err, ErrNoIndependentReviewer) || replayed || handlerCalled || p.saveCalls != saves ||
		!reflect.DeepEqual(beforeState, s.state) || !reflect.DeepEqual(beforeDurable, p.state) {
		t.Fatalf("conflicting role replay=%v handler=%v error=%v saves=%d want=%d", replayed, handlerCalled, err, p.saveCalls, saves)
	}
}

func TestAgentCommandRejectsRoleStageMismatch(t *testing.T) {
	state := taskStateWithRun()
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, clock)
	frame := validAgentCommandFrame(state, "agent-message")
	if _, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, frame.envelope.MessageID)); err != nil {
		t.Fatal(err)
	}
	// Advance the Task to REVIEW while the receipt remains. The Executor's old
	// frame must fail role/Stage authorization before replay returns that receipt.
	id := taskid.ID(frame.envelope.Task)
	if err := s.UpdateCanonical(func(state *CanonicalState) error {
		value := state.Tasks[id]
		value.Revision++
		value.LastEventSeq++
		value.UpdatedAt = clock.Now().Add(time.Second)
		value.StagePlan.Stages = []task.Stage{task.Execution, task.Review, task.Audit}
		stage := task.Review
		value.CurrentStage = &stage
		state.Tasks[id] = value
		state.Events = append(state.Events, task.DomainEvent{
			ID: task.EventID(id, value.LastEventSeq), TaskID: id, RunID: "run-1",
			EventSeq: value.LastEventSeq, TaskRevision: value.Revision, TaskFence: value.Fence,
			Type: task.EventType("TEST_STAGE_ADVANCED"), CreatedAt: value.UpdatedAt,
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, replayed, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, frame.envelope.MessageID))
	if replayed || !errors.Is(err, ErrAgentCommandStageRoleMismatch) {
		t.Fatalf("role-changed replay=%v err=%v", replayed, err)
	}
}

func TestAgentCommandRejectsInboxWithoutCommittedAcknowledgement(t *testing.T) {
	state := taskStateWithRun()
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, clock)
	frame := validAgentCommandFrame(state, "agent-message")
	_, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, func(state *CanonicalState, run fsm.Run) (InboxReceipt, error) {
		apply := applyTestAgentCommand(clock, frame.envelope.MessageID)
		receipt, err := apply(state, run)
		if err != nil {
			return InboxReceipt{}, err
		}
		delete(state.Outbox, run.RunID)
		return receipt, nil
	})
	if !errors.Is(err, ErrAgentCommandTransitionForbidden) || p.saveCalls != 0 {
		t.Fatalf("missing same-commit acknowledgement error=%v saves=%d", err, p.saveCalls)
	}
}

func TestAgentCommandRejectsMissingAgentIDForEveryRole(t *testing.T) {
	for _, tc := range []struct {
		name  string
		role  roles.Role
		stage task.Stage
		runID string
	}{
		{name: "executor", role: roles.Executor, stage: task.Execution, runID: "run-1"},
		{name: "reviewer", role: roles.Reviewer, stage: task.Review, runID: "review-run"},
		{name: "auditor", role: roles.Auditor, stage: task.Audit, runID: "audit-run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := taskStateWithRun()
			if tc.role != roles.Executor {
				state = canonicalRoleTransfer(t, canonicalIndependenceBase(tc.stage), tc.role, tc.runID, "agent-role", "model")
			}
			frame := authenticatedFrameForRun(state, tc.runID, "missing-agent", "coordd")
			id := taskid.ID(frame.envelope.Task)
			p := &canonicalMemory{state: state}
			s := newCanonicalWithTestClock(t, p, &canonicalTestClock{now: time.Unix(5000, 0).UTC()})
			s.mu.Lock()
			run := s.state.Runs[tc.runID]
			run.RoleSelection.AgentID = ""
			run.RoleHistory[len(run.RoleHistory)-1].AgentID = ""
			s.state.Runs[run.RunID] = run
			s.mu.Unlock()
			_, _, err := s.ApplyAgentCommand(frame, id, state.Tasks[id].Revision, func(*CanonicalState, fsm.Run) (InboxReceipt, error) { return InboxReceipt{}, nil })
			if !errors.Is(err, ErrAgentCommandMissingAgentID) || p.saveCalls != 0 {
				t.Fatalf("missing AgentID error=%v saves=%d", err, p.saveCalls)
			}
		})
	}
}

func TestAgentCommandRejectsSignedFrameFromDifferentKeyAtCurrentGeneration(t *testing.T) {
	state := taskStateWithRun()
	frame := validAgentCommandFrame(state, "attacker-key-current-generation")
	attackerPrivate := sessionTestPrivateKey(0x7f)
	frame.privateKey = attackerPrivate
	frame.key = attackerPrivate.Public().(ed25519.PublicKey)

	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, &canonicalTestClock{now: time.Unix(5000, 0).UTC()})
	before, err := copyCanonicalState(s.state)
	if err != nil {
		t.Fatal(err)
	}
	handlerCalled := false
	_, replayed, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, func(*CanonicalState, fsm.Run) (InboxReceipt, error) {
		handlerCalled = true
		return InboxReceipt{}, nil
	})
	if !errors.Is(err, ErrAgentCommandUntrustedSignature) || replayed || handlerCalled || p.saveCalls != 0 ||
		!reflect.DeepEqual(before, s.state) || !reflect.DeepEqual(before, p.state) {
		t.Fatalf("attacker-signed frame error=%v replay=%v handler=%v saves=%d", err, replayed, handlerCalled, p.saveCalls)
	}
}

func TestAgentCommandRejectsSessionAgentIDMismatchBeforeReplay(t *testing.T) {
	state := taskStateWithRun()
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, clock)
	frame := validAgentCommandFrame(state, "session-agent-id-mismatch")
	if _, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, frame.envelope.MessageID)); err != nil {
		t.Fatalf("seed receipt: %v", err)
	}
	beforeDurable, err := copyCanonicalState(p.state)
	if err != nil {
		t.Fatal(err)
	}
	// Defensively model the binding changing to another AgentID while this
	// Executor Run still carries its original identity. Normal state validation
	// rejects this mismatch; the command boundary must also fail closed.
	s.mu.Lock()
	detached, err := copyCanonicalState(s.state)
	if err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.state = detached
	session := s.state.AgentSessions[frame.address]
	session.AgentID = "agent-rebound"
	s.state.AgentSessions[frame.address] = session
	beforeCommand, err := copyCanonicalState(s.state)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	saves := p.saveCalls
	_, replayed, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, frame.envelope.MessageID))
	if err == nil || replayed || p.saveCalls != saves ||
		!reflect.DeepEqual(beforeCommand, s.state) || !reflect.DeepEqual(beforeDurable, p.state) {
		t.Fatalf("AgentID mismatch returned replay=%v error=%v saves=%d", replayed, err, p.saveCalls)
	}
}

func TestAgentCommandRejectsRunGenerationAfterKeyRotation(t *testing.T) {
	state := taskStateWithRun()
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, clock)
	frame := validAgentCommandFrame(state, "run-generation-after-rotation")
	if _, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, applyTestAgentCommand(clock, frame.envelope.MessageID)); err != nil {
		t.Fatalf("seed receipt: %v", err)
	}
	newPrivate := sessionTestPrivateKey(0x6a)
	clock.advance(time.Second)
	if err := s.RotateAgentSessionKey(frame.address, newPrivate.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	frame.privateKey = newPrivate
	frame.key = newPrivate.Public().(ed25519.PublicKey)
	frame.gen = 2
	before, err := copyCanonicalState(p.state)
	if err != nil {
		t.Fatal(err)
	}
	saves := p.saveCalls
	handlerCalled := false
	_, replayed, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, func(*CanonicalState, fsm.Run) (InboxReceipt, error) {
		handlerCalled = true
		return InboxReceipt{}, nil
	})
	if !errors.Is(err, ErrAgentCommandStaleGeneration) || replayed || handlerCalled || p.saveCalls != saves || !reflect.DeepEqual(before, p.state) {
		t.Fatalf("stale Run generation retry error=%v replay=%v handler=%v saves=%d", err, replayed, handlerCalled, p.saveCalls)
	}
}

func TestAgentCommandRoleStageMappingRejectsWrongRoleAndStage(t *testing.T) {
	taskValue := task.Task{Status: task.InProgress}
	execSelection := roles.Selection{AgentID: "agent", Profile: roles.Profile{Role: roles.Executor}}
	run := fsm.Run{RequiredRole: roles.Requirement{Role: roles.Executor}, RoleSelection: &execSelection}
	if err := validateAgentCommandRoleStage(taskValue, run); !errors.Is(err, ErrAgentCommandStageRoleMismatch) {
		t.Fatalf("missing current stage error=%v", err)
	}
	stage := task.Review
	taskValue.CurrentStage = &stage
	if err := validateAgentCommandRoleStage(taskValue, run); !errors.Is(err, ErrAgentCommandStageRoleMismatch) {
		t.Fatalf("wrong role/stage error=%v", err)
	}
	run.RequiredRole.Role = roles.Reviewer
	if err := validateAgentCommandRoleStage(taskValue, run); !errors.Is(err, ErrAgentCommandWrongRole) {
		t.Fatalf("required role mismatch error=%v", err)
	}
}

func TestAgentCommandRejectsUntrustedKeyForCurrentSession(t *testing.T) {
	state := taskStateWithRun()
	frame := validAgentCommandFrame(state, "bad-key")
	frame.key = sessionTestKey(0x7f)
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, &canonicalTestClock{now: time.Unix(5000, 0).UTC()})
	_, _, err := s.ApplyAgentCommand(frame, taskid.ID(frame.envelope.Task), 2, func(*CanonicalState, fsm.Run) (InboxReceipt, error) { return InboxReceipt{}, nil })
	if !errors.Is(err, ErrAgentCommandUntrustedSignature) || p.saveCalls != 0 {
		t.Fatalf("wrong verified key error=%v saves=%d", err, p.saveCalls)
	}
}

func TestAgentCommandRejectsMissingRoleSelectionBeforeRoleStage(t *testing.T) {
	state := taskStateWithRun()
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, clock)
	detached, err := copyCanonicalState(s.state)
	if err != nil {
		t.Fatal(err)
	}
	s.state = detached
	run := s.state.Runs["run-1"]
	run.RoleSelection = nil
	s.state.Runs[run.RunID] = run
	before, err := copyCanonicalState(s.state)
	if err != nil {
		t.Fatal(err)
	}
	diskBefore, err := copyCanonicalState(p.state)
	if err != nil {
		t.Fatal(err)
	}
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	graphBefore, err := s.DiscussionGraph(id)
	if err != nil {
		t.Fatal(err)
	}
	frame := validAgentCommandFrame(state, "missing-role-selection")
	called := false
	_, replayed, err := s.ApplyAgentCommand(frame, id, state.Tasks[id].Revision, func(*CanonicalState, fsm.Run) (InboxReceipt, error) {
		called = true
		return InboxReceipt{}, nil
	})
	if !errors.Is(err, ErrAgentCommandWrongSession) || replayed || called {
		t.Fatalf("missing RoleSelection err=%v replayed=%v handler=%v; want WrongSession before role-stage validation", err, replayed, called)
	}
	assertAgentCommandRejectionUnchanged(t, s, p, id, clock, before, diskBefore, graphBefore, 0)
}

func TestAgentCommandRejectsWhitespaceAgentIDWithoutWriting(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*CanonicalState)
	}{
		{"session_AgentID", func(state *CanonicalState) {
			for address, session := range state.AgentSessions {
				if session.AgentID == "agent-executor" {
					session.AgentID = " \t"
					state.AgentSessions[address] = session
				}
			}
		}},
		{"run_role_selection_AgentID", func(state *CanonicalState) {
			run := state.Runs["run-1"]
			run.RoleSelection.AgentID = " \t"
			state.Runs[run.RunID] = run
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := taskStateWithRun()
			clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
			p := &canonicalMemory{state: state}
			s := newCanonicalWithTestClock(t, p, clock)
			detached, err := copyCanonicalState(s.state)
			if err != nil {
				t.Fatal(err)
			}
			s.state = detached
			tc.mutate(&s.state)
			before, err := copyCanonicalState(s.state)
			if err != nil {
				t.Fatal(err)
			}
			diskBefore, err := copyCanonicalState(p.state)
			if err != nil {
				t.Fatal(err)
			}
			id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
			graphBefore, err := s.DiscussionGraph(id)
			if err != nil {
				t.Fatal(err)
			}
			frame := validAgentCommandFrame(state, "whitespace-agent-id-"+tc.name)
			called := false
			_, replayed, err := s.ApplyAgentCommand(frame, id, state.Tasks[id].Revision, func(*CanonicalState, fsm.Run) (InboxReceipt, error) {
				called = true
				return InboxReceipt{}, nil
			})
			if !errors.Is(err, ErrAgentCommandMissingAgentID) || replayed || called {
				t.Fatalf("whitespace AgentID err=%v replayed=%v handler=%v; want MissingAgentID before replay/handler", err, replayed, called)
			}
			assertAgentCommandRejectionUnchanged(t, s, p, id, clock, before, diskBefore, graphBefore, 0)
		})
	}
}

func assertAgentCommandRejectionUnchanged(t *testing.T, s *CanonicalStore, p *canonicalMemory, id taskid.ID, clock *canonicalTestClock, before, diskBefore CanonicalState, graphBefore task.DiscussionGraph, saves int) {
	t.Helper()
	after, err := s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Tasks[id], after.Tasks[id]) {
		t.Fatal("Task changed after rejected command")
	}
	if !reflect.DeepEqual(before.Events, after.Events) {
		t.Fatal("Events changed after rejected command")
	}
	if !reflect.DeepEqual(before.Inbox, after.Inbox) {
		t.Fatal("Inbox receipt changed after rejected command")
	}
	if !reflect.DeepEqual(before.Outbox, after.Outbox) {
		t.Fatal("Outbox changed after rejected command")
	}
	if p.saveCalls != saves {
		t.Fatalf("durable saves=%d, want %d", p.saveCalls, saves)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("canonical memory state changed after rejected command")
	}
	diskAfter, err := copyCanonicalState(p.state)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(diskBefore, diskAfter) {
		t.Fatal("durable state changed after rejected command")
	}
	graphAfter, err := s.DiscussionGraph(id)
	if err != nil || !reflect.DeepEqual(graphBefore, graphAfter) {
		t.Fatalf("Discussion Graph changed after rejection: %v", err)
	}
	reopened := newCanonicalWithTestClock(t, &canonicalMemory{state: p.state}, clock)
	reopenedState, err := reopened.CanonicalSnapshot()
	if err != nil || !reflect.DeepEqual(diskBefore, reopenedState) {
		t.Fatalf("reopened durable state differs: %v", err)
	}
	reopenedGraph, err := reopened.DiscussionGraph(id)
	if err != nil || !reflect.DeepEqual(graphBefore, reopenedGraph) {
		t.Fatalf("reopened Discussion Graph differs: %v", err)
	}
}
