package store

import (
	"coord/assignment"
	ownercredential "coord/ownercredential"
	"fmt"
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
	DelegationID         string                    `json:"delegationId"`
	Scope                DelegationScope           `json:"scope"`
	PrimaryCoordinator   DelegationPrincipal       `json:"primaryCoordinator"`
	Validity             DelegationValidity        `json:"validity"`
	Successors           []DelegationPrincipal     `json:"successors"`
	PrimaryRestart       *DelegationPrimaryRestart `json:"primaryRestart,omitempty"`
	RevokedAt            time.Time                 `json:"revokedAt,omitempty"`
	RevokedBy            string                    `json:"revokedBy,omitempty"`
	Evidence             DelegationEvidence        `json:"evidence"`
	History              []DelegationHistoryEntry  `json:"history"`
	CutoverAssignmentIDs []string                  `json:"cutoverAssignmentIds,omitempty"`
	BarredCoordinators   []DelegationPrincipal     `json:"barredCoordinators,omitempty"`
	VisibilityReceipts   []VisibilityConfirmation  `json:"visibilityReceipts,omitempty"`
	ActingCoordinator    *DelegationPrincipal      `json:"actingCoordinator,omitempty"`
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

func validateDelegations(state CanonicalState) error {
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
	if !coordinatorDelegationActive() || state.CoordinatorDelegations == nil {
		return DelegationRecord{}, false
	}
	want := principalFromAssignment(actor)
	for _, d := range state.CoordinatorDelegations {
		if !d.Active(now) || d.IsBarred(want) {
			continue
		}
		if d.CurrentActing().Equal(want) {
			return d, true
		}
	}
	return DelegationRecord{}, false
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
	grant, underGrant := findGrantForActingCoordinator(state, actor, now)
	covering, covered := findGrantCoveringCoordinator(state, record.Coordinator(), now)
	if !underGrant && !covered {
		return nil
	}
	// Assignment under a grant requires the exact acting triple (ADR §4).
	if covered {
		acting := covering.CurrentActing()
		if covering.IsBarred(actorP) || !acting.Equal(actorP) {
			return fmt.Errorf("%w: binding requires exact acting triple", ErrCoordinatorDelegationInvalid)
		}
		grant = covering
		underGrant = true
	}
	if !underGrant {
		return fmt.Errorf("%w: actor is not acting coordinator under grant", ErrCoordinatorDelegationInvalid)
	}
	if grant.IsBarred(actorP) {
		return fmt.Errorf("%w: barred coordinator until handoff", ErrCoordinatorDelegationInvalid)
	}
	if delegatedForbiddenActions[action] {
		return fmt.Errorf("%w: action %s forbidden under delegation", ErrCoordinatorDelegationInvalid, action)
	}
	if !grant.Scope.AllowsAction(action) {
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
// (ADR §8). Requires fresh visibility, new session/generation, no dual acting.
func (s *CanonicalStore) ApplyPrimaryRestart(delegationID string, newSession assignment.Principal, evidence string) error {
	if !coordinatorDelegationActive() {
		return ErrCoordinatorDelegationDisabled
	}
	return s.UpdateCanonical(func(state *CanonicalState) error {
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
		// Old session or exact prior triple rejected; a fresh session may start at generation 1.
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
		// Prior primary must be barred / loss recorded.
		if !rec.IsBarred(rec.PrimaryCoordinator) && !rec.lossAlreadyRecorded(rec.PrimaryCoordinator) {
			return fmt.Errorf("%w: predecessor loss not recorded", ErrCoordinatorDelegationInvalid)
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
		rec.ActingCoordinator = &acting
		rec.History = append(rec.History, DelegationHistoryEntry{
			Kind: DelegationHistoryPrimaryRestart, At: now, Actor: newSession.SessionID,
			Reason: evidence, From: &from, To: &acting,
		})
		state.CoordinatorDelegations[delegationID] = rec
		return nil
	})
}
