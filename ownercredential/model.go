// Package ownercredential defines the public, durable Owner credential
// aggregate. Private authenticator material is never represented here.
package ownercredential

import (
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"strings"
	"time"
)

type Algorithm string

const (
	ES256 Algorithm = "ES256"
	EdDSA Algorithm = "EdDSA"
)

type Credential struct {
	ID           string    `json:"id"`
	Algorithm    Algorithm `json:"algorithm"`
	PublicKey    []byte    `json:"public_key"`
	Fingerprint  string    `json:"fingerprint"`
	RegisteredAt time.Time `json:"registered_at"`
	RevokedAt    time.Time `json:"revoked_at,omitempty"`
	SignCount    uint32    `json:"sign_count,omitempty"`
}

func (c Credential) Active() bool { return c.RevokedAt.IsZero() }

func p256Coordinates(raw []byte) (*big.Int, *big.Int, bool) {
	if len(raw) != 64 {
		return nil, nil, false
	}
	encoded := append([]byte{4}, raw...)
	if _, err := ecdh.P256().NewPublicKey(encoded); err != nil {
		return nil, nil, false
	}
	return new(big.Int).SetBytes(raw[:32]), new(big.Int).SetBytes(raw[32:]), true
}

func (c Credential) Validate() error {
	if c.ID == "" || strings.TrimSpace(c.ID) != c.ID || len(c.ID) > 1024 || c.RegisteredAt.IsZero() {
		return errors.New("invalid OwnerCredential identity or registration time")
	}
	switch c.Algorithm {
	case ES256:
		if len(c.PublicKey) != 64 {
			return errors.New("ES256 OwnerCredential public key must contain P-256 X and Y coordinates")
		}
		if _, _, ok := p256Coordinates(c.PublicKey); !ok {
			return errors.New("ES256 OwnerCredential public key is not on P-256")
		}
	case EdDSA:
		if len(c.PublicKey) != 32 {
			return errors.New("EdDSA OwnerCredential public key must contain 32 bytes")
		}
	default:
		return fmt.Errorf("unsupported OwnerCredential algorithm %q", c.Algorithm)
	}
	if c.Fingerprint != Fingerprint(c.Algorithm, c.PublicKey) {
		return errors.New("OwnerCredential fingerprint does not match public key")
	}
	if !c.RevokedAt.IsZero() && c.RevokedAt.Before(c.RegisteredAt) {
		return errors.New("OwnerCredential revocation predates registration")
	}
	return nil
}

func Fingerprint(algorithm Algorithm, publicKey []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte("coord-owner-credential-key-v1\x00"))
	_, _ = h.Write([]byte(algorithm))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(publicKey)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Bootstrap creates the single initial aggregate and its durable receipt. The
// caller is an initialization-only human ceremony, not a daemon write route.
func Bootstrap(principalID, messageID string, credentials []Credential, now time.Time) (Aggregate, error) {
	if principalID == "" || strings.TrimSpace(principalID) != principalID || messageID == "" || strings.TrimSpace(messageID) != messageID || now.IsZero() || len(credentials) < 2 {
		return Aggregate{}, errors.New("bootstrap requires principal, message ID, time, and at least two credentials")
	}
	ordered := append([]Credential(nil), credentials...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	ids := make([]string, 0, len(ordered))
	aggregate := EmptyAggregate(principalID)
	for _, credential := range ordered {
		if !credential.RevokedAt.IsZero() || credential.SignCount != 0 {
			return Aggregate{}, errors.New("bootstrap credentials must be new active credentials with zero signature counter")
		}
		credential.RegisteredAt = now.UTC()
		if err := credential.Validate(); err != nil {
			return Aggregate{}, err
		}
		if _, exists := aggregate.Credentials[credential.ID]; exists {
			return Aggregate{}, errors.New("duplicate OwnerCredential ID")
		}
		for _, prior := range aggregate.Credentials {
			if prior.Fingerprint == credential.Fingerprint {
				return Aggregate{}, errors.New("bootstrap credentials must use distinct keys")
			}
		}
		aggregate.Credentials[credential.ID] = credential
		ids = append(ids, credential.ID)
	}
	type publicKey struct {
		ID          string    `json:"id"`
		Algorithm   Algorithm `json:"algorithm"`
		PublicKey   []byte    `json:"public_key"`
		Fingerprint string    `json:"fingerprint"`
	}
	keys := make([]publicKey, 0, len(ordered))
	for _, c := range ordered {
		keys = append(keys, publicKey{c.ID, c.Algorithm, c.PublicKey, c.Fingerprint})
	}
	canonical, err := json.Marshal(struct {
		Domain      string      `json:"domain"`
		PrincipalID string      `json:"principal_id"`
		Credentials []publicKey `json:"credentials"`
	}{"coord.owner-bootstrap.v1", principalID, keys})
	if err != nil {
		return Aggregate{}, err
	}
	digest := sha256.Sum256(canonical)
	hexDigest := hex.EncodeToString(digest[:])
	now = now.UTC()
	aggregate.CredentialRevision = 1
	aggregate.Sequence = 1
	aggregate.CommandSequence = 1
	aggregate.Events = []Event{{Sequence: 1, Kind: Bootstrapped, CredentialIDs: ids, CredentialRevision: 1, MessageID: messageID, CommandDigest: hexDigest, RecordedAt: now}}
	aggregate.Receipts[messageID] = Receipt{MessageID: messageID, CommandKind: "bootstrap", CommandDigest: hexDigest, CredentialRevision: 1, CommandSequence: 1, EventSequence: 1, RecordedAt: now}
	return aggregate, aggregate.Validate()
}

type EventKind string

const (
	Bootstrapped EventKind = "BOOTSTRAPPED"
	Registered   EventKind = "REGISTERED"
	Revoked      EventKind = "REVOKED"
)

type Event struct {
	Sequence           uint64    `json:"sequence"`
	Kind               EventKind `json:"kind"`
	CredentialID       string    `json:"credential_id,omitempty"`
	CredentialIDs      []string  `json:"credential_ids,omitempty"`
	CredentialRevision uint64    `json:"credential_revision"`
	MessageID          string    `json:"message_id"`
	CommandDigest      string    `json:"command_digest"`
	Nonce              string    `json:"nonce,omitempty"`
	RecordedAt         time.Time `json:"recorded_at"`
}

type Receipt struct {
	MessageID                  string    `json:"message_id"`
	CommandKind                string    `json:"command_kind"`
	CommandDigest              string    `json:"command_digest"`
	CredentialID               string    `json:"credential_id,omitempty"`
	TaskID                     string    `json:"task_id,omitempty"`
	Nonce                      string    `json:"nonce"`
	ExpectedCredentialRevision uint64    `json:"expected_credential_revision"`
	CredentialRevision         uint64    `json:"credential_revision"`
	CommandSequence            uint64    `json:"command_sequence"`
	EventSequence              uint64    `json:"event_sequence"`
	SignCount                  uint32    `json:"sign_count,omitempty"`
	RecordedAt                 time.Time `json:"recorded_at"`
	// CanonicalRevision binds a delegation receipt to the exact durable commit.
	CanonicalRevision    uint64            `json:"canonical_revision,omitempty"`
	DelegationID         string            `json:"delegation_id,omitempty"`
	DelegationGeneration uint64            `json:"delegation_generation,omitempty"`
	OwnerLossBinding     *OwnerLossBinding `json:"owner_loss_binding,omitempty"`
}

// OwnerLossBinding is the validated context extracted from the signed
// coordinator.loss.confirm payload and committed alongside its receipt.
type OwnerLossBinding struct {
	DelegationID    string   `json:"delegation_id"`
	GrantGeneration uint64   `json:"grant_generation"`
	StableAgentID   string   `json:"stable_agent_id"`
	SessionID       string   `json:"session_id"`
	Generation      uint64   `json:"generation"`
	AssignmentIDs   []string `json:"assignment_ids"`
}

type Aggregate struct {
	PrincipalID        string                `json:"principal_id"`
	CredentialRevision uint64                `json:"credential_revision"`
	Sequence           uint64                `json:"sequence"`
	CommandSequence    uint64                `json:"command_sequence"`
	Credentials        map[string]Credential `json:"credentials"`
	Events             []Event               `json:"events"`
	Receipts           map[string]Receipt    `json:"receipts"`
}

func EmptyAggregate(principalID string) Aggregate {
	return Aggregate{PrincipalID: principalID, Credentials: map[string]Credential{}, Events: []Event{}, Receipts: map[string]Receipt{}}
}

func (a Aggregate) Initialized() bool {
	return a.Sequence != 0 || len(a.Credentials) != 0 || len(a.Events) != 0 || len(a.Receipts) != 0
}

func (a Aggregate) ActiveCredentialCount() int {
	n := 0
	for _, c := range a.Credentials {
		if c.Active() {
			n++
		}
	}
	return n
}

func (a Aggregate) Validate() error {
	if a.PrincipalID == "" || strings.TrimSpace(a.PrincipalID) != a.PrincipalID || a.CredentialRevision == 0 || a.Sequence == 0 || a.CommandSequence == 0 {
		return errors.New("invalid OwnerCredential aggregate identity or revision")
	}
	if a.Credentials == nil || a.Events == nil || a.Receipts == nil || len(a.Credentials) < 2 || len(a.Events) == 0 || a.Sequence != uint64(len(a.Events)) || a.CommandSequence != uint64(len(a.Receipts)) {
		return errors.New("incomplete OwnerCredential aggregate")
	}
	ids := make([]string, 0, len(a.Credentials))
	fingerprints := map[string]string{}
	active := 0
	for id, c := range a.Credentials {
		if id != c.ID {
			return errors.New("OwnerCredential map key differs from credential ID")
		}
		if err := c.Validate(); err != nil {
			return err
		}
		if prior, ok := fingerprints[c.Fingerprint]; ok {
			return fmt.Errorf("credentials %q and %q share one public key", prior, id)
		}
		fingerprints[c.Fingerprint] = id
		if c.Active() {
			active++
		}
		ids = append(ids, id)
	}
	if active == 0 {
		return errors.New("OwnerCredential aggregate cannot revoke its last active credential")
	}
	if len(a.Receipts) == 0 {
		return errors.New("OwnerCredential aggregate has no command receipts")
	}
	sort.Strings(ids)
	bootstrapIDs := append([]string(nil), a.Events[0].CredentialIDs...)
	sort.Strings(bootstrapIDs)
	if a.Events[0].Kind != Bootstrapped || a.Events[0].Sequence != 1 || a.Events[0].CredentialRevision != 1 || len(bootstrapIDs) < 2 || !equalStrings(a.Events[0].CredentialIDs, bootstrapIDs) {
		return errors.New("OwnerCredential history must begin with its exact bootstrap credential set")
	}
	for _, id := range bootstrapIDs {
		if _, ok := a.Credentials[id]; !ok {
			return errors.New("OwnerCredential bootstrap history references a missing credential")
		}
	}
	seenNonce := map[string]bool{}
	for i, event := range a.Events {
		if event.Sequence != uint64(i+1) || event.MessageID == "" || event.CommandDigest == "" || event.RecordedAt.IsZero() {
			return errors.New("invalid OwnerCredential event sequence or identity")
		}
		if digest, err := hex.DecodeString(event.CommandDigest); err != nil || len(digest) != 32 {
			return errors.New("invalid OwnerCredential event command digest")
		}
		if i > 0 && event.CredentialRevision != a.Events[i-1].CredentialRevision+1 {
			return errors.New("OwnerCredential event revision is not monotonic")
		}
		if i == 0 && (event.CredentialID != "" || len(event.CredentialIDs) < 2 || event.Nonce != "") {
			return errors.New("invalid OwnerCredential bootstrap event")
		}
		if i > 0 {
			if event.Kind != Registered && event.Kind != Revoked || event.CredentialID == "" || len(event.CredentialIDs) != 0 || event.Nonce == "" {
				return errors.New("invalid OwnerCredential lifecycle event")
			}
			credential, exists := a.Credentials[event.CredentialID]
			if !exists {
				return errors.New("OwnerCredential event references a missing credential")
			}
			if event.Kind == Registered && !credential.RegisteredAt.Equal(event.RecordedAt) {
				return errors.New("registration event timestamp differs from credential")
			}
			if event.Kind == Revoked && !credential.RevokedAt.Equal(event.RecordedAt) {
				return errors.New("revocation event timestamp differs from credential")
			}
		}
		if event.Nonce != "" {
			if seenNonce[event.Nonce] {
				return errors.New("OwnerCredential nonce was reused")
			}
			seenNonce[event.Nonce] = true
		}
		receipt, ok := a.Receipts[event.MessageID]
		if !ok || receipt.MessageID != event.MessageID || receipt.CommandDigest != event.CommandDigest || receipt.CredentialRevision != event.CredentialRevision || receipt.EventSequence != event.Sequence || receipt.Nonce != event.Nonce || receipt.RecordedAt != event.RecordedAt {
			return errors.New("OwnerCredential event has no matching command receipt")
		}
		if (event.Kind == Registered && receipt.CommandKind != "owner.credential.register") || (event.Kind == Revoked && receipt.CommandKind != "owner.credential.revoke") || (event.Kind == Bootstrapped && receipt.CommandKind != "bootstrap") {
			return errors.New("OwnerCredential receipt command kind differs from event")
		}
	}
	if a.CredentialRevision != a.Events[len(a.Events)-1].CredentialRevision {
		return errors.New("OwnerCredential revision differs from event history")
	}
	receiptNonces := map[string]bool{}
	commands := make([]Receipt, a.CommandSequence)
	for messageID, receipt := range a.Receipts {
		if messageID != receipt.MessageID || receipt.CommandSequence == 0 || receipt.CommandSequence > a.CommandSequence || commands[receipt.CommandSequence-1].MessageID != "" || receipt.CommandKind == "" || receipt.CommandDigest == "" || receipt.RecordedAt.IsZero() || receipt.CredentialRevision == 0 || receipt.CredentialRevision > a.CredentialRevision || receipt.EventSequence > a.Sequence {
			return errors.New("invalid OwnerCredential receipt")
		}
		if digest, err := hex.DecodeString(receipt.CommandDigest); err != nil || len(digest) != 32 {
			return errors.New("invalid OwnerCredential receipt digest")
		}
		commands[receipt.CommandSequence-1] = receipt
		if receipt.CommandKind == "bootstrap" {
			if receipt.CommandSequence != 1 || receipt.Nonce != "" || receipt.EventSequence != 1 || receipt.ExpectedCredentialRevision != 0 || receipt.CredentialRevision != 1 || receipt.CredentialID != "" || receipt.SignCount != 0 {
				return errors.New("invalid OwnerCredential bootstrap receipt")
			}
		} else if receipt.Nonce == "" || receipt.CredentialID == "" || receipt.ExpectedCredentialRevision == 0 {
			return errors.New("signed OwnerCredential receipt requires a nonce")
		}
		receiptOnly := receipt.CommandKind == "task.create" || IsDelegationCommand(receipt.CommandKind)
		if receipt.OwnerLossBinding != nil {
			b := receipt.OwnerLossBinding
			if receipt.CommandKind != CommandLossConfirm || receipt.CanonicalRevision == 0 || b.DelegationID == "" || b.GrantGeneration == 0 || b.StableAgentID == "" || b.SessionID == "" || b.Generation == 0 || len(b.AssignmentIDs) == 0 {
				return errors.New("invalid OwnerCredential loss receipt binding")
			}
			seenAssignments := map[string]bool{}
			for _, assignmentID := range b.AssignmentIDs {
				if assignmentID == "" || strings.TrimSpace(assignmentID) != assignmentID || seenAssignments[assignmentID] {
					return errors.New("invalid OwnerCredential loss receipt assignment set")
				}
				seenAssignments[assignmentID] = true
			}
		} else if receipt.CommandKind == CommandLossConfirm && receipt.CanonicalRevision != 0 {
			return errors.New("OwnerCredential loss receipt requires exact binding")
		}
		if IsDelegationCommand(receipt.CommandKind) && (receipt.DelegationID == "" || receipt.DelegationGeneration == 0 || receipt.CanonicalRevision == 0) {
			return errors.New("delegation receipt requires commit and grant binding")
		}
		if receiptOnly && receipt.CredentialRevision != receipt.ExpectedCredentialRevision || (receipt.CommandKind == "owner.credential.register" || receipt.CommandKind == "owner.credential.revoke") && (receipt.ExpectedCredentialRevision == ^uint64(0) || receipt.CredentialRevision != receipt.ExpectedCredentialRevision+1) {
			return errors.New("OwnerCredential receipt revision does not match command kind")
		}
		if receipt.Nonce != "" {
			if receiptNonces[receipt.Nonce] {
				return errors.New("OwnerCredential nonce was reused")
			}
			receiptNonces[receipt.Nonce] = true
		}
		if receipt.EventSequence == 0 && !receiptOnly {
			return errors.New("OwnerCredential lifecycle receipt must reference an event")
		}
		if receipt.EventSequence > 0 && receiptOnly {
			return errors.New("owner receipt-only command cannot reference a credential event")
		}
		if (receipt.CommandKind == "task.create") != (receipt.TaskID != "") {
			return errors.New("owner task receipt must bind exactly one Task ID")
		}
		if IsDelegationCommand(receipt.CommandKind) && receipt.TaskID != "" {
			return errors.New("delegation Owner command cannot bind a Task ID")
		}
		if receipt.EventSequence > 0 && receipt.CommandKind != "bootstrap" && receipt.CredentialID == "" {
			return errors.New("OwnerCredential event receipt must name its signer")
		}
	}
	lastCounters := map[string]uint32{}
	for _, receipt := range commands {
		if receipt.MessageID == "" {
			return errors.New("OwnerCredential command receipt history has a gap")
		}
		if receipt.CommandKind == "bootstrap" {
			continue
		}
		if receipt.CommandKind != "owner.credential.register" && receipt.CommandKind != "owner.credential.revoke" && receipt.CommandKind != "task.create" && !IsDelegationCommand(receipt.CommandKind) {
			return errors.New("unknown OwnerCredential command receipt kind")
		}
		if _, ok := a.Credentials[receipt.CredentialID]; !ok {
			return errors.New("OwnerCredential receipt signer is absent")
		}
		if last := lastCounters[receipt.CredentialID]; last != 0 && (receipt.SignCount == 0 || receipt.SignCount <= last) {
			return errors.New("OwnerCredential signature counter did not increase")
		}
		lastCounters[receipt.CredentialID] = receipt.SignCount
	}
	for id, credential := range a.Credentials {
		if credential.SignCount != lastCounters[id] {
			return errors.New("OwnerCredential signature counter differs from command receipts")
		}
	}
	return nil
}

// ValidateTransition enforces append-only credential history. A receipt-only
// Task command may advance an authenticator counter but cannot change keys or
// lifecycle status. Credential lifecycle writes append one event and receipt.
func ValidateTransition(previous, next map[string]Aggregate) error {
	for principalID, old := range previous {
		current, ok := next[principalID]
		if !ok {
			return fmt.Errorf("OwnerCredential aggregate %q cannot be deleted", principalID)
		}
		if reflect.DeepEqual(old, current) {
			continue
		}
		if len(current.Events) < len(old.Events) || len(current.Receipts) < len(old.Receipts) {
			return fmt.Errorf("OwnerCredential events and receipts are append-only")
		}
		for i, event := range old.Events {
			if current.Events[i].Sequence != event.Sequence || !equalEvent(current.Events[i], event) {
				return fmt.Errorf("OwnerCredential event history is immutable")
			}
		}
		for id, receipt := range old.Receipts {
			if currentReceipt, exists := current.Receipts[id]; !exists || !reflect.DeepEqual(currentReceipt, receipt) {
				return fmt.Errorf("OwnerCredential receipt %q is immutable", id)
			}
		}
		var addedReceipt Receipt
		for id, receipt := range current.Receipts {
			if _, existed := old.Receipts[id]; !existed {
				addedReceipt = receipt
			}
		}
		for id, oldCredential := range old.Credentials {
			currentCredential, exists := current.Credentials[id]
			if !exists || oldCredential.ID != currentCredential.ID || oldCredential.Algorithm != currentCredential.Algorithm || !equalBytes(oldCredential.PublicKey, currentCredential.PublicKey) || oldCredential.Fingerprint != currentCredential.Fingerprint || !oldCredential.RegisteredAt.Equal(currentCredential.RegisteredAt) {
				return fmt.Errorf("OwnerCredential public identity %q is immutable", id)
			}
			if oldCredential.SignCount > currentCredential.SignCount {
				return fmt.Errorf("OwnerCredential signature counter cannot decrease")
			}
			if oldCredential.SignCount != currentCredential.SignCount && addedReceipt.CredentialID != id {
				return fmt.Errorf("OwnerCredential signature counter change requires that credential's command receipt")
			}
			if !oldCredential.RevokedAt.IsZero() && !oldCredential.RevokedAt.Equal(currentCredential.RevokedAt) {
				return fmt.Errorf("revoked OwnerCredential cannot be reactivated or rewritten")
			}
		}
		addedReceipts := len(current.Receipts) - len(old.Receipts)
		addedEvents := len(current.Events) - len(old.Events)
		if addedReceipts != 1 || (addedEvents != 0 && addedEvents != 1) || current.CommandSequence != old.CommandSequence+1 || addedReceipt.CommandSequence != current.CommandSequence || addedReceipt.ExpectedCredentialRevision != old.CredentialRevision {
			return fmt.Errorf("one Owner command must append exactly one receipt and at most one lifecycle event")
		}
		if addedEvents == 1 {
			if addedReceipts != 1 || current.CredentialRevision != old.CredentialRevision+1 || current.Sequence != old.Sequence+1 {
				return fmt.Errorf("OwnerCredential lifecycle command must advance revision and event sequence once")
			}
		} else {
			if current.CredentialRevision != old.CredentialRevision || current.Sequence != old.Sequence || (addedReceipt.CommandKind != "task.create" && !IsDelegationCommand(addedReceipt.CommandKind)) {
				return fmt.Errorf("receipt-only Owner command must be a Task or delegation command and cannot change credential lifecycle revision")
			}
		}
		for id, c := range current.Credentials {
			oldCredential, existed := old.Credentials[id]
			if !existed {
				if addedEvents != 1 || current.Events[len(current.Events)-1].Kind != Registered || current.Events[len(current.Events)-1].CredentialID != id {
					return fmt.Errorf("new credential requires matching registration event")
				}
			}
			if existed && oldCredential.RevokedAt.IsZero() && !c.RevokedAt.IsZero() {
				if addedEvents != 1 || current.Events[len(current.Events)-1].Kind != Revoked || current.Events[len(current.Events)-1].CredentialID != id {
					return fmt.Errorf("credential revocation requires matching event")
				}
			}
		}
		if err := current.Validate(); err != nil {
			return fmt.Errorf("invalid OwnerCredential transition: %w", err)
		}
	}
	for principalID, current := range next {
		if _, existed := previous[principalID]; existed {
			continue
		}
		if current.PrincipalID != principalID || current.CredentialRevision != 1 || current.Sequence != 1 || current.CommandSequence != 1 || len(current.Events) != 1 || current.Events[0].Kind != Bootstrapped {
			return fmt.Errorf("new OwnerCredential aggregate requires one initial bootstrap event")
		}
		if err := current.Validate(); err != nil {
			return fmt.Errorf("invalid OwnerCredential bootstrap: %w", err)
		}
	}
	return nil
}

func equalEvent(a, b Event) bool {
	if a.Sequence != b.Sequence || a.Kind != b.Kind || a.CredentialID != b.CredentialID || a.CredentialRevision != b.CredentialRevision || a.MessageID != b.MessageID || a.CommandDigest != b.CommandDigest || a.Nonce != b.Nonce || !a.RecordedAt.Equal(b.RecordedAt) || len(a.CredentialIDs) != len(b.CredentialIDs) {
		return false
	}
	for i := range a.CredentialIDs {
		if a.CredentialIDs[i] != b.CredentialIDs[i] {
			return false
		}
	}
	return true
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
