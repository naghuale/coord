package store

import (
	"bytes"
	"coord/ownercredential"
	"coord/task"
	"coord/taskid"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

type ownerCommandFixture struct {
	t           *testing.T
	p           *canonicalMemory
	s           *CanonicalStore
	credentials map[string]ownercredential.Credential
	private     map[string]ed25519.PrivateKey
}

func newOwnerCommandFixture(t *testing.T) *ownerCommandFixture {
	t.Helper()
	f := &ownerCommandFixture{t: t, p: &canonicalMemory{state: EmptyCanonicalState()}, credentials: map[string]ownercredential.Credential{}, private: map[string]ed25519.PrivateKey{}}
	for _, id := range []string{"owner-primary", "owner-backup"} {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key := append([]byte(nil), public...)
		f.credentials[id] = ownercredential.Credential{ID: id, Algorithm: ownercredential.EdDSA, PublicKey: key, Fingerprint: ownercredential.Fingerprint(ownercredential.EdDSA, key)}
		f.private[id] = private
	}
	var err error
	f.s, err = NewCanonical(f.p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.s.Close() })
	if _, err := f.s.BootstrapOwnerCredentials("owner-1", "bootstrap-1", []ownercredential.Credential{f.credentials["owner-primary"], f.credentials["owner-backup"]}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ConfigureOwnerWebAuthnPolicy("localhost", "http://localhost:8090"); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *ownerCommandFixture) command(kind, messageID, signer string, revision uint64, payload []byte) ownercredential.Command {
	f.t.Helper()
	command, err := f.s.IssueOwnerCommandChallenge(ownercredential.CommandRequest{Kind: kind, MessageID: messageID, PrincipalID: "owner-1", CredentialID: signer, ExpectedCredentialRevision: revision, Payload: payload})
	if err != nil {
		f.t.Fatal(err)
	}
	return command
}

func assertionFor(command ownercredential.Command, private ed25519.PrivateKey, count uint32) ownercredential.Assertion {
	return assertionWithWebAuthnFields(command, private, count, command.WebAuthnChallenge, "webauthn.get", "localhost", 0x05)
}

func assertionWithWebAuthnFields(command ownercredential.Command, private ed25519.PrivateKey, count uint32, challenge, clientType, rpID string, flags byte) ownercredential.Assertion {
	clientData, _ := json.Marshal(map[string]any{"type": clientType, "challenge": challenge, "origin": "http://localhost:8090"})
	authData := make([]byte, 37)
	rpHash := sha256.Sum256([]byte(rpID))
	copy(authData[:32], rpHash[:])
	authData[32] = flags
	binary.BigEndian.PutUint32(authData[33:], count)
	clientHash := sha256.Sum256(clientData)
	signed := append(append([]byte(nil), authData...), clientHash[:]...)
	return ownercredential.Assertion{ClientDataJSON: clientData, AuthenticatorData: authData, Signature: ed25519.Sign(private, signed)}
}

func assertOwnerCommandRejectedWithoutWrite(t *testing.T, f *ownerCommandFixture, command ownercredential.Command, assertion ownercredential.Assertion, want error) {
	t.Helper()
	before := ownerStateBytes(t, f.s)
	saves := f.p.saveCalls
	if _, replayed, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertion); !errors.Is(err, want) || replayed {
		t.Fatalf("command replayed=%v error=%v want=%v", replayed, err, want)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) || f.p.saveCalls != saves {
		t.Fatalf("rejected command changed canonical state or saved: saves=%d want=%d", f.p.saveCalls, saves)
	}
}

func edDSACOSE(publicKey []byte) []byte {
	encoded := []byte{0xa4, 0x01, 0x01, 0x03, 0x27, 0x20, 0x06, 0x21, 0x58, 0x20}
	return append(encoded, publicKey...)
}

func registerPayload(t *testing.T, id string, cose []byte) []byte {
	t.Helper()
	payload, err := json.Marshal(ownercredential.RegisterCredentialPayload{CredentialID: id, COSEPublicKey: cose})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func ownerStateBytes(t *testing.T, s *CanonicalStore) []byte {
	t.Helper()
	state, err := s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestOwnerCredentialSignedRevokeIsAtomicAndExactRetryReturnsReceipt(t *testing.T) {
	f := newOwnerCommandFixture(t)
	payload, _ := json.Marshal(ownercredential.RevokeCredentialPayload{CredentialID: "owner-backup"})
	command := f.command(ownercredential.CommandRevokeCredential, "revoke-backup-1", "owner-primary", 1, payload)
	assertion := assertionFor(command, f.private["owner-primary"], 1)
	beforeSaves := f.p.saveCalls
	receipt, replayed, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertion)
	if err != nil || replayed {
		t.Fatalf("receipt=%+v replayed=%v err=%v", receipt, replayed, err)
	}
	if f.p.saveCalls != beforeSaves+1 {
		t.Fatalf("saves=%d, want one atomic save", f.p.saveCalls)
	}
	state, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	aggregate := state.OwnerCredentials["owner-1"]
	if aggregate.CredentialRevision != 2 || aggregate.Sequence != 2 || aggregate.Credentials["owner-backup"].RevokedAt.IsZero() || aggregate.Credentials["owner-primary"].SignCount != 1 || aggregate.Receipts[receipt.MessageID] != receipt || aggregate.Events[1].Nonce != command.Nonce {
		t.Fatalf("revoke effect/event/receipt not committed together: %+v", aggregate)
	}
	committedBytes := ownerStateBytes(t, f.s)
	retry, replayed, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertion)
	if err != nil || !replayed || retry != receipt || f.p.saveCalls != beforeSaves+1 || !bytes.Equal(committedBytes, ownerStateBytes(t, f.s)) {
		t.Fatalf("exact lost-response retry was not idempotent: receipt=%+v replayed=%v saves=%d err=%v", retry, replayed, f.p.saveCalls, err)
	}
}

func TestOwnerCommandExactRetryAfterRestartReturnsOnlyDurableReceipt(t *testing.T) {
	f := newOwnerCommandFixture(t)
	payload, _ := json.Marshal(ownercredential.RevokeCredentialPayload{CredentialID: "owner-backup"})
	command := f.command(ownercredential.CommandRevokeCredential, "revoke-after-restart", "owner-primary", 1, payload)
	assertion := assertionFor(command, f.private["owner-primary"], 1)
	receipt, replayed, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertion)
	if err != nil || replayed {
		t.Fatalf("initial command receipt=%+v replayed=%v err=%v", receipt, replayed, err)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = NewCanonical(f.p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.ConfigureOwnerWebAuthnPolicy("localhost", "http://localhost:8090"); err != nil {
		t.Fatal(err)
	}
	saves := f.p.saveCalls
	retry, replayed, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertion)
	if err != nil || !replayed || retry != receipt || f.p.saveCalls != saves {
		t.Fatalf("post-restart exact retry receipt=%+v replayed=%v saves=%d err=%v", retry, replayed, f.p.saveCalls, err)
	}
}

func TestOwnerCredentialRejectsInvalidSignaturePayloadSwapPrincipalSwapAndUnknownKey(t *testing.T) {
	f := newOwnerCommandFixture(t)
	payload, _ := json.Marshal(ownercredential.RevokeCredentialPayload{CredentialID: "owner-backup"})
	command := f.command(ownercredential.CommandRevokeCredential, "command-1", "owner-primary", 1, payload)
	before := ownerStateBytes(t, f.s)
	badSignature := assertionFor(command, f.private["owner-primary"], 1)
	badSignature.Signature[0] ^= 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, badSignature); err == nil {
		t.Fatal("invalid signature accepted")
	}
	changed := command
	changed.Payload = []byte(`{"credential_id":"owner-primary"}`)
	if _, _, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, changed, assertionFor(command, f.private["owner-primary"], 1)); !errors.Is(err, ErrOwnerChallengeUnknown) {
		t.Fatalf("payload swap error=%v", err)
	}
	changed = command
	changed.PrincipalID = "owner-2"
	if _, _, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, changed, assertionFor(command, f.private["owner-primary"], 1)); !errors.Is(err, ErrOwnerChallengeUnknown) {
		t.Fatalf("principal swap error=%v", err)
	}
	if _, err := f.s.IssueOwnerCommandChallenge(ownercredential.CommandRequest{Kind: ownercredential.CommandRevokeCredential, MessageID: "unknown", PrincipalID: "owner-1", CredentialID: "not-registered", ExpectedCredentialRevision: 1, Payload: payload}); !errors.Is(err, ErrOwnerCredentialUnknown) {
		t.Fatalf("unknown credential error=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) || f.p.saveCalls != 1 {
		t.Fatal("refused Owner command changed canonical state")
	}
}

func TestOwnerCredentialRejectsStaleRevisionNonceReplayAndLastKeyRevocation(t *testing.T) {
	f := newOwnerCommandFixture(t)
	backupPayload, _ := json.Marshal(ownercredential.RevokeCredentialPayload{CredentialID: "owner-backup"})
	first := f.command(ownercredential.CommandRevokeCredential, "revoke-backup", "owner-primary", 1, backupPayload)
	if _, _, err := f.s.ApplyOwnerCredentialCommand(first.ChallengeID, first, assertionFor(first, f.private["owner-primary"], 1)); err != nil {
		t.Fatal(err)
	}
	lastPayload, _ := json.Marshal(ownercredential.RevokeCredentialPayload{CredentialID: "owner-primary"})
	last := f.command(ownercredential.CommandRevokeCredential, "revoke-last", "owner-primary", 2, lastPayload)
	before := ownerStateBytes(t, f.s)
	if _, _, err := f.s.ApplyOwnerCredentialCommand(last.ChallengeID, last, assertionFor(last, f.private["owner-primary"], 2)); err == nil {
		t.Fatal("last active credential was revoked")
	}
	stale := first
	stale.MessageID = "stale-message"
	if _, _, err := f.s.ApplyOwnerCredentialCommand(first.ChallengeID, stale, assertionFor(first, f.private["owner-primary"], 1)); !errors.Is(err, ErrOwnerChallengeUnknown) {
		t.Fatalf("stale altered command error=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("rejected stale/last-key command changed state")
	}
}

func TestOwnerCredentialFailedCanonicalSavePublishesNothing(t *testing.T) {
	f := newOwnerCommandFixture(t)
	payload, _ := json.Marshal(ownercredential.RevokeCredentialPayload{CredentialID: "owner-backup"})
	command := f.command(ownercredential.CommandRevokeCredential, "revoke-backup", "owner-primary", 1, payload)
	before := ownerStateBytes(t, f.s)
	f.p.fail = true
	if _, _, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertionFor(command, f.private["owner-primary"], 1)); !errors.Is(err, ErrPersistence) {
		t.Fatalf("save failure=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) || f.p.state.OwnerCredentials["owner-1"].CredentialRevision != 1 {
		t.Fatal("failed canonical Save published credential, event, nonce, or receipt")
	}
}

func TestOwnerTaskCreateFailedSavePublishesNoTaskOrOwnerReceipt(t *testing.T) {
	f := newOwnerCommandFixture(t)
	payload, _ := json.Marshal(ownercredential.CreateTaskPayload{Title: "failed commit", StagePlan: task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion}})
	command := f.command(ownercredential.CommandCreateTask, "create-failed", "owner-primary", 1, payload)
	before := ownerStateBytes(t, f.s)
	f.p.fail = true
	if _, _, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertionFor(command, f.private["owner-primary"], 1)); !errors.Is(err, ErrPersistence) {
		t.Fatalf("failed Task create Save error=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) || len(f.p.state.Tasks) != 0 || len(f.p.state.OwnerCredentials["owner-1"].Receipts) != 1 {
		t.Fatal("failed Task create Save published partial Task, nonce, event, or receipt")
	}
}

func TestOwnerCredentialWriteCannotBypassAuthenticatedStoreBoundary(t *testing.T) {
	f := newOwnerCommandFixture(t)
	before := ownerStateBytes(t, f.s)
	err := f.s.UpdateCanonical(func(state *CanonicalState) error {
		aggregate := state.OwnerCredentials["owner-1"]
		aggregate.Credentials["forged"] = aggregate.Credentials["owner-primary"]
		state.OwnerCredentials["owner-1"] = aggregate
		return nil
	})
	if !errors.Is(err, ErrOwnerCommandRequired) {
		t.Fatalf("direct write error=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("direct mutation bypassed the authenticated write boundary")
	}

	// Generic Inbox handlers are already-authenticated Agent commands, not an
	// OwnerCredential command boundary; they must not mutate the Owner aggregate.
	if _, _, err := f.s.ApplyInboxCommand("agent-1", "message-1", taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV"), 0, func(state *CanonicalState) (InboxReceipt, error) {
		state.OwnerCredentials["owner-1"] = ownercredential.EmptyAggregate("forged")
		return InboxReceipt{}, nil
	}); !errors.Is(err, ErrOwnerCommandRequired) {
		t.Fatalf("generic Inbox Owner mutation error=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("generic Inbox mutation changed Owner state")
	}
	if err := f.s.UpdateCanonical(func(state *CanonicalState) error {
		id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
		now := f.s.now().UTC()
		stage := task.Execution
		state.Tasks[id] = task.Task{ID: id, Title: "forged", OwnerID: "owner-1", Revision: 1, LastEventSeq: 1, StagePlan: task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion}, CurrentStage: &stage, Status: task.Pending, CreatedAt: now, UpdatedAt: now}
		state.Events = append(state.Events, task.DomainEvent{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1, Type: task.TaskCreated, ActorID: "owner-1", CreatedAt: now})
		return nil
	}); err == nil {
		t.Fatal("generic canonical mutator created a Task without Owner command receipt")
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("unauthenticated Task creation changed canonical state")
	}
}

func TestWriteAuthorityRejectsUnprivilegedLossObserver(t *testing.T) {
	f := newOwnerCommandFixture(t)
	before := ownerStateBytes(t, f.s)
	_, err := f.s.IssueOwnerCommandChallenge(ownercredential.CommandRequest{
		Kind: "assignment.coordinator.loss", MessageID: "loss-attempt", PrincipalID: "owner-1",
		CredentialID: "owner-primary", ExpectedCredentialRevision: 1, Payload: []byte(`{"assignment_id":"assignment-1"}`),
	})
	if !errors.Is(err, ErrOwnerCommandInvalid) {
		t.Fatalf("#93a unexpectedly exposes coordinator-loss command: %v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) || f.p.saveCalls != 1 {
		t.Fatal("rejected unprivileged coordinator-loss request changed canonical state")
	}
}

func TestOwnerCredentialRegistrationAcceptsSupportedCOSEAndBumpsRevision(t *testing.T) {
	f := newOwnerCommandFixture(t)
	newPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cose := append([]byte{0xa4, 0x01, 0x01, 0x03, 0x27, 0x20, 0x06, 0x21, 0x58, 0x20}, newPublic...)
	payload, _ := json.Marshal(ownercredential.RegisterCredentialPayload{CredentialID: "owner-third", COSEPublicKey: cose})
	command := f.command(ownercredential.CommandRegisterCredential, "register-third", "owner-primary", 1, payload)
	receipt, replayed, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertionFor(command, f.private["owner-primary"], 1))
	if err != nil || replayed {
		t.Fatalf("receipt=%+v replayed=%v err=%v", receipt, replayed, err)
	}
	state, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	aggregate := state.OwnerCredentials["owner-1"]
	if aggregate.CredentialRevision != 2 || aggregate.Sequence != 2 || aggregate.Credentials["owner-third"].Fingerprint != ownercredential.Fingerprint(ownercredential.EdDSA, newPublic) || aggregate.Credentials["owner-primary"].SignCount != 1 {
		t.Fatalf("registration did not create public credential and advance revision: %+v", aggregate)
	}
}

func TestCreateTaskGeneratesCanonicalTaskID(t *testing.T) {
	f := newOwnerCommandFixture(t)
	payload, err := json.Marshal(ownercredential.CreateTaskPayload{Title: "signed task", Description: "created by owner", StagePlan: task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion}})
	if err != nil {
		t.Fatal(err)
	}
	command := f.command(ownercredential.CommandCreateTask, "create-1", "owner-primary", 1, payload)
	beforeSaves := f.p.saveCalls
	receipt, replayed, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertionFor(command, f.private["owner-primary"], 1))
	if err != nil || replayed {
		t.Fatalf("receipt=%+v replayed=%v err=%v", receipt, replayed, err)
	}
	if f.p.saveCalls != beforeSaves+1 || receipt.TaskID == "" {
		t.Fatalf("Task and Owner receipts were not saved once: receipt=%+v saves=%d", receipt, f.p.saveCalls-beforeSaves)
	}
	state, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	taskID := taskid.ID(receipt.TaskID)
	created, exists := state.Tasks[taskID]
	if !exists || created.OwnerID != "owner-1" || created.Title != "signed task" || created.Revision != 1 {
		t.Fatalf("authenticated task missing or incorrect: %+v", created)
	}
	inbox, exists := state.Inbox[InboxKey("owner-1", command.MessageID)]
	if !exists || inbox.TaskID != taskID || inbox.OwnerCommandDigest != command.Digest || inbox.OwnerNonce != command.Nonce {
		t.Fatalf("Task Inbox receipt missing Owner binding: %+v", inbox)
	}
	committed := ownerStateBytes(t, f.s)
	retry, replayed, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertionFor(command, f.private["owner-primary"], 1))
	if err != nil || !replayed || retry != receipt || f.p.saveCalls != beforeSaves+1 || !bytes.Equal(committed, ownerStateBytes(t, f.s)) {
		t.Fatalf("exact create retry was not idempotent: receipt=%+v replayed=%v err=%v", retry, replayed, err)
	}
}

func TestOwnerTaskCreateRejectsUnsignedAndPayloadChangedCommandsWithoutCommit(t *testing.T) {
	f := newOwnerCommandFixture(t)
	payload, _ := json.Marshal(ownercredential.CreateTaskPayload{Title: "signed title", StagePlan: task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion}})
	command := f.command(ownercredential.CommandCreateTask, "create-signed-payload", "owner-primary", 1, payload)
	before := ownerStateBytes(t, f.s)
	unsigned := assertionFor(command, f.private["owner-primary"], 1)
	unsigned.Signature = nil
	if _, _, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, unsigned); !errors.Is(err, ErrOwnerCommandInvalid) {
		t.Fatalf("unsigned create error=%v", err)
	}
	changed := command
	changed.Payload = []byte(`{"title":"different title"}`)
	if _, _, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, changed, assertionFor(command, f.private["owner-primary"], 1)); !errors.Is(err, ErrOwnerChallengeUnknown) {
		t.Fatalf("payload changed after signature error=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) || f.p.saveCalls != 1 {
		t.Fatal("unsigned or payload-mismatched create changed canonical state")
	}
}

func TestOwnerTaskCreateRejectsDifferentTaskOwnerUnknownPrincipalAndWrongCommandKind(t *testing.T) {
	f := newOwnerCommandFixture(t)
	stagePlan := task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion}

	// OwnerID is not an input field for task.create. Even a valid signature
	// over an explicit, different owner must fail closed rather than letting
	// the payload select another principal.
	differentOwnerPayload, err := json.Marshal(map[string]any{
		"title": "wrong owner", "owner_id": "owner-2", "stage_plan": stagePlan,
	})
	if err != nil {
		t.Fatal(err)
	}
	differentOwner := f.command(ownercredential.CommandCreateTask, "create-different-owner", "owner-primary", 1, differentOwnerPayload)
	before := ownerStateBytes(t, f.s)
	if _, _, err := f.s.ApplyOwnerCredentialCommand(differentOwner.ChallengeID, differentOwner, assertionFor(differentOwner, f.private["owner-primary"], 1)); !errors.Is(err, ErrOwnerCommandInvalid) {
		t.Fatalf("different Task.OwnerID payload error=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) || f.p.saveCalls != 1 {
		t.Fatal("different Task.OwnerID payload changed canonical state")
	}

	validPayload, err := json.Marshal(ownercredential.CreateTaskPayload{Title: "valid", StagePlan: stagePlan})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.IssueOwnerCommandChallenge(ownercredential.CommandRequest{
		Kind: ownercredential.CommandCreateTask, MessageID: "unknown-owner", PrincipalID: "owner-unknown",
		CredentialID: "owner-primary", ExpectedCredentialRevision: 1, Payload: validPayload,
	}); !errors.Is(err, ErrOwnerCredentialUnknown) {
		t.Fatalf("unknown Owner principal error=%v", err)
	}

	// The assertion is valid for task.create, but changing the signed command
	// kind cannot reuse it as a credential lifecycle command.
	wrongKind := f.command(ownercredential.CommandCreateTask, "create-wrong-kind", "owner-primary", 1, validPayload)
	wrongKindAssertion := assertionFor(wrongKind, f.private["owner-primary"], 1)
	wrongKind.Kind = ownercredential.CommandRevokeCredential
	if _, _, err := f.s.ApplyOwnerCredentialCommand(wrongKind.ChallengeID, wrongKind, wrongKindAssertion); !errors.Is(err, ErrOwnerChallengeUnknown) {
		t.Fatalf("command-kind substitution error=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) || f.p.saveCalls != 1 {
		t.Fatal("unknown principal or command-kind substitution changed canonical state")
	}
}

func TestOwnerAssertionRejectsClientDataChallengeForDifferentCommand(t *testing.T) {
	f := newOwnerCommandFixture(t)
	payload, _ := json.Marshal(ownercredential.RevokeCredentialPayload{CredentialID: "owner-backup"})
	command := f.command(ownercredential.CommandRevokeCredential, "challenge-bound-command", "owner-primary", 1, payload)
	other := f.command(ownercredential.CommandRevokeCredential, "different-command", "owner-primary", 1, payload)
	assertion := assertionWithWebAuthnFields(command, f.private["owner-primary"], 1, other.WebAuthnChallenge, "webauthn.get", "localhost", 0x05)
	assertOwnerCommandRejectedWithoutWrite(t, f, command, assertion, ErrOwnerCommandInvalid)
}

func TestOwnerRevokedCredentialCannotSignCommand(t *testing.T) {
	f := newOwnerCommandFixture(t)
	revokePayload, _ := json.Marshal(ownercredential.RevokeCredentialPayload{CredentialID: "owner-backup"})
	revoke := f.command(ownercredential.CommandRevokeCredential, "revoke-backup-before-stale-key", "owner-primary", 1, revokePayload)
	if _, _, err := f.s.ApplyOwnerCredentialCommand(revoke.ChallengeID, revoke, assertionFor(revoke, f.private["owner-primary"], 1)); err != nil {
		t.Fatal(err)
	}

	newPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload := registerPayload(t, "owner-third", edDSACOSE(newPublic))
	// Inject a structurally valid current-revision challenge to exercise the
	// apply-side credential-active check independently of challenge issuance.
	command, err := ownercredential.NewChallenge(ownercredential.CommandRequest{
		Kind: ownercredential.CommandRegisterCredential, MessageID: "revoked-key-command", PrincipalID: "owner-1",
		CredentialID: "owner-backup", ExpectedCredentialRevision: 2, Payload: payload,
	}, f.s.now().UTC(), ownercredential.ChallengeLifetime)
	if err != nil {
		t.Fatal(err)
	}
	f.s.ownerChallenges[command.ChallengeID] = command
	assertOwnerCommandRejectedWithoutWrite(t, f, command, assertionFor(command, f.private["owner-backup"], 1), ErrOwnerCredentialInactive)
}

func TestOwnerCommandExpiredChallengeRejectsWithoutWrite(t *testing.T) {
	f := newOwnerCommandFixture(t)
	clock := &canonicalTestClock{now: time.Now().UTC()}
	f.s.now = clock.Now
	payload, _ := json.Marshal(ownercredential.RevokeCredentialPayload{CredentialID: "owner-backup"})
	command := f.command(ownercredential.CommandRevokeCredential, "expired-command", "owner-primary", 1, payload)
	assertion := assertionFor(command, f.private["owner-primary"], 1)
	clock.advance(ownercredential.ChallengeLifetime + time.Nanosecond)
	assertOwnerCommandRejectedWithoutWrite(t, f, command, assertion, ErrOwnerChallengeExpired)
}

func TestOwnerCommandRejectsChallengeAtPriorCredentialRevisionWithoutWrite(t *testing.T) {
	f := newOwnerCommandFixture(t)
	firstPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secondPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	first := f.command(ownercredential.CommandRegisterCredential, "revision-first", "owner-primary", 1, registerPayload(t, "owner-third", edDSACOSE(firstPublic)))
	second := f.command(ownercredential.CommandRegisterCredential, "revision-second", "owner-primary", 1, registerPayload(t, "owner-fourth", edDSACOSE(secondPublic)))
	if _, _, err := f.s.ApplyOwnerCredentialCommand(first.ChallengeID, first, assertionFor(first, f.private["owner-primary"], 1)); err != nil {
		t.Fatal(err)
	}
	assertOwnerCommandRejectedWithoutWrite(t, f, second, assertionFor(second, f.private["owner-primary"], 2), ErrOwnerCredentialRevision)
}

func TestOwnerCommandRejectsInvalidRPIDPresenceAndClientDataTypeWithoutWrite(t *testing.T) {
	cases := []struct {
		name       string
		clientType string
		rpID       string
		flags      byte
	}{
		{name: "wrong rp id hash", clientType: "webauthn.get", rpID: "elsewhere.local", flags: 0x05},
		{name: "uv without up", clientType: "webauthn.get", rpID: "localhost", flags: 0x04},
		{name: "creation client data type", clientType: "webauthn.create", rpID: "localhost", flags: 0x05},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newOwnerCommandFixture(t)
			payload, _ := json.Marshal(ownercredential.RevokeCredentialPayload{CredentialID: "owner-backup"})
			command := f.command(ownercredential.CommandRevokeCredential, "invalid-webauthn-"+tc.name, "owner-primary", 1, payload)
			assertion := assertionWithWebAuthnFields(command, f.private["owner-primary"], 1, command.WebAuthnChallenge, tc.clientType, tc.rpID, tc.flags)
			assertOwnerCommandRejectedWithoutWrite(t, f, command, assertion, ErrOwnerCommandInvalid)
		})
	}
}

func TestOwnerCredentialRegisterRejectsDuplicateCOSELabelWithoutWrite(t *testing.T) {
	f := newOwnerCommandFixture(t)
	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cose := []byte{0xa5, 0x01, 0x01, 0x03, 0x27, 0x20, 0x06, 0x21, 0x58, 0x20}
	cose = append(cose, f.credentials["owner-backup"].PublicKey...)
	cose = append(cose, 0x21, 0x58, 0x20) // duplicate -2 (x) label with a different key
	cose = append(cose, otherPublic...)
	command := f.command(ownercredential.CommandRegisterCredential, "duplicate-cose-label", "owner-primary", 1, registerPayload(t, "owner-third", cose))
	assertOwnerCommandRejectedWithoutWrite(t, f, command, assertionFor(command, f.private["owner-primary"], 1), ErrOwnerCommandInvalid)
}

func TestOwnerCredentialRegisterRejectsCOSEKeyTypeAlgorithmMismatchWithoutWrite(t *testing.T) {
	p256Key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	x := p256Key.X.FillBytes(make([]byte, 32))
	y := p256Key.Y.FillBytes(make([]byte, 32))
	validP256 := []byte{0xa5, 0x01, 0x02, 0x03, 0x27, 0x20, 0x01, 0x21, 0x58, 0x20}
	validP256 = append(validP256, x...)
	validP256 = append(validP256, 0x22, 0x58, 0x20)
	validP256 = append(validP256, y...)
	validEd25519 := make([]byte, 32)
	cases := []struct {
		name string
		cose []byte
	}{
		{name: "EC2 with EdDSA algorithm", cose: validP256},
		{name: "OKP with ES256 algorithm", cose: append([]byte{0xa4, 0x01, 0x01, 0x03, 0x26, 0x20, 0x06, 0x21, 0x58, 0x20}, validEd25519...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newOwnerCommandFixture(t)
			command := f.command(ownercredential.CommandRegisterCredential, "cose-mismatch-"+tc.name, "owner-primary", 1, registerPayload(t, "owner-third", tc.cose))
			assertOwnerCommandRejectedWithoutWrite(t, f, command, assertionFor(command, f.private["owner-primary"], 1), ErrOwnerCommandInvalid)
		})
	}
}

func TestTaskCreateRejectsUnsupportedStageWithoutCommit(t *testing.T) {
	f := newOwnerCommandFixture(t)
	payload, _ := json.Marshal(ownercredential.CreateTaskPayload{Title: "unsupported", StagePlan: task.StagePlan{Stages: []task.Stage{task.Execution, task.Review}, PolicyVersion: task.StagePlanPolicyVersion}})
	command := f.command(ownercredential.CommandCreateTask, "create-review-stage", "owner-primary", 1, payload)
	before := ownerStateBytes(t, f.s)
	if _, _, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertionFor(command, f.private["owner-primary"], 1)); err == nil {
		t.Fatal("unsupported runtime stage accepted")
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) || f.p.saveCalls != 1 {
		t.Fatal("unsupported StagePlan changed canonical state")
	}
}

func TestTaskCreateRejectsRequiredOwnerDecisionWithoutCommit(t *testing.T) {
	f := newOwnerCommandFixture(t)
	payload, _ := json.Marshal(ownercredential.CreateTaskPayload{Title: "unsupported decision", StagePlan: task.StagePlan{Stages: []task.Stage{task.Execution}, RequireOwnerDecision: true, PolicyVersion: task.StagePlanPolicyVersion}})
	command := f.command(ownercredential.CommandCreateTask, "create-owner-decision", "owner-primary", 1, payload)
	before := ownerStateBytes(t, f.s)
	if _, _, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertionFor(command, f.private["owner-primary"], 1)); err == nil {
		t.Fatal("unsupported Owner decision plan accepted")
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) || f.p.saveCalls != 1 {
		t.Fatal("unsupported Owner decision plan changed canonical state")
	}
}

func TestOwnerCredentialTransitionRejectsReceiptAndSignatureCounterMutation(t *testing.T) {
	f := newOwnerCommandFixture(t)
	state, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	next, err := copyCanonicalState(state)
	if err != nil {
		t.Fatal(err)
	}
	aggregate := next.OwnerCredentials["owner-1"]
	credential := aggregate.Credentials["owner-primary"]
	credential.SignCount++
	aggregate.Credentials[credential.ID] = credential
	next.OwnerCredentials["owner-1"] = aggregate
	if err := ValidateCanonicalTransition(state, next); err == nil {
		t.Fatal("signature counter changed without a command receipt")
	}
	if !reflect.DeepEqual(state.OwnerCredentials["owner-1"], f.p.state.OwnerCredentials["owner-1"]) {
		t.Fatal("snapshot helper diverged")
	}
}
