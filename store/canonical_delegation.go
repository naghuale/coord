package store

import (
	"coord/assignment"
	ownercredential "coord/ownercredential"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// DelegationHistoryKind names append-only lifecycle events stored on the grant.
type DelegationHistoryKind string

const (
	DelegationHistoryGrant            DelegationHistoryKind = "GRANT"
	DelegationHistoryRevoke           DelegationHistoryKind = "REVOKE"
	DelegationHistorySuccessorSkipped DelegationHistoryKind = "SUCCESSOR_SKIPPED"
	DelegationHistoryLossRecorded     DelegationHistoryKind = "LOSS_RECORDED"
	DelegationHistoryHandoff          DelegationHistoryKind = "HANDOFF"
	DelegationHistoryPrimaryRestart   DelegationHistoryKind = "PRIMARY_RESTART"
	DelegationHistoryVisibility       DelegationHistoryKind = "VISIBILITY_CONFIRMED"
)

// DelegationPrincipal is the exact triple bound by a grant or successor entry.
type DelegationPrincipal struct {
	StableAgentID string `json:"stableAgentId"`
	SessionID     string `json:"sessionId"`
	Generation    uint64 `json:"generation"`
}

func (p DelegationPrincipal) Valid() bool {
	return strings.TrimSpace(p.StableAgentID) == p.StableAgentID && p.StableAgentID != "" &&
		strings.TrimSpace(p.SessionID) == p.SessionID && p.SessionID != "" &&
		p.Generation > 0
}

func (p DelegationPrincipal) Equal(o DelegationPrincipal) bool {
	return p.StableAgentID == o.StableAgentID && p.SessionID == o.SessionID && p.Generation == o.Generation
}

func (p DelegationPrincipal) AsAssignment() assignment.Principal {
	return assignment.Principal{SessionID: p.SessionID, AgentID: p.StableAgentID, Generation: p.Generation}
}

// DelegationScope is the explicit allowlist for one grant. Empty lists and
// masks are rejected (ADR §3).
type DelegationScope struct {
	Repository       string   `json:"repository"`
	Project          string   `json:"project"`
	AllowedWorkItems []string `json:"allowedWorkItems"`
	AllowedActions   []string `json:"allowedActions"`
}

func (s DelegationScope) Validate() error {
	if strings.TrimSpace(s.Repository) != s.Repository || s.Repository == "" ||
		strings.TrimSpace(s.Project) != s.Project || s.Project == "" {
		return fmt.Errorf("%w: repository and project required", ErrCoordinatorDelegationInvalid)
	}
	if len(s.AllowedWorkItems) == 0 || len(s.AllowedActions) == 0 {
		return fmt.Errorf("%w: empty scope allowlists rejected", ErrCoordinatorDelegationInvalid)
	}
	seenWork, seenAction := map[string]bool{}, map[string]bool{}
	for _, item := range s.AllowedWorkItems {
		if strings.TrimSpace(item) != item || item == "" || item == "*" || seenWork[item] {
			return fmt.Errorf("%w: invalid allowedWorkItems", ErrCoordinatorDelegationInvalid)
		}
		seenWork[item] = true
	}
	for _, action := range s.AllowedActions {
		if strings.TrimSpace(action) != action || action == "" || action == "*" || seenAction[action] {
			return fmt.Errorf("%w: invalid allowedActions", ErrCoordinatorDelegationInvalid)
		}
		seenAction[action] = true
	}
	return nil
}

func (s DelegationScope) AllowsAction(action string) bool {
	for _, a := range s.AllowedActions {
		if a == action {
			return true
		}
	}
	return false
}

// AllowsWorkItem reports whether item is an explicit AllowedWorkItems entry.
// AdmissionBasis.Reference is the assignment's work-item identity for ADR §3/§5.
func (s DelegationScope) AllowsWorkItem(item string) bool {
	if item == "" {
		return false
	}
	for _, w := range s.AllowedWorkItems {
		if w == item {
			return true
		}
	}
	return false
}

// MatchesRepositoryProject binds an operation to the grant's repository+project
// identity (ADR §3). Empty values never match.
func (s DelegationScope) MatchesRepositoryProject(repository, project string) bool {
	return repository != "" && project != "" && s.Repository == repository && s.Project == project
}

// assignmentWorkItem returns the work-item id used for AllowedWorkItems checks.
func assignmentWorkItem(r assignment.Record) string {
	return r.AdmissionBasis.Reference
}

// assignmentBoundGrant resolves only the grant named by the assignment.
// It does not search for another grant with the same repository and project.
func assignmentBoundGrant(state CanonicalState, record assignment.Record) (DelegationRecord, error) {
	b := record.DelegationBinding
	if b == nil || b.GrantID == "" || b.Repository == "" || b.Project == "" || b.AllowedWorkItem == "" {
		return DelegationRecord{}, fmt.Errorf("%w: persisted assignment grant binding required", ErrCoordinatorDelegationInvalid)
	}
	if b.AllowedWorkItem != assignmentWorkItem(record) {
		return DelegationRecord{}, fmt.Errorf("%w: allowedWorkItem differs from admission reference", ErrCoordinatorDelegationInvalid)
	}
	grant, ok := state.CoordinatorDelegations[b.GrantID]
	if !ok || grant.DelegationID != b.GrantID {
		return DelegationRecord{}, fmt.Errorf("%w: originating grant %s missing", ErrCoordinatorDelegationInvalid, b.GrantID)
	}
	if !grant.RevokedAt.IsZero() {
		return DelegationRecord{}, fmt.Errorf("%w: originating grant %s revoked", ErrCoordinatorDelegationInvalid, b.GrantID)
	}
	if grant.Generation != b.GrantGeneration {
		return DelegationRecord{}, fmt.Errorf("%w: originating grant %s generation mismatch", ErrCoordinatorDelegationInvalid, b.GrantID)
	}
	if grant.Scope.Repository != b.Repository || grant.Scope.Project != b.Project {
		return DelegationRecord{}, fmt.Errorf("%w: originating grant %s repository/project mismatch", ErrCoordinatorDelegationInvalid, b.GrantID)
	}
	if !grant.Scope.AllowsWorkItem(b.AllowedWorkItem) {
		return DelegationRecord{}, fmt.Errorf("%w: work item %q outside allowedWorkItems of grant %s", ErrCoordinatorDelegationInvalid, b.AllowedWorkItem, b.GrantID)
	}
	return grant, nil
}

// bindAssignmentScopeFromGrant stamps the issuing grant onto a new assignment.
// A command GrantID selects that grant. Other binding fields are claims only.
// If GrantID is absent and more than one acting grant applies, the offer fails.
func bindAssignmentScopeFromGrant(state CanonicalState, actor assignment.Principal, record *assignment.Record, now time.Time) error {
	if !coordinatorDelegationActive() || record == nil {
		return nil
	}
	actorP := principalFromAssignment(actor)
	var grant DelegationRecord
	if record.DelegationBinding != nil && record.DelegationBinding.GrantID != "" {
		claimed, ok := state.CoordinatorDelegations[record.DelegationBinding.GrantID]
		if !ok || claimed.DelegationID != record.DelegationBinding.GrantID {
			return fmt.Errorf("%w: originating grant %s missing", ErrCoordinatorDelegationInvalid, record.DelegationBinding.GrantID)
		}
		if !claimed.Active(now) || claimed.IsBarred(actorP) || !claimed.CurrentActing().Equal(actorP) {
			return fmt.Errorf("%w: actor is not acting coordinator under grant %s", ErrCoordinatorDelegationInvalid, claimed.DelegationID)
		}
		grant = claimed
	} else {
		matches := actingGrantsFor(state, actor, now)
		if len(matches) == 0 {
			if grantMentionsStableCoordinator(state, actor.AgentID, now) {
				return fmt.Errorf("%w: coordinator generation/session is not authorized by grant", ErrCoordinatorDelegationInvalid)
			}
			return nil
		}
		if len(matches) > 1 {
			return fmt.Errorf("%w: actor covered by multiple grants; grantId required", ErrCoordinatorDelegationInvalid)
		}
		grant = matches[0]
	}
	item := assignmentWorkItem(*record)
	if !grant.Scope.AllowsWorkItem(item) {
		return fmt.Errorf("%w: work item %q outside allowedWorkItems of issuing grant %s", ErrCoordinatorDelegationInvalid, item, grant.DelegationID)
	}
	want := &assignment.DelegationBinding{
		GrantID: grant.DelegationID, GrantGeneration: grant.Generation,
		Repository: grant.Scope.Repository, Project: grant.Scope.Project, AllowedWorkItem: item,
	}
	if record.DelegationBinding != nil {
		b := record.DelegationBinding
		if b.GrantID != "" && b.GrantID != want.GrantID {
			return fmt.Errorf("%w: command grant binding differs from issuing grant %s", ErrCoordinatorDelegationInvalid, grant.DelegationID)
		}
		if b.GrantGeneration != 0 && b.GrantGeneration != want.GrantGeneration {
			return fmt.Errorf("%w: command grant binding differs from issuing grant %s", ErrCoordinatorDelegationInvalid, grant.DelegationID)
		}
		if b.Repository != "" && b.Repository != want.Repository {
			return fmt.Errorf("%w: command grant binding differs from issuing grant %s", ErrCoordinatorDelegationInvalid, grant.DelegationID)
		}
		if b.Project != "" && b.Project != want.Project {
			return fmt.Errorf("%w: command grant binding differs from issuing grant %s", ErrCoordinatorDelegationInvalid, grant.DelegationID)
		}
		if b.AllowedWorkItem != "" && b.AllowedWorkItem != want.AllowedWorkItem {
			return fmt.Errorf("%w: command grant binding differs from issuing grant %s", ErrCoordinatorDelegationInvalid, grant.DelegationID)
		}
	}
	record.DelegationBinding = want
	return nil
}

func grantMentionsStableCoordinator(state CanonicalState, stableAgentID string, now time.Time) bool {
	if stableAgentID == "" || state.CoordinatorDelegations == nil {
		return false
	}
	for _, grant := range state.CoordinatorDelegations {
		if !grant.Active(now) {
			continue
		}
		if grant.PrimaryCoordinator.StableAgentID == stableAgentID || grant.CurrentActing().StableAgentID == stableAgentID {
			return true
		}
		for _, successor := range grant.Successors {
			if successor.StableAgentID == stableAgentID {
				return true
			}
		}
	}
	return false
}

// validateAssignmentDelegationScope checks each persisted binding against the
// exact grant id, including AllowedWorkItems. A covered coordinator with no
// binding cannot load.
func validateAssignmentDelegationScope(state CanonicalState) error {
	if !coordinatorDelegationActive() || len(state.CoordinatorDelegations) == 0 || state.AssignmentLedger == nil {
		return nil
	}
	for id, r := range state.AssignmentLedger.Records {
		if r.DelegationBinding == nil {
			if _, covered := findAnyGrantCoveringCoordinator(state, r.Coordinator()); covered {
				return fmt.Errorf("%w: assignment %s missing persisted grant binding", ErrCoordinatorDelegationInvalid, id)
			}
			continue
		}
		b := r.DelegationBinding
		grant, ok := state.CoordinatorDelegations[b.GrantID]
		if !ok || grant.DelegationID != b.GrantID {
			return fmt.Errorf("%w: assignment %s grant %s missing", ErrCoordinatorDelegationInvalid, id, b.GrantID)
		}
		if b.Repository == "" || b.Project == "" || b.AllowedWorkItem == "" {
			return fmt.Errorf("%w: assignment %s missing persisted repository/project/work item", ErrCoordinatorDelegationInvalid, id)
		}
		if grant.Generation != b.GrantGeneration || grant.Scope.Repository != b.Repository || grant.Scope.Project != b.Project {
			return fmt.Errorf("%w: assignment %s grant binding does not match grant %s", ErrCoordinatorDelegationInvalid, id, b.GrantID)
		}
		if b.AllowedWorkItem != assignmentWorkItem(r) {
			return fmt.Errorf("%w: assignment %s allowedWorkItem drifted from admission reference", ErrCoordinatorDelegationInvalid, id)
		}
		if !grant.Scope.AllowsWorkItem(b.AllowedWorkItem) {
			return fmt.Errorf("%w: assignment %s work item %q outside allowedWorkItems of grant %s", ErrCoordinatorDelegationInvalid, id, b.AllowedWorkItem, b.GrantID)
		}
	}
	return nil
}

// DelegationValidity is immutable after grant (ADR §7 / §10).
type DelegationValidity struct {
	NotBefore time.Time `json:"notBefore"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (v DelegationValidity) Validate() error {
	if v.NotBefore.IsZero() || v.ExpiresAt.IsZero() || !v.ExpiresAt.After(v.NotBefore) {
		return fmt.Errorf("%w: invalid validity window", ErrCoordinatorDelegationInvalid)
	}
	return nil
}

func (v DelegationValidity) OpenAt(now time.Time) bool {
	return !now.IsZero() && !now.Before(v.NotBefore) && now.Before(v.ExpiresAt)
}

// DelegationPrimaryRestart binds only the primary's stableAgentId (ADR §8).
type DelegationPrimaryRestart struct {
	StableAgentID string `json:"stableAgentId"`
}

// DelegationEvidence records OWNER decision and grant-time visibility.
type DelegationEvidence struct {
	OwnerDecisionRef   string `json:"ownerDecisionRef"`
	VisibilityEvidence string `json:"visibilityEvidence"`
}

// VisibilityConfirmation is a single-use OWNER visibility receipt consumed by
// exactly one takeover / primaryRestart (transitional; authorityCreating=false).
type VisibilityConfirmation struct {
	MessageID     string              `json:"messageId"`
	Digest        string              `json:"digest"`
	DelegationID  string              `json:"delegationId"`
	Successor     DelegationPrincipal `json:"successor"`
	StableAgentID string              `json:"stableAgentId,omitempty"` // primaryRestart only
	IssuedAt      time.Time           `json:"issuedAt"`
	ExpiresAt     time.Time           `json:"expiresAt"`
	ConsumedAt    time.Time           `json:"consumedAt,omitempty"`
	ConsumedBy    string              `json:"consumedBy,omitempty"`
}

func (v VisibilityConfirmation) Available(now time.Time) bool {
	return v.ConsumedAt.IsZero() && !now.IsZero() && !now.Before(v.IssuedAt) && now.Before(v.ExpiresAt)
}

// DelegationHistoryEntry is one same-commit lifecycle event on the grant.
type DelegationHistoryEntry struct {
	Kind              DelegationHistoryKind `json:"kind"`
	At                time.Time             `json:"at"`
	Actor             string                `json:"actor,omitempty"`
	Reason            string                `json:"reason,omitempty"`
	AssignmentID      string                `json:"assignmentId,omitempty"`
	From              *DelegationPrincipal  `json:"from,omitempty"`
	To                *DelegationPrincipal  `json:"to,omitempty"`
	ReceiptID         string                `json:"receiptId,omitempty"`
	ReceiptDig        string                `json:"receiptDigest,omitempty"`
	CanonicalRevision uint64                `json:"canonicalRevision,omitempty"`
	AssignmentIDs     []string              `json:"assignmentIds,omitempty"`
}

// DelegationRecord is the canonical grant aggregate (ADR §3). Not a Task or
// Assignment field. cutoverAssignmentIds is OWNER-only and inert while the
// production const is false.
type DelegationRecord struct {
	DelegationID       string                    `json:"delegationId"`
	Scope              DelegationScope           `json:"scope"`
	PrimaryCoordinator DelegationPrincipal       `json:"primaryCoordinator"`
	Validity           DelegationValidity        `json:"validity"`
	Successors         []DelegationPrincipal     `json:"successors"`
	PrimaryRestart     *DelegationPrimaryRestart `json:"primaryRestart,omitempty"`
	RevokedAt          time.Time                 `json:"revokedAt,omitempty"`
	RevokedBy          string                    `json:"revokedBy,omitempty"`
	Evidence           DelegationEvidence        `json:"evidence"`
	// Generation is the grant's own generation, set to 1 at issue. It is not
	// the coordinator session generation. Reissue is a new DelegationID.
	// A later change of this value invalidates assignments bound to the old one.
	Generation           uint64                   `json:"generation,omitempty"`
	History              []DelegationHistoryEntry `json:"history"`
	CutoverAssignmentIDs []string                 `json:"cutoverAssignmentIds,omitempty"`
	BarredCoordinators   []DelegationPrincipal    `json:"barredCoordinators,omitempty"`
	VisibilityReceipts   []VisibilityConfirmation `json:"visibilityReceipts,omitempty"`
	ActingCoordinator    *DelegationPrincipal     `json:"actingCoordinator,omitempty"`
}

func (d DelegationRecord) Active(now time.Time) bool {
	return d.RevokedAt.IsZero() && d.Validity.OpenAt(now)
}

func (d DelegationRecord) IsBarred(p DelegationPrincipal) bool {
	for _, b := range d.BarredCoordinators {
		if b.Equal(p) {
			return true
		}
	}
	return false
}

func (d DelegationRecord) CurrentActing() DelegationPrincipal {
	if d.ActingCoordinator != nil {
		return *d.ActingCoordinator
	}
	return d.PrimaryCoordinator
}

func (d *DelegationRecord) Validate() error {
	if d == nil {
		return fmt.Errorf("%w: nil delegation", ErrCoordinatorDelegationInvalid)
	}
	if strings.TrimSpace(d.DelegationID) != d.DelegationID || d.DelegationID == "" {
		return fmt.Errorf("%w: delegationId required", ErrCoordinatorDelegationInvalid)
	}
	if err := d.Scope.Validate(); err != nil {
		return err
	}
	if !d.PrimaryCoordinator.Valid() {
		return fmt.Errorf("%w: primaryCoordinator invalid", ErrCoordinatorDelegationInvalid)
	}
	if err := d.Validity.Validate(); err != nil {
		return err
	}
	if len(d.Successors) == 0 {
		return fmt.Errorf("%w: successors required", ErrCoordinatorDelegationInvalid)
	}
	seen := map[string]bool{}
	key := func(p DelegationPrincipal) string {
		return fmt.Sprintf("%s\x00%s\x00%d", p.StableAgentID, p.SessionID, p.Generation)
	}
	seen[key(d.PrimaryCoordinator)] = true
	for _, s := range d.Successors {
		if !s.Valid() || seen[key(s)] {
			return fmt.Errorf("%w: invalid or duplicate successor", ErrCoordinatorDelegationInvalid)
		}
		seen[key(s)] = true
	}
	if d.PrimaryRestart != nil {
		if strings.TrimSpace(d.PrimaryRestart.StableAgentID) != d.PrimaryRestart.StableAgentID ||
			d.PrimaryRestart.StableAgentID == "" ||
			d.PrimaryRestart.StableAgentID != d.PrimaryCoordinator.StableAgentID {
			return fmt.Errorf("%w: primaryRestart must match primary stableAgentId", ErrCoordinatorDelegationInvalid)
		}
	}
	if strings.TrimSpace(d.Evidence.OwnerDecisionRef) == "" || strings.TrimSpace(d.Evidence.VisibilityEvidence) == "" {
		return fmt.Errorf("%w: grant evidence required", ErrCoordinatorDelegationInvalid)
	}
	if len(d.History) == 0 {
		return fmt.Errorf("%w: history required", ErrCoordinatorDelegationInvalid)
	}
	for _, id := range d.CutoverAssignmentIDs {
		if strings.TrimSpace(id) != id || id == "" {
			return fmt.Errorf("%w: invalid cutoverAssignmentIds", ErrCoordinatorDelegationInvalid)
		}
	}
	return nil
}

// validateDelegationDisabledInvariants rejects delegation records and
// OWNER_CREDENTIAL loss events while the production const and harness are off.
func validateDelegationDisabledInvariants(state CanonicalState) error {
	if coordinatorDelegationActive() {
		return nil
	}
	if len(state.CoordinatorDelegations) != 0 {
		return fmt.Errorf("%w: CoordinatorDelegations present while delegation disabled", ErrCoordinatorDelegationDisabled)
	}
	if state.AssignmentLedger != nil {
		for _, e := range state.AssignmentLedger.Events {
			if e.Kind == "COORDINATOR_LOST" && e.EffectiveActorSource() == assignment.ActorSourceOwnerCredential {
				return fmt.Errorf("%w: OWNER_CREDENTIAL loss present while delegation disabled", ErrCoordinatorDelegationDisabled)
			}
		}
	}
	return nil
}

func validateDelegations(state CanonicalState) error {
	if !coordinatorDelegationActive() {
		// Disabled invariants already enforced by validateDelegationDisabledInvariants.
		return nil
	}
	if state.CoordinatorDelegations == nil {
		return nil
	}
	if state.SchemaVersion < 6 && len(state.CoordinatorDelegations) != 0 {
		return fmt.Errorf("schema %d cannot contain CoordinatorDelegations", state.SchemaVersion)
	}
	if err := validateOwnerCredentialLossReceipts(state); err != nil {
		return err
	}
	activeKeys := map[string]string{}
	for id, d := range state.CoordinatorDelegations {
		if id != d.DelegationID {
			return fmt.Errorf("delegation map key %q does not match id %q", id, d.DelegationID)
		}
		if err := d.Validate(); err != nil {
			return err
		}
		if err := validateDelegationDerivedState(state, d); err != nil {
			return err
		}
		if d.RevokedAt.IsZero() {
			key := d.Scope.Repository + "\x00" + d.Scope.Project
			if prior, ok := activeKeys[key]; ok {
				return fmt.Errorf("%w: overlapping active grant %q and %q", ErrCoordinatorDelegationInvalid, prior, id)
			}
			activeKeys[key] = id
		}
	}
	return nil
}

func validateOwnerCredentialLossReceipts(state CanonicalState) error {
	if state.AssignmentLedger == nil {
		return nil
	}
	for _, e := range state.AssignmentLedger.Events {
		if e.Kind != "COORDINATOR_LOST" || e.ActorSource != assignment.ActorSourceOwnerCredential {
			continue
		}
		d, grantExists := state.CoordinatorDelegations[e.OwnerDelegationID]
		// OWNER_CREDENTIAL loss must reference the receipt and exact loss context
		// from this canonical commit, not merely any matching durable receipt.
		if !grantExists || e.OwnerReceiptMessageID == "" || e.OwnerReceiptDigest == "" || e.OwnerPrincipalID == "" || e.OwnerCredentialID == "" || e.OwnerCommitRevision == 0 || e.OwnerDelegationID != d.DelegationID || e.OwnerGrantGeneration != d.Generation {
			return fmt.Errorf("%w: OWNER_CREDENTIAL loss missing receipt binding", ErrCoordinatorDelegationInvalid)
		}
		agg, ok := state.OwnerCredentials[e.OwnerPrincipalID]
		if !ok {
			return fmt.Errorf("%w: OWNER_CREDENTIAL loss principal missing", ErrCoordinatorDelegationInvalid)
		}
		receipt, ok := agg.Receipts[e.OwnerReceiptMessageID]
		if !ok || receipt.CommandDigest != e.OwnerReceiptDigest || receipt.CommandKind != ownercredential.CommandLossConfirm || receipt.CredentialID != e.OwnerCredentialID || receipt.CanonicalRevision != e.OwnerCommitRevision || receipt.CanonicalRevision > state.Revision || receipt.OwnerLossBinding == nil {
			return fmt.Errorf("%w: OWNER_CREDENTIAL loss receipt missing or mismatched", ErrCoordinatorDelegationInvalid)
		}
		binding := receipt.OwnerLossBinding
		coord := e.Record.Coordinator()
		if binding.DelegationID != d.DelegationID || binding.GrantGeneration != d.Generation || binding.StableAgentID != coord.AgentID || binding.SessionID != coord.SessionID || binding.Generation != coord.Generation || !e.Record.CoordinatorLost || e.Record.AssignmentID != e.AssignmentID || e.Record.DelegationBinding == nil || e.Record.DelegationBinding.GrantID != d.DelegationID || e.Record.DelegationBinding.GrantGeneration != d.Generation {
			return fmt.Errorf("%w: OWNER_CREDENTIAL loss context differs from signed receipt", ErrCoordinatorDelegationInvalid)
		}
		if e.Actor.SessionID != "ownercredential:"+e.OwnerPrincipalID || e.Actor.AgentID != e.OwnerPrincipalID || e.Actor.Generation != 1 {
			return fmt.Errorf("%w: OWNER_CREDENTIAL loss actor mismatch", ErrCoordinatorDelegationInvalid)
		}
		if !containsString(binding.AssignmentIDs, e.AssignmentID) {
			return fmt.Errorf("%w: OWNER_CREDENTIAL receipt does not bind assignment", ErrCoordinatorDelegationInvalid)
		}
		var referenced []string
		for _, candidate := range state.AssignmentLedger.Events {
			if candidate.Kind == "COORDINATOR_LOST" && candidate.EffectiveActorSource() == assignment.ActorSourceOwnerCredential && candidate.OwnerReceiptMessageID == e.OwnerReceiptMessageID {
				if candidate.OwnerCommitRevision != e.OwnerCommitRevision || candidate.OwnerPrincipalID != e.OwnerPrincipalID || candidate.OwnerCredentialID != e.OwnerCredentialID || candidate.OwnerReceiptDigest != e.OwnerReceiptDigest || candidate.OwnerDelegationID != d.DelegationID || candidate.OwnerGrantGeneration != d.Generation {
					return fmt.Errorf("%w: OWNER_CREDENTIAL receipt reused across loss contexts", ErrCoordinatorDelegationInvalid)
				}
				referenced = append(referenced, candidate.AssignmentID)
			}
		}
		sort.Strings(referenced)
		if !reflect.DeepEqual(referenced, binding.AssignmentIDs) {
			return fmt.Errorf("%w: OWNER_CREDENTIAL receipt assignment set mismatch", ErrCoordinatorDelegationInvalid)
		}
		matches := 0
		for _, h := range d.History {
			if h.Kind == DelegationHistoryLossRecorded && h.ReceiptID == e.OwnerReceiptMessageID {
				matches++
				if h.ReceiptDig != e.OwnerReceiptDigest || h.CanonicalRevision != e.OwnerCommitRevision || h.From == nil || h.From.StableAgentID != binding.StableAgentID || h.From.SessionID != binding.SessionID || h.From.Generation != binding.Generation || !reflect.DeepEqual(h.AssignmentIDs, binding.AssignmentIDs) {
					return fmt.Errorf("%w: OWNER_CREDENTIAL loss history differs from receipt", ErrCoordinatorDelegationInvalid)
				}
			}
		}
		if matches != 1 {
			return fmt.Errorf("%w: OWNER_CREDENTIAL loss must have one grant history entry", ErrCoordinatorDelegationInvalid)
		}
	}
	return nil
}

func delegationOwnerReceipt(state CanonicalState, principalID, messageID, kind, digest, delegationID string, generation, revision uint64) (ownercredential.Receipt, error) {
	agg, ok := state.OwnerCredentials[principalID]
	if !ok {
		return ownercredential.Receipt{}, fmt.Errorf("%w: delegation Owner principal missing", ErrCoordinatorDelegationInvalid)
	}
	receipt, ok := agg.Receipts[messageID]
	if !ok || receipt.CommandKind != kind || receipt.CommandDigest != digest || receipt.DelegationID != delegationID || receipt.DelegationGeneration != generation || receipt.CanonicalRevision != revision {
		return ownercredential.Receipt{}, fmt.Errorf("%w: delegation history receipt mismatch", ErrCoordinatorDelegationInvalid)
	}
	return receipt, nil
}

// validateDelegationDerivedState reconstructs grant control fields from its
// append-only history and authenticated OwnerCredential receipts. Snapshot
// fields are accepted only when they are the result of those recorded events.
func validateDelegationDerivedState(state CanonicalState, d DelegationRecord) error {
	if len(d.History) == 0 || d.History[0].Kind != DelegationHistoryGrant {
		return fmt.Errorf("%w: delegation history must begin with GRANT", ErrCoordinatorDelegationInvalid)
	}
	grant := d.History[0]
	if _, err := delegationOwnerReceipt(state, grant.Actor, grant.ReceiptID, ownercredential.CommandDelegationGrant, grant.ReceiptDig, d.DelegationID, d.Generation, grant.CanonicalRevision); err != nil {
		return err
	}
	acting := d.PrimaryCoordinator
	revokedAt := time.Time{}
	revokedBy := ""
	barred := make([]DelegationPrincipal, 0)
	barredSet := map[DelegationPrincipal]bool{}
	visibleHistory := map[string]DelegationHistoryEntry{}
	consumedVisibility := map[string]string{}
	var lastLoss *DelegationHistoryEntry
	lossesByAssignment := map[string][]DelegationHistoryEntry{}
	handedOffForLoss := map[string]map[string]bool{}
	// actingLossID identifies the loss through which the current acting
	// successor was selected. It permits same-successor sibling handoffs for
	// that exact signed loss set without consuming visibility twice.
	actingLossID := ""
	actingLossVisibility := ""
	actingIsPrimaryRestart := false
	actingPrimaryRestartVisibility := ""
	var preRestartLosses map[string]bool
	highestPrimaryGeneration := d.PrimaryCoordinator.Generation
	selectedSuccessorForLoss := map[string]DelegationPrincipal{}
	for i, h := range d.History {
		if h.CanonicalRevision == 0 || h.CanonicalRevision > state.Revision {
			return fmt.Errorf("%w: delegation history lacks canonical commit binding", ErrCoordinatorDelegationInvalid)
		}
		// An old visibility cannot restore acting authority in this revoked
		// grant. Receipt replay and non-executable records retain their own
		// validation; a replacement grant has a separate history.
		if !revokedAt.IsZero() && (h.Kind == DelegationHistoryHandoff || h.Kind == DelegationHistoryPrimaryRestart) {
			return fmt.Errorf("%w: authority transition after revocation", ErrCoordinatorDelegationInvalid)
		}
		switch h.Kind {
		case DelegationHistoryGrant:
			if i != 0 {
				return fmt.Errorf("%w: duplicate GRANT history", ErrCoordinatorDelegationInvalid)
			}
		case DelegationHistoryRevoke:
			if i == 0 || !revokedAt.IsZero() {
				return fmt.Errorf("%w: invalid REVOKE history", ErrCoordinatorDelegationInvalid)
			}
			if _, err := delegationOwnerReceipt(state, h.Actor, h.ReceiptID, ownercredential.CommandDelegationRevoke, h.ReceiptDig, d.DelegationID, d.Generation, h.CanonicalRevision); err != nil {
				return err
			}
			revokedAt, revokedBy = h.At, h.Actor
			acting = DelegationPrincipal{}
			actingIsPrimaryRestart = false
			actingPrimaryRestartVisibility = ""
		case DelegationHistoryVisibility:
			if _, err := delegationOwnerReceipt(state, h.Actor, h.ReceiptID, ownercredential.CommandVisibilityConfirm, h.ReceiptDig, d.DelegationID, d.Generation, h.CanonicalRevision); err != nil {
				return err
			}
			if _, exists := visibleHistory[h.ReceiptID]; exists {
				return fmt.Errorf("%w: duplicate visibility history", ErrCoordinatorDelegationInvalid)
			}
			visibleHistory[h.ReceiptID] = h
		case DelegationHistoryLossRecorded:
			receipt, err := delegationOwnerReceipt(state, h.Actor, h.ReceiptID, ownercredential.CommandLossConfirm, h.ReceiptDig, d.DelegationID, d.Generation, h.CanonicalRevision)
			if err != nil {
				return err
			}
			if receipt.OwnerLossBinding == nil || h.From == nil || !sameLossPrincipal(*h.From, receipt.OwnerLossBinding) || !reflect.DeepEqual(h.AssignmentIDs, receipt.OwnerLossBinding.AssignmentIDs) || len(h.AssignmentIDs) == 0 || h.AssignmentID != h.AssignmentIDs[0] {
				return fmt.Errorf("%w: LOSS_RECORDED does not derive from OwnerCredential receipt", ErrCoordinatorDelegationInvalid)
			}
			for _, id := range h.AssignmentIDs {
				eventFound := false
				for _, event := range state.AssignmentLedger.Events {
					if event.AssignmentID == id && event.Kind == "COORDINATOR_LOST" && event.EffectiveActorSource() == assignment.ActorSourceOwnerCredential && event.OwnerReceiptMessageID == h.ReceiptID && event.OwnerCommitRevision == h.CanonicalRevision && event.OwnerDelegationID == d.DelegationID && event.OwnerGrantGeneration == d.Generation {
						eventFound = true
						break
					}
				}
				if !eventFound {
					return fmt.Errorf("%w: loss history assignment event missing", ErrCoordinatorDelegationInvalid)
				}
			}
			// Check the reverse direction too: no extra OWNER loss event may
			// reuse this receipt outside the signed assignment set. This keeps
			// snapshot derivation fail-closed even if a separate receipt validator
			// loses one of its matching predicates.
			expectedAssignments := append([]string(nil), h.AssignmentIDs...)
			sort.Strings(expectedAssignments)
			var eventAssignments []string
			for _, event := range state.AssignmentLedger.Events {
				if event.Kind == "COORDINATOR_LOST" && event.EffectiveActorSource() == assignment.ActorSourceOwnerCredential && event.OwnerReceiptMessageID == h.ReceiptID {
					if event.OwnerCommitRevision != h.CanonicalRevision || event.OwnerDelegationID != d.DelegationID || event.OwnerGrantGeneration != d.Generation {
						return fmt.Errorf("%w: loss event differs from derived history commit", ErrCoordinatorDelegationInvalid)
					}
					eventAssignments = append(eventAssignments, event.AssignmentID)
				}
			}
			sort.Strings(eventAssignments)
			if !reflect.DeepEqual(eventAssignments, expectedAssignments) {
				return fmt.Errorf("%w: loss event assignment set differs from derived history", ErrCoordinatorDelegationInvalid)
			}
			if !barredSet[*h.From] {
				barredSet[*h.From] = true
				barred = append(barred, *h.From)
			}
			if acting.Equal(*h.From) {
				acting = DelegationPrincipal{}
				actingIsPrimaryRestart = false
				actingPrimaryRestartVisibility = ""
			}
			// Every distinct loss ends the previous capture context. A later
			// capture needs fresh visibility even when an older loss still covers
			// another assignment.
			actingLossID = ""
			actingLossVisibility = ""
			copy := h
			lastLoss = &copy
			for _, id := range h.AssignmentIDs {
				lossesByAssignment[id] = append(lossesByAssignment[id], copy)
			}
		case DelegationHistoryHandoff:
			if h.From == nil || h.To == nil || h.AssignmentID == "" || h.ReceiptDig == "" {
				return fmt.Errorf("%w: incomplete HANDOFF history", ErrCoordinatorDelegationInvalid)
			}
			if barredSet[*h.To] {
				return fmt.Errorf("%w: HANDOFF target is barred", ErrCoordinatorDelegationInvalid)
			}
			var coveredLoss *DelegationHistoryEntry
			for j := len(lossesByAssignment[h.AssignmentID]) - 1; j >= 0; j-- {
				candidate := &lossesByAssignment[h.AssignmentID][j]
				if candidate.From != nil && candidate.From.Equal(*h.From) {
					coveredLoss = candidate
					break
				}
			}
			if coveredLoss != nil {
				if handedOffForLoss[coveredLoss.ReceiptID][h.AssignmentID] {
					return fmt.Errorf("%w: assignment already handed off for loss", ErrCoordinatorDelegationInvalid)
				}
			} else if !acting.Valid() {
				return fmt.Errorf("%w: HANDOFF lacks a corresponding loss", ErrCoordinatorDelegationInvalid)
			} else if !acting.Equal(*h.From) {
				return fmt.Errorf("%w: HANDOFF source differs from derived coordinator", ErrCoordinatorDelegationInvalid)
			}
			restartPrimaryTarget := coveredLoss != nil && actingIsPrimaryRestart && acting.Valid() && !barredSet[acting] && acting.Equal(*h.To) && h.To.StableAgentID == d.PrimaryCoordinator.StableAgentID
			restartPrimaryHandoff := restartPrimaryTarget && preRestartLosses[coveredLoss.ReceiptID] && h.ReceiptDig == actingPrimaryRestartVisibility
			if !d.hasSuccessor(*h.To) && !restartPrimaryTarget {
				return fmt.Errorf("%w: handoff target outside successor list", ErrCoordinatorDelegationInvalid)
			}
			reuseSelectedSuccessor := false
			if restartPrimaryHandoff {
				reuseSelectedSuccessor = true
			} else if coveredLoss != nil && acting.Valid() && !barredSet[acting] {
				if actingLossID == coveredLoss.ReceiptID {
					if !acting.Equal(*h.To) || actingLossVisibility == "" || h.ReceiptDig != actingLossVisibility {
						return fmt.Errorf("%w: sibling handoff must retain selected successor and visibility", ErrCoordinatorDelegationInvalid)
					}
					reuseSelectedSuccessor = true
				} else if !restartPrimaryTarget {
					selected, selectedBefore := selectedSuccessorForLoss[coveredLoss.ReceiptID]
					if acting.StableAgentID == d.PrimaryCoordinator.StableAgentID || selectedBefore && !barredSet[selected] && !selected.Equal(acting) {
						return fmt.Errorf("%w: another unbarred coordinator is acting", ErrCoordinatorDelegationInvalid)
					}
					if !acting.Equal(*h.To) {
						return fmt.Errorf("%w: new loss capture must use the acting successor", ErrCoordinatorDelegationInvalid)
					}
				}
			}
			if !reuseSelectedSuccessor {
				if err := validateVisibilityConsumption(d, h.ReceiptDig, h.AssignmentID, *h.To, h.At, consumedVisibility); err != nil {
					return err
				}
			}
			acting = *h.To
			if coveredLoss != nil {
				if handedOffForLoss[coveredLoss.ReceiptID] == nil {
					handedOffForLoss[coveredLoss.ReceiptID] = map[string]bool{}
				}
				handedOffForLoss[coveredLoss.ReceiptID][h.AssignmentID] = true
				if lastLoss != nil && lastLoss.ReceiptID == coveredLoss.ReceiptID {
					lastLoss = nil
				}
				if !restartPrimaryHandoff {
					actingLossID = coveredLoss.ReceiptID
					actingLossVisibility = h.ReceiptDig
					selectedSuccessorForLoss[coveredLoss.ReceiptID] = *h.To
					if !restartPrimaryTarget {
						actingIsPrimaryRestart = false
						actingPrimaryRestartVisibility = ""
					}
				}
			} else {
				actingLossID = ""
				actingLossVisibility = ""
				actingIsPrimaryRestart = false
				actingPrimaryRestartVisibility = ""
			}
		case DelegationHistoryPrimaryRestart:
			if h.From == nil || h.To == nil || lastLoss == nil || h.ReceiptID == "" || h.ReceiptDig == "" || h.ReceiptID != lastLoss.ReceiptID || lastLoss.From == nil || !h.From.Equal(*lastLoss.From) || h.To.StableAgentID != d.PrimaryCoordinator.StableAgentID || h.To.Generation <= h.From.Generation {
				return fmt.Errorf("%w: invalid PRIMARY_RESTART history", ErrCoordinatorDelegationInvalid)
			}
			if d.PrimaryRestart == nil || d.PrimaryRestart.StableAgentID != h.To.StableAgentID {
				return fmt.Errorf("%w: PRIMARY_RESTART has no matching configured record", ErrCoordinatorDelegationInvalid)
			}
			if h.To.SessionID == d.PrimaryCoordinator.SessionID {
				return fmt.Errorf("%w: PRIMARY_RESTART reuses primary session", ErrCoordinatorDelegationInvalid)
			}
			if h.To.SessionID == h.From.SessionID {
				return fmt.Errorf("%w: PRIMARY_RESTART reuses lost session", ErrCoordinatorDelegationInvalid)
			}
			if h.To.Generation <= highestPrimaryGeneration {
				return fmt.Errorf("%w: PRIMARY_RESTART generation does not exceed highest primary generation", ErrCoordinatorDelegationInvalid)
			}
			for _, barredPrincipal := range barred {
				if h.To.SessionID == barredPrincipal.SessionID {
					return fmt.Errorf("%w: PRIMARY_RESTART reuses a barred session", ErrCoordinatorDelegationInvalid)
				}
			}
			for _, previous := range d.History[:i] {
				if previous.Kind == DelegationHistoryPrimaryRestart && previous.To != nil && previous.To.SessionID == h.To.SessionID {
					return fmt.Errorf("%w: PRIMARY_RESTART reuses a prior restart session", ErrCoordinatorDelegationInvalid)
				}
			}
			if acting.Valid() && !barredSet[acting] {
				return fmt.Errorf("%w: PRIMARY_RESTART while another coordinator is acting", ErrCoordinatorDelegationInvalid)
			}
			if err := validateVisibilityConsumption(d, h.ReceiptDig, h.Reason, *h.To, h.At, consumedVisibility); err != nil {
				return err
			}
			acting = *h.To
			highestPrimaryGeneration = h.To.Generation
			lastLoss = nil
			actingLossID = ""
			actingLossVisibility = ""
			actingIsPrimaryRestart = true
			actingPrimaryRestartVisibility = h.ReceiptDig
			preRestartLosses = lossesBeforePrimaryRestart(d, h)
		case DelegationHistorySuccessorSkipped:
			if h.Reason == "" || h.To == nil || !d.hasSuccessor(*h.To) {
				return fmt.Errorf("%w: invalid SUCCESSOR_SKIPPED history", ErrCoordinatorDelegationInvalid)
			}
		default:
			return fmt.Errorf("%w: unknown delegation history event", ErrCoordinatorDelegationInvalid)
		}
		for _, principal := range []*DelegationPrincipal{h.From, h.To} {
			if principal != nil && principal.StableAgentID == d.PrimaryCoordinator.StableAgentID && principal.Generation > highestPrimaryGeneration {
				highestPrimaryGeneration = principal.Generation
			}
		}
	}
	if !revokedAt.Equal(d.RevokedAt) || revokedBy != d.RevokedBy {
		return fmt.Errorf("%w: revocation snapshot differs from history", ErrCoordinatorDelegationInvalid)
	}
	if (d.ActingCoordinator == nil) != !acting.Valid() || d.ActingCoordinator != nil && !d.ActingCoordinator.Equal(acting) {
		return fmt.Errorf("%w: acting coordinator differs from history", ErrCoordinatorDelegationInvalid)
	}
	if len(d.BarredCoordinators) != len(barred) {
		return fmt.Errorf("%w: barred coordinators differ from loss history", ErrCoordinatorDelegationInvalid)
	}
	for i := range barred {
		if !d.BarredCoordinators[i].Equal(barred[i]) {
			return fmt.Errorf("%w: barred coordinators differ from loss history", ErrCoordinatorDelegationInvalid)
		}
	}
	if len(visibleHistory) != len(d.VisibilityReceipts) {
		return fmt.Errorf("%w: visibility receipts differ from Owner history", ErrCoordinatorDelegationInvalid)
	}
	for _, visibility := range d.VisibilityReceipts {
		h, ok := visibleHistory[visibility.MessageID]
		if !ok || h.ReceiptDig != visibility.Digest || visibility.DelegationID != d.DelegationID {
			return fmt.Errorf("%w: visibility receipt missing matching history", ErrCoordinatorDelegationInvalid)
		}
		wantConsumedBy, consumed := consumedVisibility[visibility.MessageID]
		if visibility.ConsumedAt.IsZero() != !consumed || consumed && (visibility.ConsumedBy != wantConsumedBy || visibility.ConsumedAt.IsZero()) {
			return fmt.Errorf("%w: visibility consumption differs from history", ErrCoordinatorDelegationInvalid)
		}
	}
	return nil
}

func sameLossPrincipal(p DelegationPrincipal, b *ownercredential.OwnerLossBinding) bool {
	return b != nil && p.StableAgentID == b.StableAgentID && p.SessionID == b.SessionID && p.Generation == b.Generation
}

func validateVisibilityConsumption(d DelegationRecord, messageID, consumedBy string, successor DelegationPrincipal, at time.Time, consumed map[string]string) error {
	if _, duplicate := consumed[messageID]; duplicate {
		return fmt.Errorf("%w: visibility receipt consumed more than once", ErrCoordinatorDelegationInvalid)
	}
	for _, v := range d.VisibilityReceipts {
		if v.MessageID != messageID {
			continue
		}
		if v.ConsumedAt.IsZero() || v.ConsumedBy != consumedBy || !v.ConsumedAt.Equal(at) {
			return fmt.Errorf("%w: visibility receipt consumption mismatch", ErrCoordinatorDelegationInvalid)
		}
		if v.Successor.Valid() && !v.Successor.Equal(successor) {
			return fmt.Errorf("%w: visibility successor differs from history", ErrCoordinatorDelegationInvalid)
		}
		if v.StableAgentID != "" && v.StableAgentID != successor.StableAgentID {
			return fmt.Errorf("%w: visibility primary differs from history", ErrCoordinatorDelegationInvalid)
		}
		consumed[messageID] = consumedBy
		return nil
	}
	return fmt.Errorf("%w: transition references unknown visibility receipt", ErrCoordinatorDelegationInvalid)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// actingLossContext derives the OWNER loss that selected the current acting
// successor. A later loss or primary restart ends the prior no-new-visibility
// sibling context.
func actingLossContext(d DelegationRecord) (string, string) {
	acting := d.PrimaryCoordinator
	contextID, contextVisibility := "", ""
	restart, isRestartedPrimary := currentPrimaryRestart(d)
	preRestartLosses := lossesBeforePrimaryRestart(d, restart)
	lossesByAssignment := map[string][]DelegationHistoryEntry{}
	for _, h := range d.History {
		switch h.Kind {
		case DelegationHistoryLossRecorded:
			if h.From != nil && acting.Equal(*h.From) {
				acting = DelegationPrincipal{}
			}
			contextID, contextVisibility = "", ""
			for _, id := range h.AssignmentIDs {
				lossesByAssignment[id] = append(lossesByAssignment[id], h)
			}
		case DelegationHistoryHandoff:
			if h.To == nil {
				contextID, contextVisibility = "", ""
				continue
			}
			var covered *DelegationHistoryEntry
			for i := len(lossesByAssignment[h.AssignmentID]) - 1; i >= 0; i-- {
				candidate := &lossesByAssignment[h.AssignmentID][i]
				if candidate.From != nil && h.From != nil && candidate.From.Equal(*h.From) {
					covered = candidate
					break
				}
			}
			// Routing an older loss via A-2 does not create a new capture and
			// must not erase a fresh A-3 sibling context already established.
			reusesRestart := covered != nil && isRestartedPrimary && preRestartLosses[covered.ReceiptID] && h.To.Equal(acting) && h.ReceiptDig == restart.ReceiptDig
			if !reusesRestart {
				contextID, contextVisibility = "", ""
				if covered != nil {
					contextID, contextVisibility = covered.ReceiptID, h.ReceiptDig
				}
			}
			acting = *h.To
		case DelegationHistoryPrimaryRestart:
			contextID, contextVisibility = "", ""
			if h.To != nil {
				acting = *h.To
			}
		case DelegationHistoryRevoke:
			acting = DelegationPrincipal{}
			contextID, contextVisibility = "", ""
		}
	}
	if !acting.Equal(d.CurrentActing()) {
		return "", ""
	}
	return contextID, contextVisibility
}

// currentPrimaryRestart returns the restart event that established the
// currently acting primary session. Covered assignments may be routed to that
// primary while that exact session remains the unbarred actor. Callers decide
// whether the loss predates the restart (A-2) or needs fresh visibility (A-3).
func currentPrimaryRestart(d DelegationRecord) (DelegationHistoryEntry, bool) {
	acting := d.PrimaryCoordinator
	var restart *DelegationHistoryEntry
	lossesByAssignment := map[string][]DelegationHistoryEntry{}
	for _, h := range d.History {
		switch h.Kind {
		case DelegationHistoryLossRecorded:
			if h.From != nil && acting.Equal(*h.From) {
				acting = DelegationPrincipal{}
				restart = nil
			}
			for _, id := range h.AssignmentIDs {
				lossesByAssignment[id] = append(lossesByAssignment[id], h)
			}
		case DelegationHistoryHandoff:
			if h.To == nil {
				restart = nil
				continue
			}
			keepRestart := false
			if restart != nil && h.From != nil && h.To.Equal(acting) {
				for i := len(lossesByAssignment[h.AssignmentID]) - 1; i >= 0; i-- {
					loss := lossesByAssignment[h.AssignmentID][i]
					if loss.From != nil && loss.From.Equal(*h.From) {
						keepRestart = true
						break
					}
				}
			}
			acting = *h.To
			if !keepRestart {
				restart = nil
			}
		case DelegationHistoryPrimaryRestart:
			if h.To == nil {
				acting = DelegationPrincipal{}
				restart = nil
				continue
			}
			acting = *h.To
			copy := h
			restart = &copy
		case DelegationHistoryRevoke:
			acting = DelegationPrincipal{}
			restart = nil
		}
	}
	if restart == nil || !acting.Equal(d.CurrentActing()) || acting.StableAgentID != d.PrimaryCoordinator.StableAgentID || d.IsBarred(acting) {
		return DelegationHistoryEntry{}, false
	}
	return *restart, true
}

// lossesBeforePrimaryRestart requires both the grant's history order and a
// strictly earlier canonical commit. A changed numeric revision alone cannot
// turn a later recorded loss into an A-2 visibility exemption.
func lossesBeforePrimaryRestart(d DelegationRecord, restart DelegationHistoryEntry) map[string]bool {
	out := map[string]bool{}
	for _, h := range d.History {
		if h.Kind == DelegationHistoryPrimaryRestart && h.CanonicalRevision == restart.CanonicalRevision && h.ReceiptID == restart.ReceiptID && h.ReceiptDig == restart.ReceiptDig {
			return out
		}
		if h.Kind == DelegationHistoryLossRecorded && h.CanonicalRevision > 0 && h.CanonicalRevision < restart.CanonicalRevision {
			out[h.ReceiptID] = true
		}
	}
	return nil
}

func lossHistoryForAssignment(d DelegationRecord, assignmentID string, lost DelegationPrincipal) (DelegationHistoryEntry, bool) {
	for i := len(d.History) - 1; i >= 0; i-- {
		h := d.History[i]
		if h.Kind == DelegationHistoryLossRecorded && h.From != nil && h.From.Equal(lost) && h.ReceiptID != "" && containsString(h.AssignmentIDs, assignmentID) {
			return h, true
		}
	}
	return DelegationHistoryEntry{}, false
}

func selectedSuccessorForLoss(d DelegationRecord, lossID string) (DelegationPrincipal, bool) {
	var selected DelegationPrincipal
	found := false
	lossesByAssignment := map[string][]DelegationHistoryEntry{}
	for _, h := range d.History {
		if h.Kind == DelegationHistoryLossRecorded {
			for _, id := range h.AssignmentIDs {
				lossesByAssignment[id] = append(lossesByAssignment[id], h)
			}
			continue
		}
		if h.Kind != DelegationHistoryHandoff || h.From == nil || h.To == nil {
			continue
		}
		var loss *DelegationHistoryEntry
		for i := len(lossesByAssignment[h.AssignmentID]) - 1; i >= 0; i-- {
			candidate := &lossesByAssignment[h.AssignmentID][i]
			if candidate.From != nil && candidate.From.Equal(*h.From) {
				loss = candidate
				break
			}
		}
		if loss != nil && loss.ReceiptID == lossID {
			selected, found = *h.To, true
		}
	}
	return selected, found
}

func stampNewDelegationHistory(previous, next map[string]DelegationRecord, revision uint64) {
	for id, after := range next {
		start := 0
		if before, ok := previous[id]; ok {
			start = len(before.History)
		}
		for i := start; i < len(after.History); i++ {
			after.History[i].CanonicalRevision = revision
		}
		next[id] = after
	}
}

// rejectGenericOwnerLossEvents reserves OWNER_CREDENTIAL loss creation for the
// authenticated owner-delegation commit. A prior receipt is never authority to
// append another loss through a generic callback.
func rejectGenericOwnerLossEvents(previous, next *assignment.Ledger) error {
	if next == nil {
		return nil
	}
	start := 0
	if previous != nil {
		if len(next.Events) < len(previous.Events) {
			return fmt.Errorf("%w: assignment history cannot shrink", ErrCoordinatorDelegationInvalid)
		}
		for i := range previous.Events {
			if !reflect.DeepEqual(previous.Events[i], next.Events[i]) {
				return fmt.Errorf("%w: assignment history is append-only", ErrCoordinatorDelegationInvalid)
			}
		}
		start = len(previous.Events)
	}
	for _, event := range next.Events[start:] {
		if event.Kind == "COORDINATOR_LOST" && event.EffectiveActorSource() == assignment.ActorSourceOwnerCredential {
			return fmt.Errorf("%w: OWNER_CREDENTIAL loss requires authenticated owner command", ErrOwnerCommandRequired)
		}
	}
	return nil
}

// ownerCredentialLossAllowsHandoffWithoutObserver is the G93b-1 gate for recovery
// after COORDINATOR_LOST when OpenCoordinatorLossObserver reports !open.
// At const=false it is always false (main/#90 parity). When delegation is active
// it requires the assignment's latest COORDINATOR_LOST to be OWNER_CREDENTIAL
// with a matching same-commit OwnerCredential receipt.
func ownerCredentialLossAllowsHandoffWithoutObserver(state CanonicalState, assignmentID string) bool {
	if !coordinatorDelegationActive() || state.AssignmentLedger == nil {
		return false
	}
	rec, ok := state.AssignmentLedger.Records[assignmentID]
	if !ok || !rec.CoordinatorLost {
		return false
	}
	var lost *assignment.Event
	for i := range state.AssignmentLedger.Events {
		e := &state.AssignmentLedger.Events[i]
		if e.AssignmentID == assignmentID && e.Kind == "COORDINATOR_LOST" {
			lost = e
		}
	}
	if lost == nil || lost.ActorSource != assignment.ActorSourceOwnerCredential {
		return false
	}
	// Receipt must bind this loss record, this assignment, and the current generation.
	if lost.Record.AssignmentID != assignmentID || lost.Record.Coordinator() != rec.Coordinator() {
		return false
	}
	if lost.OwnerReceiptMessageID == "" || lost.OwnerReceiptDigest == "" || lost.OwnerPrincipalID == "" || lost.OwnerCredentialID == "" {
		return false
	}
	agg, ok := state.OwnerCredentials[lost.OwnerPrincipalID]
	if !ok {
		return false
	}
	receipt, ok := agg.Receipts[lost.OwnerReceiptMessageID]
	if !ok || receipt.CommandDigest != lost.OwnerReceiptDigest || receipt.CommandKind != ownercredential.CommandLossConfirm || receipt.CanonicalRevision != lost.OwnerCommitRevision || receipt.OwnerLossBinding == nil || lost.OwnerDelegationID == "" || lost.Record.DelegationBinding == nil || lost.Record.DelegationBinding.GrantID != lost.OwnerDelegationID {
		return false
	}
	b := receipt.OwnerLossBinding
	grant, exists := state.CoordinatorDelegations[lost.OwnerDelegationID]
	if !exists || b.DelegationID != grant.DelegationID || b.GrantGeneration != grant.Generation || b.StableAgentID != lost.Record.CoordinatorAgentID || b.SessionID != lost.Record.CurrentCoordinator || b.Generation != lost.Record.CoordinatorGeneration || !containsString(b.AssignmentIDs, assignmentID) {
		return false
	}
	return true
}

// Delegated action names used in grant allowlists (ADR §5).
const (
	DelegatedActionOffer    = "OFFER"
	DelegatedActionActivate = "ACTIVATE"
	DelegatedActionVoid     = "VOID"
	DelegatedActionExpire   = "EXPIRE"
	DelegatedActionHandoff  = "HANDOFF"
	DelegatedActionAccept   = "ACCEPT_RESULT"
	DelegatedActionReview   = "REQUEST_REVIEW"
	DelegatedActionAudit    = "REQUEST_AUDIT"
	DelegatedActionGate     = "APPLY_ACCEPTED_GATE"
	DelegatedActionCorrect  = "START_PRESCRIBED_CORRECTION"
	DelegatedActionEvidence = "OPERATIONAL_EVIDENCE"
)

// Forbidden under any grant (ADR §5 default / §6).
var delegatedForbiddenActions = map[string]bool{
	"SUSPEND": true, "RECONCILE": true,
	"EXTEND_SCOPE": true, "EXTEND_EXPIRY": true, "EDIT_SUCCESSORS": true,
	"SUBDELEGATE": true, "PASS_OVER_FAIL": true, "PASS_OVER_UNKNOWN": true,
	"SELF_ACCEPT": true, "OWNER_CREDENTIAL_LIFECYCLE": true,
	"AGENT_SESSION_REGISTER": true, "AGENT_SESSION_ROTATE": true,
	"AGENT_SESSION_REPLACE": true, "BYPASS_ROLE_INDEPENDENCE": true,
}

func principalFromAssignment(p assignment.Principal) DelegationPrincipal {
	return DelegationPrincipal{StableAgentID: p.AgentID, SessionID: p.SessionID, Generation: p.Generation}
}

// findGrantForActingCoordinator returns the active grant whose acting triple matches.
func findGrantForActingCoordinator(state CanonicalState, actor assignment.Principal, now time.Time) (DelegationRecord, bool) {
	matches := actingGrantsFor(state, actor, now)
	if len(matches) != 1 {
		return DelegationRecord{}, false
	}
	return matches[0], true
}

func actingGrantsFor(state CanonicalState, actor assignment.Principal, now time.Time) []DelegationRecord {
	if !coordinatorDelegationActive() || state.CoordinatorDelegations == nil {
		return nil
	}
	want := principalFromAssignment(actor)
	var out []DelegationRecord
	for _, d := range state.CoordinatorDelegations {
		if !d.Active(now) || d.IsBarred(want) {
			continue
		}
		if d.CurrentActing().Equal(want) {
			out = append(out, d)
		}
	}
	return out
}

// delegationCommitKind is the unexported writer seam for grant mutations.
// A callback cannot set this; only HandoffAssignment and ApplyPrimaryRestart
// mint the corresponding value. Generic UpdateCanonical is always generic.
type delegationCommitKind uint8

const (
	delegationCommitGeneric delegationCommitKind = iota
	delegationCommitHandoff
	delegationCommitPrimaryRestart
)

func grantAuthorityFrozen(before, after DelegationRecord) bool {
	return before.Generation == after.Generation && bytesEqualScope(before.Scope, after.Scope) &&
		before.Validity.NotBefore.Equal(after.Validity.NotBefore) && before.Validity.ExpiresAt.Equal(after.Validity.ExpiresAt) &&
		before.PrimaryCoordinator.Equal(after.PrimaryCoordinator) && reflect.DeepEqual(before.Successors, after.Successors) &&
		reflect.DeepEqual(before.PrimaryRestart, after.PrimaryRestart) && before.Evidence == after.Evidence &&
		reflect.DeepEqual(before.CutoverAssignmentIDs, after.CutoverAssignmentIDs) &&
		before.RevokedAt.Equal(after.RevokedAt) && before.RevokedBy == after.RevokedBy
}

func historyAppendOnly(before, after DelegationRecord) error {
	if len(after.History) < len(before.History) {
		return fmt.Errorf("%w: grant history cannot shrink", ErrCoordinatorDelegationInvalid)
	}
	for i := range before.History {
		if !reflect.DeepEqual(before.History[i], after.History[i]) {
			return fmt.Errorf("%w: grant history is append-only", ErrCoordinatorDelegationInvalid)
		}
	}
	return nil
}

func visibilityNoReset(before, after DelegationRecord) error {
	if len(before.VisibilityReceipts) != len(after.VisibilityReceipts) {
		return fmt.Errorf("%w: visibility receipts are owner-command only", ErrCoordinatorDelegationInvalid)
	}
	for i := range before.VisibilityReceipts {
		p, n := before.VisibilityReceipts[i], after.VisibilityReceipts[i]
		if p.MessageID != n.MessageID || p.Digest != n.Digest || p.DelegationID != n.DelegationID ||
			p.StableAgentID != n.StableAgentID || !p.IssuedAt.Equal(n.IssuedAt) || !p.ExpiresAt.Equal(n.ExpiresAt) ||
			!reflect.DeepEqual(p.Successor, n.Successor) {
			return fmt.Errorf("%w: visibility receipt identity is immutable", ErrCoordinatorDelegationInvalid)
		}
		if !p.ConsumedAt.IsZero() && (n.ConsumedAt.IsZero() || n.ConsumedBy != p.ConsumedBy || !n.ConsumedAt.Equal(p.ConsumedAt)) {
			return fmt.Errorf("%w: consumed visibility cannot be reset", ErrCoordinatorDelegationInvalid)
		}
	}
	return nil
}

func newlyConsumedVisibility(before, after DelegationRecord) []VisibilityConfirmation {
	var out []VisibilityConfirmation
	for i := range before.VisibilityReceipts {
		p, n := before.VisibilityReceipts[i], after.VisibilityReceipts[i]
		if p.ConsumedAt.IsZero() && !n.ConsumedAt.IsZero() {
			out = append(out, n)
		}
	}
	return out
}

func changedGrants(prev, next map[string]DelegationRecord) ([]string, error) {
	if len(prev) != len(next) {
		return nil, fmt.Errorf("%w: generic writer cannot insert or delete grants", ErrCoordinatorDelegationInvalid)
	}
	var ids []string
	for id, before := range prev {
		after, ok := next[id]
		if !ok || after.DelegationID != before.DelegationID {
			return nil, fmt.Errorf("%w: generic writer cannot insert or delete grants", ErrCoordinatorDelegationInvalid)
		}
		if !reflect.DeepEqual(before, after) {
			ids = append(ids, id)
		}
	}
	for id := range next {
		if _, ok := prev[id]; !ok {
			return nil, fmt.Errorf("%w: generic writer cannot insert or delete grants", ErrCoordinatorDelegationInvalid)
		}
	}
	return ids, nil
}

// rejectUnauthorizedGrantMutation is the F11 generic-writer gate for every
// canonical Save path that is not the handoff/primaryRestart seam or the
// OwnerCredential grant/revoke/loss/visibility command.
func rejectUnauthorizedGrantMutation(prev, next CanonicalState) error {
	if reflect.DeepEqual(prev.CoordinatorDelegations, next.CoordinatorDelegations) {
		return nil
	}
	if !coordinatorDelegationActive() {
		return ErrCoordinatorDelegationDisabled
	}
	return fmt.Errorf("%w: grant authority is immutable via generic writer", ErrCoordinatorDelegationInvalid)
}

// validateDelegatedWriterDelta is the F8/F11 grant-writer gate.
// Generic UpdateCanonical cannot mutate CoordinatorDelegations at all.
// Handoff and primaryRestart permits still freeze grant authority and require
// an exact history entry plus a matching one-time visibility consumption.
func validateDelegatedWriterDelta(kind delegationCommitKind, prev, next CanonicalState) error {
	if kind == delegationCommitGeneric {
		return fmt.Errorf("%w: grant authority is immutable via generic writer", ErrCoordinatorDelegationInvalid)
	}
	ids, err := changedGrants(prev.CoordinatorDelegations, next.CoordinatorDelegations)
	if err != nil {
		return err
	}
	if len(ids) != 1 {
		return fmt.Errorf("%w: handoff/primaryRestart must mutate exactly one grant", ErrCoordinatorDelegationInvalid)
	}
	before := prev.CoordinatorDelegations[ids[0]]
	after := next.CoordinatorDelegations[ids[0]]
	if !grantAuthorityFrozen(before, after) {
		return fmt.Errorf("%w: grant authority is immutable via generic writer", ErrCoordinatorDelegationInvalid)
	}
	if err := historyAppendOnly(before, after); err != nil {
		return err
	}
	if !reflect.DeepEqual(before.BarredCoordinators, after.BarredCoordinators) {
		return fmt.Errorf("%w: barred coordinators are owner-command only", ErrCoordinatorDelegationInvalid)
	}
	if err := visibilityNoReset(before, after); err != nil {
		return err
	}
	switch kind {
	case delegationCommitHandoff:
		return validateHandoffGrantDelta(prev, next, before, after)
	case delegationCommitPrimaryRestart:
		return validatePrimaryRestartGrantDelta(before, after)
	default:
		return fmt.Errorf("%w: acting coordinator requires handoff or primaryRestart", ErrCoordinatorDelegationInvalid)
	}
}

func validateHandoffGrantDelta(prev, next CanonicalState, before, after DelegationRecord) error {
	if after.ActingCoordinator == nil {
		return fmt.Errorf("%w: handoff history required", ErrCoordinatorDelegationInvalid)
	}
	to := *after.ActingCoordinator
	added := after.History[len(before.History):]
	if len(added) == 0 {
		return fmt.Errorf("%w: handoff history required", ErrCoordinatorDelegationInvalid)
	}
	for i := 0; i < len(added)-1; i++ {
		if added[i].Kind != DelegationHistorySuccessorSkipped {
			return fmt.Errorf("%w: handoff history required", ErrCoordinatorDelegationInvalid)
		}
	}
	h := added[len(added)-1]
	if h.Kind != DelegationHistoryHandoff || h.To == nil || !h.To.Equal(to) || h.From == nil || h.AssignmentID == "" {
		return fmt.Errorf("%w: handoff history required", ErrCoordinatorDelegationInvalid)
	}
	if next.AssignmentLedger == nil {
		return fmt.Errorf("%w: handoff assignment mismatch", ErrCoordinatorDelegationInvalid)
	}
	rec, ok := next.AssignmentLedger.Records[h.AssignmentID]
	if !ok {
		return fmt.Errorf("%w: handoff assignment mismatch", ErrCoordinatorDelegationInvalid)
	}
	coord := rec.Coordinator()
	if coord.AgentID != to.StableAgentID || coord.SessionID != to.SessionID || coord.Generation != to.Generation {
		return fmt.Errorf("%w: handoff assignment mismatch", ErrCoordinatorDelegationInvalid)
	}
	if rec.DelegationBinding == nil || rec.DelegationBinding.GrantID != after.DelegationID || rec.DelegationBinding.GrantGeneration != after.Generation {
		return fmt.Errorf("%w: handoff assignment mismatch", ErrCoordinatorDelegationInvalid)
	}
	restart, isRestartedPrimary := currentPrimaryRestart(before)
	preRestartLosses := lossesBeforePrimaryRestart(before, restart)
	loss, lossCoversAssignment := lossHistoryForAssignment(before, h.AssignmentID, *h.From)
	acting := before.CurrentActing()
	if lossCoversAssignment && acting.Valid() && !before.IsBarred(acting) && !h.From.Equal(acting) && !to.Equal(acting) {
		return fmt.Errorf("%w: another unbarred coordinator is acting", ErrCoordinatorDelegationInvalid)
	}
	var prior assignment.Record
	wasLost := false
	if prev.AssignmentLedger != nil {
		prior, wasLost = prev.AssignmentLedger.Records[h.AssignmentID]
	}
	restartPrimaryTarget := isRestartedPrimary && lossCoversAssignment && wasLost && prior.CoordinatorLost && ownerCredentialLossAllowsHandoffWithoutObserver(prev, h.AssignmentID) && before.CurrentActing().Equal(to) && !before.IsBarred(to) && to.StableAgentID == before.PrimaryCoordinator.StableAgentID && restart.ReceiptDig != ""
	restartPrimaryHandoff := restartPrimaryTarget && preRestartLosses[loss.ReceiptID] && h.ReceiptDig == restart.ReceiptDig
	if !after.hasSuccessor(to) && !restartPrimaryTarget {
		return fmt.Errorf("%w: handoff target outside successor list", ErrCoordinatorDelegationInvalid)
	}
	consumed := newlyConsumedVisibility(before, after)
	if len(consumed) == 0 {
		if restartPrimaryHandoff {
			return nil
		}
		lossID, visibilityID := actingLossContext(before)
		loss, covered := lossHistoryForAssignment(before, h.AssignmentID, *h.From)
		if !covered || lossID == "" || lossID != loss.ReceiptID || visibilityID == "" || h.ReceiptDig != visibilityID || !before.CurrentActing().Equal(to) || before.IsBarred(to) {
			return fmt.Errorf("%w: visibility receipt required", ErrCoordinatorDelegationInvalid)
		}
		// The existing visibility was already consumed by the first assignment
		// under this exact signed loss. Siblings may retain that receipt reference
		// only for the already-acting successor.
		return nil
	}
	if len(consumed) != 1 {
		return fmt.Errorf("%w: visibility receipt assignment mismatch", ErrCoordinatorDelegationInvalid)
	}
	v := consumed[0]
	if restartPrimaryTarget && v.MessageID != h.ReceiptDig {
		return fmt.Errorf("%w: visibility receipt does not match handoff", ErrCoordinatorDelegationInvalid)
	}
	if v.Successor.StableAgentID != to.StableAgentID || v.Successor.SessionID != to.SessionID {
		return fmt.Errorf("%w: visibility receipt successor mismatch", ErrCoordinatorDelegationInvalid)
	}
	if v.Successor.Generation != to.Generation {
		return fmt.Errorf("%w: visibility receipt generation mismatch", ErrCoordinatorDelegationInvalid)
	}
	if v.ConsumedBy != h.AssignmentID {
		return fmt.Errorf("%w: visibility receipt assignment mismatch", ErrCoordinatorDelegationInvalid)
	}
	return nil
}

func validatePrimaryRestartGrantDelta(before, after DelegationRecord) error {
	if after.ActingCoordinator == nil {
		return fmt.Errorf("%w: primaryRestart history required", ErrCoordinatorDelegationInvalid)
	}
	to := *after.ActingCoordinator
	if to.StableAgentID != after.PrimaryCoordinator.StableAgentID {
		return fmt.Errorf("%w: primaryRestart only for primary stableAgentId", ErrCoordinatorDelegationInvalid)
	}
	if to.Generation <= before.highestPrimaryGeneration() {
		return fmt.Errorf("%w: restart requires a new generation", ErrCoordinatorDelegationInvalid)
	}
	if before.ActingCoordinator != nil && !before.IsBarred(*before.ActingCoordinator) {
		return fmt.Errorf("%w: predecessor still acting", ErrCoordinatorDelegationInvalid)
	}
	added := after.History[len(before.History):]
	if len(added) != 1 || added[0].Kind != DelegationHistoryPrimaryRestart || added[0].To == nil || !added[0].To.Equal(to) {
		return fmt.Errorf("%w: primaryRestart history required", ErrCoordinatorDelegationInvalid)
	}
	h := added[0]
	loss, ok := before.latestUnconsumedLoss()
	if !ok || h.ReceiptID == "" || h.ReceiptID != loss.ReceiptID {
		return fmt.Errorf("%w: visibility receipt loss record mismatch", ErrCoordinatorDelegationInvalid)
	}
	if h.AssignmentID != loss.AssignmentID {
		return fmt.Errorf("%w: visibility receipt assignment mismatch", ErrCoordinatorDelegationInvalid)
	}
	consumed := newlyConsumedVisibility(before, after)
	if len(consumed) == 0 {
		return fmt.Errorf("%w: visibility receipt required", ErrCoordinatorDelegationInvalid)
	}
	if len(consumed) != 1 {
		return fmt.Errorf("%w: visibility receipt assignment mismatch", ErrCoordinatorDelegationInvalid)
	}
	v := consumed[0]
	if v.MessageID != h.ReceiptDig {
		return fmt.Errorf("%w: visibility receipt loss record mismatch", ErrCoordinatorDelegationInvalid)
	}
	if v.Successor.StableAgentID != "" || v.Successor.SessionID != "" {
		if v.Successor.StableAgentID != to.StableAgentID || v.Successor.SessionID != to.SessionID {
			return fmt.Errorf("%w: visibility receipt successor mismatch", ErrCoordinatorDelegationInvalid)
		}
		if v.Successor.Generation != to.Generation {
			return fmt.Errorf("%w: visibility receipt generation mismatch", ErrCoordinatorDelegationInvalid)
		}
	} else if v.StableAgentID != to.StableAgentID {
		return fmt.Errorf("%w: visibility receipt successor mismatch", ErrCoordinatorDelegationInvalid)
	}
	return nil
}

// findGrantCoveringCoordinator finds an active grant that named the triple as
// primary, acting, or successor (used for binding / loss / handoff gates).
func findGrantCoveringCoordinator(state CanonicalState, coord assignment.Principal, now time.Time) (DelegationRecord, bool) {
	if !coordinatorDelegationActive() || state.CoordinatorDelegations == nil {
		return DelegationRecord{}, false
	}
	want := principalFromAssignment(coord)
	for _, d := range state.CoordinatorDelegations {
		if !d.Active(now) {
			continue
		}
		if d.PrimaryCoordinator.Equal(want) || d.CurrentActing().Equal(want) {
			return d, true
		}
		for _, s := range d.Successors {
			if s.Equal(want) {
				return d, true
			}
		}
	}
	return DelegationRecord{}, false
}

func (d DelegationRecord) hasSuccessor(p DelegationPrincipal) bool {
	for _, s := range d.Successors {
		if s.Equal(p) {
			return true
		}
	}
	return false
}

func (d DelegationRecord) hasFreshVisibility(p DelegationPrincipal, now time.Time) (VisibilityConfirmation, bool) {
	for i := len(d.VisibilityReceipts) - 1; i >= 0; i-- {
		v := d.VisibilityReceipts[i]
		if !v.Available(now) {
			continue
		}
		if v.Successor.Equal(p) {
			return v, true
		}
	}
	return VisibilityConfirmation{}, false
}

func (d DelegationRecord) hasFreshPrimaryRestartVisibility(stableAgentID string, now time.Time) (VisibilityConfirmation, bool) {
	for i := len(d.VisibilityReceipts) - 1; i >= 0; i-- {
		v := d.VisibilityReceipts[i]
		if !v.Available(now) {
			continue
		}
		if v.StableAgentID == stableAgentID && v.Successor.StableAgentID == "" {
			return v, true
		}
		// primaryRestart confirmations may also carry the new triple in Successor
		// with StableAgentID set; accept either form.
		if v.StableAgentID == stableAgentID {
			return v, true
		}
	}
	return VisibilityConfirmation{}, false
}

func (d DelegationRecord) lossAlreadyRecorded(p DelegationPrincipal) bool {
	for _, h := range d.History {
		if h.Kind == DelegationHistoryLossRecorded && h.From != nil && h.From.Equal(p) {
			return true
		}
	}
	return false
}

// primaryRestartConsumedLoss reports whether lossRecordID was already used by a
// PRIMARY_RESTART (OWNER F5: at most once per distinct loss event).
func (d DelegationRecord) primaryRestartConsumedLoss(lossRecordID string) bool {
	if lossRecordID == "" {
		return false
	}
	for _, h := range d.History {
		if h.Kind == DelegationHistoryPrimaryRestart && h.ReceiptID == lossRecordID {
			return true
		}
	}
	return false
}

// highestPrimaryGeneration is the highest generation already bound to the
// primary stableAgentId on this grant (primary, acting, barred, history).
// ADR-0010 §8: primaryRestart must present a strictly newer generation.
// AgentSession issues each new address at generation 1; the numeric generation
// is still comparable for this stableAgentId across session ids.
func (d DelegationRecord) highestPrimaryGeneration() uint64 {
	max := d.PrimaryCoordinator.Generation
	consider := func(p *DelegationPrincipal) {
		if p == nil || p.StableAgentID != d.PrimaryCoordinator.StableAgentID {
			return
		}
		if p.Generation > max {
			max = p.Generation
		}
	}
	if d.ActingCoordinator != nil {
		consider(d.ActingCoordinator)
	}
	for i := range d.BarredCoordinators {
		consider(&d.BarredCoordinators[i])
	}
	for i := range d.History {
		consider(d.History[i].From)
		consider(d.History[i].To)
	}
	return max
}

// latestUnconsumedLoss returns the newest LOSS_RECORDED entry whose ReceiptID
// has not yet been bound to a PRIMARY_RESTART. lossRecordID is the OWNER
// loss.confirm message id stored on the history entry.
func (d DelegationRecord) latestUnconsumedLoss() (DelegationHistoryEntry, bool) {
	for i := len(d.History) - 1; i >= 0; i-- {
		h := d.History[i]
		if h.Kind != DelegationHistoryLossRecorded || h.ReceiptID == "" {
			continue
		}
		if d.primaryRestartConsumedLoss(h.ReceiptID) {
			continue
		}
		return h, true
	}
	return DelegationHistoryEntry{}, false
}

// enforceDelegatedMutation gates Offer/Change/Accept under an active grant.
// When no grant covers the actor, the #90 path is unchanged (ADR §14/#18).
func findAnyGrantCoveringCoordinator(state CanonicalState, coord assignment.Principal) (DelegationRecord, bool) {
	if !coordinatorDelegationActive() || state.CoordinatorDelegations == nil {
		return DelegationRecord{}, false
	}
	want := principalFromAssignment(coord)
	for _, d := range state.CoordinatorDelegations {
		if d.PrimaryCoordinator.Equal(want) || (d.ActingCoordinator != nil && d.ActingCoordinator.Equal(want)) || d.hasSuccessor(want) || d.IsBarred(want) {
			return d, true
		}
	}
	return DelegationRecord{}, false
}

func enforceDelegatedMutation(state CanonicalState, actor assignment.Principal, action string, record assignment.Record, now time.Time) error {
	if !coordinatorDelegationActive() {
		return nil
	}
	actorP := principalFromAssignment(actor)
	if record.DelegationBinding == nil {
		if _, covered := findAnyGrantCoveringCoordinator(state, record.Coordinator()); covered || grantMentionsStableCoordinator(state, record.CoordinatorAgentID, now) {
			return fmt.Errorf("%w: persisted assignment grant binding required", ErrCoordinatorDelegationInvalid)
		}
		return nil
	}
	if bound, ok := state.CoordinatorDelegations[record.DelegationBinding.GrantID]; ok && !bound.Active(now) {
		return fmt.Errorf("%w: originating grant revoked or expired", ErrCoordinatorDelegationExhausted)
	}
	bound, err := assignmentBoundGrant(state, record)
	if err != nil {
		return err
	}
	if !bound.Active(now) {
		return fmt.Errorf("%w: exhausted, revoked, expired or barred", ErrCoordinatorDelegationExhausted)
	}
	if bound.IsBarred(actorP) || !bound.CurrentActing().Equal(actorP) {
		return fmt.Errorf("%w: binding requires exact acting triple", ErrCoordinatorDelegationInvalid)
	}
	if delegatedForbiddenActions[action] {
		return fmt.Errorf("%w: action %s forbidden under delegation", ErrCoordinatorDelegationInvalid, action)
	}
	if !bound.Scope.AllowsAction(action) {
		return fmt.Errorf("%w: action %s outside allowedActions", ErrCoordinatorDelegationInvalid, action)
	}
	switch action {
	case DelegatedActionVoid:
		if record.State != assignment.Offered && record.State != assignment.Acked {
			return fmt.Errorf("%w: delegated VOID only OFFERED/ACKED", ErrCoordinatorDelegationInvalid)
		}
	case DelegatedActionExpire:
		if now.Before(record.ValidUntil) {
			return fmt.Errorf("%w: delegated EXPIRE before validUntil", ErrCoordinatorDelegationInvalid)
		}
	case "SUSPEND", "RECONCILE":
		return fmt.Errorf("%w: action not delegated", ErrCoordinatorDelegationInvalid)
	}
	return nil
}

// SuccessorSkipReason names why an ordered successor entry was skipped.
type SuccessorSkipReason string

const (
	SkipNotOnList          SuccessorSkipReason = "outside successor list"
	SkipObserver           SuccessorSkipReason = "loss observer"
	SkipTarget             SuccessorSkipReason = "affected target"
	SkipBarred             SuccessorSkipReason = "barred"
	SkipNoVisibility       SuccessorSkipReason = "нет обязательного свидетельства видимости"
	SkipExpiredOrRevoked   SuccessorSkipReason = "delegation expired or revoked"
	SkipGenerationMismatch SuccessorSkipReason = "generation mismatch"
	SkipSessionInactive    SuccessorSkipReason = "session inactive or mismatched"
)

// SelectDelegationSuccessor walks the ordered successor list and returns the
// first eligible entry, recording skip reasons on a copy of the grant.
func SelectDelegationSuccessor(state CanonicalState, d DelegationRecord, assignmentID string, now time.Time) (DelegationPrincipal, []DelegationHistoryEntry, error) {
	if !d.Active(now) {
		return DelegationPrincipal{}, nil, fmt.Errorf("%w: grant expired or revoked", ErrCoordinatorDelegationExhausted)
	}
	var skips []DelegationHistoryEntry
	var observer assignment.Principal
	var hasObserver bool
	var target assignment.Principal
	if state.AssignmentLedger != nil {
		if r, ok := state.AssignmentLedger.Records[assignmentID]; ok {
			target = r.Target()
			observer, hasObserver = state.AssignmentLedger.OpenCoordinatorLossObserver(assignmentID)
		}
	}
	for _, cand := range d.Successors {
		reason := ""
		switch {
		case d.IsBarred(cand):
			reason = string(SkipBarred)
		case hasObserver && cand.Equal(principalFromAssignment(observer)):
			reason = string(SkipObserver)
		case cand.Equal(principalFromAssignment(target)):
			reason = string(SkipTarget)
		case assignmentPrincipal(state, cand.AsAssignment()) != nil:
			reason = string(SkipSessionInactive)
		default:
			if _, ok := d.hasFreshVisibility(cand, now); !ok {
				reason = string(SkipNoVisibility)
			}
		}
		if reason != "" {
			c := cand
			skips = append(skips, DelegationHistoryEntry{
				Kind: DelegationHistorySuccessorSkipped, At: now, Reason: reason,
				AssignmentID: assignmentID, To: &c,
			})
			continue
		}
		return cand, skips, nil
	}
	return DelegationPrincipal{}, skips, fmt.Errorf("%w: no eligible successor", ErrCoordinatorDelegationExhausted)
}

// enforceDelegatedHandoff applies ADR §8 ordered-successor + visibility rules
// when the assignment's predecessor is under an active grant.
func enforceDelegatedHandoff(state *CanonicalState, previous, next assignment.Principal, id string, now time.Time) error {
	if !coordinatorDelegationActive() {
		return nil
	}
	var grant DelegationRecord
	if state.AssignmentLedger != nil {
		if record, exists := state.AssignmentLedger.Records[id]; exists && record.DelegationBinding != nil {
			bound, err := assignmentBoundGrant(*state, record)
			if err != nil {
				return err
			}
			grant = bound
		} else if prior, covered := findAnyGrantCoveringCoordinator(*state, previous); covered {
			return fmt.Errorf("%w: assignment %s requires persisted grant binding (grant %s)", ErrCoordinatorDelegationInvalid, id, prior.DelegationID)
		} else {
			// No grant: #90 handoff unchanged. Absence of delegation does not mint authority (#18).
			return nil
		}
	} else if prior, covered := findAnyGrantCoveringCoordinator(*state, previous); covered {
		return fmt.Errorf("%w: assignment ledger missing for grant %s", ErrCoordinatorDelegationInvalid, prior.DelegationID)
	} else {
		return nil
	}
	if !grant.Active(now) {
		return fmt.Errorf("%w: grant expired or revoked", ErrCoordinatorDelegationExhausted)
	}
	previousP := principalFromAssignment(previous)
	if !grant.PrimaryCoordinator.Equal(previousP) && !grant.CurrentActing().Equal(previousP) && !grant.hasSuccessor(previousP) && !grant.IsBarred(previousP) {
		return fmt.Errorf("%w: handoff predecessor is outside bound grant", ErrCoordinatorDelegationInvalid)
	}
	if !grant.CurrentActing().Equal(previousP) {
		record, exists := state.AssignmentLedger.Records[id]
		_, observerLossOpen := state.AssignmentLedger.OpenCoordinatorLossObserver(id)
		ownerLossOpen := exists && record.CoordinatorLost && ownerCredentialLossAllowsHandoffWithoutObserver(*state, id)
		if !exists || !record.CoordinatorLost || (!ownerLossOpen && !observerLossOpen) {
			return fmt.Errorf("%w: handoff source is not acting coordinator and assignment has no matching loss", ErrCoordinatorDelegationInvalid)
		}
	}
	nextP := principalFromAssignment(next)
	siblingHandoff := false
	restartPrimaryHandoff := false
	restartPrimaryTarget := false
	if record, exists := state.AssignmentLedger.Records[id]; exists && record.CoordinatorLost && ownerCredentialLossAllowsHandoffWithoutObserver(*state, id) {
		loss, covered := lossHistoryForAssignment(grant, id, previousP)
		if !covered {
			return fmt.Errorf("%w: OWNER loss history does not cover assignment", ErrCoordinatorDelegationInvalid)
		}
		acting := grant.CurrentActing()
		if acting.Valid() && !grant.IsBarred(acting) {
			restart, isRestartedPrimary := currentPrimaryRestart(grant)
			preRestartLosses := lossesBeforePrimaryRestart(grant, restart)
			restartPrimaryTarget = isRestartedPrimary && acting.Equal(nextP) && restart.ReceiptDig != ""
			if restartPrimaryTarget && preRestartLosses[loss.ReceiptID] {
				restartPrimaryHandoff = true
			} else if contextID, _ := actingLossContext(grant); contextID == loss.ReceiptID {
				if !acting.Equal(nextP) {
					return fmt.Errorf("%w: sibling handoff must retain selected successor", ErrCoordinatorDelegationInvalid)
				}
				siblingHandoff = true
			} else if !restartPrimaryTarget {
				selected, selectedBefore := selectedSuccessorForLoss(grant, loss.ReceiptID)
				if acting.StableAgentID == grant.PrimaryCoordinator.StableAgentID || selectedBefore && !grant.IsBarred(selected) && !selected.Equal(acting) {
					return fmt.Errorf("%w: another unbarred coordinator is acting", ErrCoordinatorDelegationInvalid)
				}
				if !acting.Equal(nextP) {
					return fmt.Errorf("%w: new loss capture must use the acting successor", ErrCoordinatorDelegationInvalid)
				}
			}
		}
	}
	if !grant.hasSuccessor(nextP) && !restartPrimaryTarget {
		return fmt.Errorf("%w: handoff target outside successor list", ErrCoordinatorDelegationInvalid)
	}
	// Loss observer / target exclusions (path a); path b has no AgentSession observer.
	if r, exists := state.AssignmentLedger.Records[id]; exists {
		if nextP.Equal(principalFromAssignment(r.Target())) {
			return fmt.Errorf("%w: affected target cannot become successor", ErrCoordinatorDelegationInvalid)
		}
		if observer, open := state.AssignmentLedger.OpenCoordinatorLossObserver(id); open {
			if nextP.Equal(principalFromAssignment(observer)) {
				return fmt.Errorf("%w: loss observer cannot become successor", ErrCoordinatorDelegationInvalid)
			}
		}
	}
	visibilityID := ""
	if siblingHandoff {
		_, visibilityID = actingLossContext(grant)
		if visibilityID == "" {
			return fmt.Errorf("%w: selected successor visibility missing", ErrCoordinatorDelegationInvalid)
		}
	} else if restartPrimaryHandoff {
		restart, ok := currentPrimaryRestart(grant)
		if !ok || restart.ReceiptDig == "" {
			return fmt.Errorf("%w: primaryRestart record required for lost assignment", ErrCoordinatorDelegationInvalid)
		}
		visibilityID = restart.ReceiptDig
	} else {
		if !restartPrimaryTarget {
			eligible, skips, err := SelectDelegationSuccessor(*state, grant, id, now)
			grant.History = append(grant.History, skips...)
			if err != nil {
				state.CoordinatorDelegations[grant.DelegationID] = grant
				return err
			}
			if !eligible.Equal(nextP) {
				state.CoordinatorDelegations[grant.DelegationID] = grant
				return fmt.Errorf("%w: must hand off to first eligible successor", ErrCoordinatorDelegationInvalid)
			}
		}
		vis, ok := grant.hasFreshVisibility(nextP, now)
		if !ok {
			if restartPrimaryTarget {
				return fmt.Errorf("%w: post-restart loss requires fresh visibility", ErrCoordinatorDelegationInvalid)
			}
			return fmt.Errorf("%w: %s", ErrCoordinatorDelegationInvalid, SkipNoVisibility)
		}
		visibilityID = vis.MessageID
		// Consume visibility in the same commit.
		for i := range grant.VisibilityReceipts {
			if grant.VisibilityReceipts[i].MessageID == vis.MessageID {
				grant.VisibilityReceipts[i].ConsumedAt = now
				grant.VisibilityReceipts[i].ConsumedBy = id
				break
			}
		}
	}
	acting := nextP
	grant.ActingCoordinator = &acting
	from := principalFromAssignment(previous)
	grant.History = append(grant.History, DelegationHistoryEntry{
		Kind: DelegationHistoryHandoff, At: now, AssignmentID: id,
		From: &from, To: &acting, ReceiptDig: visibilityID,
	})
	state.CoordinatorDelegations[grant.DelegationID] = grant
	return nil
}

// enforceDelegatedOfferRejectsAutoCapture blocks offer/accept when the grant
// is exhausted, revoked, or expired (ADR §11).
func enforceDelegatedOfferOrAccept(state CanonicalState, actor assignment.Principal, action string, now time.Time) error {
	if !coordinatorDelegationActive() || state.CoordinatorDelegations == nil {
		return nil
	}
	// The later offer/result checks bind the action to one exact grant. A revoked
	// historical grant cannot veto a new active grant, but remains a blocker when
	// no active grant covers the actor.
	want := principalFromAssignment(actor)
	activeAllowed, activeBarred, inactiveCovered := false, false, false
	for _, d := range state.CoordinatorDelegations {
		covered := d.PrimaryCoordinator.Equal(want) || d.hasSuccessor(want)
		if d.ActingCoordinator != nil && d.ActingCoordinator.Equal(want) {
			covered = true
		}
		if !covered && !d.IsBarred(want) {
			continue
		}
		if !d.Active(now) {
			inactiveCovered = true
			continue
		}
		if d.IsBarred(want) {
			activeBarred = true
		} else {
			activeAllowed = true
		}
	}
	if activeAllowed {
		_ = action
		return nil
	}
	if activeBarred || inactiveCovered {
		return fmt.Errorf("%w: exhausted, revoked, expired or barred", ErrCoordinatorDelegationExhausted)
	}
	return nil
}

// ApplyPrimaryRestart activates a fresh primary session after recorded loss
// (ADR §8 / OWNER F5). primaryRestart is not globally consumable; it may be
// applied at most once per distinct loss event. The success history entry binds
// lossRecordID (ReceiptID), replacement session/generation (To), and the
// consumed visibility confirmation id (Reason carries evidence; visibility
// ConsumedBy records the bind).
func (s *CanonicalStore) ApplyPrimaryRestart(delegationID string, newSession assignment.Principal, evidence string) error {
	if !coordinatorDelegationActive() {
		return ErrCoordinatorDelegationDisabled
	}
	return s.commitCanonical(delegationCommitPrimaryRestart, func(state *CanonicalState) error {
		now := s.now().UTC()
		rec, ok := state.CoordinatorDelegations[delegationID]
		if !ok || !rec.Active(now) {
			return fmt.Errorf("%w: grant missing/expired/revoked", ErrCoordinatorDelegationInvalid)
		}
		if rec.PrimaryRestart == nil || rec.PrimaryRestart.StableAgentID != newSession.AgentID {
			return fmt.Errorf("%w: primaryRestart stableAgentId mismatch", ErrCoordinatorDelegationInvalid)
		}
		if newSession.AgentID != rec.PrimaryCoordinator.StableAgentID {
			return fmt.Errorf("%w: primaryRestart only for primary stableAgentId", ErrCoordinatorDelegationInvalid)
		}
		// Old grant-primary session or exact prior triple rejected; a fresh session may start at generation 1.
		if newSession.SessionID == rec.PrimaryCoordinator.SessionID {
			return fmt.Errorf("%w: restart requires new sessionId and generation", ErrCoordinatorDelegationInvalid)
		}
		if principalFromAssignment(newSession).Equal(rec.PrimaryCoordinator) {
			return fmt.Errorf("%w: restart cannot reuse prior triple", ErrCoordinatorDelegationInvalid)
		}
		if rec.ActingCoordinator != nil && !rec.IsBarred(*rec.ActingCoordinator) {
			// Predecessor still acting — no dual coordinators.
			return fmt.Errorf("%w: predecessor still acting", ErrCoordinatorDelegationInvalid)
		}
		// F5: require a distinct unconsumed loss event (lossRecordID = loss receipt id).
		lossEntry, ok := rec.latestUnconsumedLoss()
		if !ok {
			return fmt.Errorf("%w: predecessor loss not recorded or already consumed by primaryRestart", ErrCoordinatorDelegationInvalid)
		}
		if rec.primaryRestartConsumedLoss(lossEntry.ReceiptID) {
			return fmt.Errorf("%w: primaryRestart already applied for lossRecordID %s", ErrCoordinatorDelegationInvalid, lossEntry.ReceiptID)
		}
		// Prior primary / lost triple must be barred.
		if lossEntry.From != nil && !rec.IsBarred(*lossEntry.From) {
			return fmt.Errorf("%w: lost coordinator not barred", ErrCoordinatorDelegationInvalid)
		}
		// F5: new loss still requires a fresh session — never reuse the lost
		// session id (even with a bumped generation) or the exact lost triple.
		if lossEntry.From != nil {
			if newSession.SessionID == lossEntry.From.SessionID {
				return fmt.Errorf("%w: restart requires new sessionId (lost session)", ErrCoordinatorDelegationInvalid)
			}
			if principalFromAssignment(newSession).Equal(*lossEntry.From) {
				return fmt.Errorf("%w: restart cannot reuse lost triple", ErrCoordinatorDelegationInvalid)
			}
		}
		// Also reject any previously barred or previously installed primary-restart session.
		for _, barred := range rec.BarredCoordinators {
			if newSession.SessionID == barred.SessionID {
				return fmt.Errorf("%w: restart cannot reuse barred session", ErrCoordinatorDelegationInvalid)
			}
			if principalFromAssignment(newSession).Equal(barred) {
				return fmt.Errorf("%w: restart cannot reuse barred triple", ErrCoordinatorDelegationInvalid)
			}
		}
		for _, h := range rec.History {
			if h.Kind != DelegationHistoryPrimaryRestart || h.To == nil {
				continue
			}
			if newSession.SessionID == h.To.SessionID {
				return fmt.Errorf("%w: restart cannot reuse prior restart session", ErrCoordinatorDelegationInvalid)
			}
		}
		// Distinct session is not enough: generation must be strictly newer than
		// every generation already bound to this primary stableAgentId.
		if newSession.Generation <= rec.highestPrimaryGeneration() {
			return fmt.Errorf("%w: restart requires a new generation", ErrCoordinatorDelegationInvalid)
		}
		if err := assignmentPrincipal(*state, newSession); err != nil {
			return fmt.Errorf("%w: fresh session registration required", ErrCoordinatorDelegationInvalid)
		}
		vis, ok := rec.hasFreshPrimaryRestartVisibility(newSession.AgentID, now)
		if !ok {
			// Also accept visibility confirming the exact new triple.
			vis, ok = rec.hasFreshVisibility(principalFromAssignment(newSession), now)
		}
		if !ok {
			return fmt.Errorf("%w: fresh visibility required for primaryRestart", ErrCoordinatorDelegationInvalid)
		}
		for i := range rec.VisibilityReceipts {
			if rec.VisibilityReceipts[i].MessageID == vis.MessageID {
				rec.VisibilityReceipts[i].ConsumedAt = now
				rec.VisibilityReceipts[i].ConsumedBy = evidence
				break
			}
		}
		acting := principalFromAssignment(newSession)
		from := rec.PrimaryCoordinator
		if lossEntry.From != nil {
			from = *lossEntry.From
		}
		rec.ActingCoordinator = &acting
		rec.History = append(rec.History, DelegationHistoryEntry{
			Kind: DelegationHistoryPrimaryRestart, At: now, Actor: newSession.SessionID,
			Reason: evidence, From: &from, To: &acting,
			AssignmentID: lossEntry.AssignmentID,
			ReceiptID:    lossEntry.ReceiptID, // bind success to lossRecordID
			ReceiptDig:   vis.MessageID,       // bind success to visibility confirmation id
		})
		state.CoordinatorDelegations[delegationID] = rec
		return nil
	})
}
