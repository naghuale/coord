package store

import (
	"coord/assignment"
	ownercredential "coord/ownercredential"
	"fmt"
	"reflect"
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
	Kind         DelegationHistoryKind `json:"kind"`
	At           time.Time             `json:"at"`
	Actor        string                `json:"actor,omitempty"`
	Reason       string                `json:"reason,omitempty"`
	AssignmentID string                `json:"assignmentId,omitempty"`
	From         *DelegationPrincipal  `json:"from,omitempty"`
	To           *DelegationPrincipal  `json:"to,omitempty"`
	ReceiptID    string                `json:"receiptId,omitempty"`
	ReceiptDig   string                `json:"receiptDigest,omitempty"`
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
	activeKeys := map[string]string{}
	for id, d := range state.CoordinatorDelegations {
		if id != d.DelegationID {
			return fmt.Errorf("delegation map key %q does not match id %q", id, d.DelegationID)
		}
		if err := d.Validate(); err != nil {
			return err
		}
		if d.RevokedAt.IsZero() {
			key := d.Scope.Repository + "\x00" + d.Scope.Project
			if prior, ok := activeKeys[key]; ok {
				return fmt.Errorf("%w: overlapping active grant %q and %q", ErrCoordinatorDelegationInvalid, prior, id)
			}
			activeKeys[key] = id
		}
		if err := validateOwnerCredentialLossReceipts(state, d); err != nil {
			return err
		}
	}
	return nil
}

func validateOwnerCredentialLossReceipts(state CanonicalState, d DelegationRecord) error {
	if state.AssignmentLedger == nil {
		return nil
	}
	for _, e := range state.AssignmentLedger.Events {
		if e.Kind != "COORDINATOR_LOST" || e.ActorSource != assignment.ActorSourceOwnerCredential {
			continue
		}
		// OWNER_CREDENTIAL loss must reference a same-commit receipt.
		if e.OwnerReceiptMessageID == "" || e.OwnerReceiptDigest == "" || e.OwnerPrincipalID == "" || e.OwnerCredentialID == "" {
			return fmt.Errorf("%w: OWNER_CREDENTIAL loss missing receipt binding", ErrCoordinatorDelegationInvalid)
		}
		agg, ok := state.OwnerCredentials[e.OwnerPrincipalID]
		if !ok {
			return fmt.Errorf("%w: OWNER_CREDENTIAL loss principal missing", ErrCoordinatorDelegationInvalid)
		}
		receipt, ok := agg.Receipts[e.OwnerReceiptMessageID]
		if !ok || receipt.CommandDigest != e.OwnerReceiptDigest || receipt.CommandKind != ownercredential.CommandLossConfirm {
			return fmt.Errorf("%w: OWNER_CREDENTIAL loss receipt missing or mismatched", ErrCoordinatorDelegationInvalid)
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
	if !ok || receipt.CommandDigest != lost.OwnerReceiptDigest || receipt.CommandKind != ownercredential.CommandLossConfirm {
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
		return validateHandoffGrantDelta(next, before, after)
	case delegationCommitPrimaryRestart:
		return validatePrimaryRestartGrantDelta(before, after)
	default:
		return fmt.Errorf("%w: acting coordinator requires handoff or primaryRestart", ErrCoordinatorDelegationInvalid)
	}
}

func validateHandoffGrantDelta(next CanonicalState, before, after DelegationRecord) error {
	if after.ActingCoordinator == nil {
		return fmt.Errorf("%w: handoff history required", ErrCoordinatorDelegationInvalid)
	}
	to := *after.ActingCoordinator
	if !after.hasSuccessor(to) {
		return fmt.Errorf("%w: handoff target outside successor list", ErrCoordinatorDelegationInvalid)
	}
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
	consumed := newlyConsumedVisibility(before, after)
	if len(consumed) == 0 {
		return fmt.Errorf("%w: visibility receipt required", ErrCoordinatorDelegationInvalid)
	}
	if len(consumed) != 1 {
		return fmt.Errorf("%w: visibility receipt assignment mismatch", ErrCoordinatorDelegationInvalid)
	}
	v := consumed[0]
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
	// Revoked/expired/barred grants still block the former acting triple (ADR §7/§11).
	if prior, ok := findAnyGrantCoveringCoordinator(state, actor); ok {
		if !prior.Active(now) || prior.IsBarred(actorP) {
			return fmt.Errorf("%w: exhausted, revoked, expired or barred", ErrCoordinatorDelegationExhausted)
		}
	}
	if record.DelegationBinding == nil {
		if _, covered := findAnyGrantCoveringCoordinator(state, record.Coordinator()); covered {
			return fmt.Errorf("%w: persisted assignment grant binding required", ErrCoordinatorDelegationInvalid)
		}
		return nil
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
	grant, ok := findGrantCoveringCoordinator(*state, previous, now)
	if !ok {
		// No grant: #90 handoff unchanged. Absence of delegation does not mint authority (#18).
		return nil
	}
	if !grant.Active(now) {
		return fmt.Errorf("%w: grant expired or revoked", ErrCoordinatorDelegationExhausted)
	}
	nextP := principalFromAssignment(next)
	if !grant.hasSuccessor(nextP) {
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
	vis, ok := grant.hasFreshVisibility(nextP, now)
	if !ok {
		return fmt.Errorf("%w: %s", ErrCoordinatorDelegationInvalid, SkipNoVisibility)
	}
	// Consume visibility in the same commit.
	for i := range grant.VisibilityReceipts {
		if grant.VisibilityReceipts[i].MessageID == vis.MessageID {
			grant.VisibilityReceipts[i].ConsumedAt = now
			grant.VisibilityReceipts[i].ConsumedBy = id
			break
		}
	}
	acting := nextP
	grant.ActingCoordinator = &acting
	from := principalFromAssignment(previous)
	grant.History = append(grant.History, DelegationHistoryEntry{
		Kind: DelegationHistoryHandoff, At: now, AssignmentID: id,
		From: &from, To: &acting,
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
	// If actor matches a barred or formerly-acting triple under a non-active grant, reject.
	want := principalFromAssignment(actor)
	for _, d := range state.CoordinatorDelegations {
		covered := d.PrimaryCoordinator.Equal(want) || d.hasSuccessor(want)
		if d.ActingCoordinator != nil && d.ActingCoordinator.Equal(want) {
			covered = true
		}
		if !covered && !d.IsBarred(want) {
			continue
		}
		if !d.Active(now) || d.IsBarred(want) {
			return fmt.Errorf("%w: exhausted, revoked, expired or barred", ErrCoordinatorDelegationExhausted)
		}
		_ = action
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
