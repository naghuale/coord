// Package assignment models assignment authority, independently of transport
// delivery and of the optional canonical Task/Run relation.
package assignment

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"
)

type State string

const (
	Offered   State = "OFFERED"
	Acked     State = "ACKED"
	Active    State = "ACTIVE"
	Suspended State = "SUSPENDED"
	Completed State = "COMPLETED"
	Expired   State = "EXPIRED"
	Void      State = "VOID"
)

var ErrInvalid = errors.New("ASSIGNMENT_INVALID")
var ErrUnsupportedFormat = errors.New("UNSUPPORTED_LEDGER_FORMAT")

func (s State) Terminal() bool { return s == Completed || s == Expired || s == Void }
func (s State) Valid() bool {
	return s == Offered || s == Acked || s == Active || s == Suspended || s.Terminal()
}

// Principal is obtained by a trusted command boundary, never inferred from a
// directory or an unverified transport payload. SessionID is the existing
// AgentSession address (the canonical session key, not a display alias).
type Principal struct {
	SessionID  string `json:"sessionId"`
	AgentID    string `json:"stableAgentId"`
	Generation uint64 `json:"generation"`
}

type AdmissionBasis struct {
	Kind            string `json:"kind"`
	Reference       string `json:"reference"`
	TargetSessionID string `json:"targetSessionId"`
	TargetAgentID   string `json:"targetAgentId"`
	Role            string `json:"role"`
	SubjectSHA      string `json:"subjectSha"`
}

type VisibilityEvidence struct {
	Reference  string    `json:"reference"`
	SessionID  string    `json:"sessionId"`
	ObservedAt time.Time `json:"observedAt"`
}

type Handoff struct {
	From        Principal `json:"from"`
	To          Principal `json:"to"`
	EvidenceRef string    `json:"evidenceRef"`
	At          time.Time `json:"at"`
}

// RunBinding is optional. Process review/audit/analysis assignments do not need
// a fabricated Task or Run. When present, the existing fence/attempt still apply.
type RunBinding struct {
	TaskID  string `json:"taskId"`
	RunID   string `json:"runId"`
	Fence   uint64 `json:"fence"`
	Attempt uint64 `json:"attempt"`
}

type Result struct {
	AssignmentID          string `json:"assignmentId"`
	SubjectSHA            string `json:"subjectSha"`
	Assignee              string `json:"assignee"`
	SessionID             string `json:"sessionId"`
	StableAgentID         string `json:"stableAgentId"`
	Role                  string `json:"role"`
	CoordinatorGeneration uint64 `json:"coordinatorGeneration"`
	ResultID              string `json:"resultId"`
	Digest                string `json:"digest"`
	EvidenceRef           string `json:"evidenceRef"`
}

type Record struct {
	AssignmentID          string             `json:"assignmentId"`
	State                 State              `json:"state"`
	Issuer                string             `json:"issuer"`
	IssuerAgentID         string             `json:"issuerAgentId"`
	IssuerGeneration      uint64             `json:"issuerGeneration"`
	CurrentCoordinator    string             `json:"currentCoordinator"`
	CoordinatorAgentID    string             `json:"coordinatorAgentId"`
	CoordinatorGeneration uint64             `json:"coordinatorGeneration"`
	HandoffEvidence       []Handoff          `json:"handoffEvidence"`
	CoordinatorLost       bool               `json:"coordinatorLost"`
	Assignee              string             `json:"assignee"`
	StableAgentID         string             `json:"stableAgentId"`
	SessionID             string             `json:"sessionId"`
	AssigneeGeneration    uint64             `json:"assigneeGeneration"`
	Role                  string             `json:"role"`
	SubjectSHA            string             `json:"subjectSha"`
	IssuedAt              time.Time          `json:"issuedAt"`
	ValidUntil            time.Time          `json:"validUntil"`
	AdmissionBasis        AdmissionBasis     `json:"admissionBasis"`
	VisibilityEvidence    VisibilityEvidence `json:"visibilityEvidence"`
	RunBinding            *RunBinding        `json:"runBinding,omitempty"`
	Revision              uint64             `json:"revision"`
	UpdatedAt             time.Time          `json:"updatedAt"`
	WorkStartedAt         time.Time          `json:"workStartedAt,omitempty"`
	AcknowledgedAt        time.Time          `json:"acknowledgedAt,omitempty"`
	AcceptedResult        *Result            `json:"acceptedResult,omitempty"`
}

func (r Record) Coordinator() Principal {
	return Principal{r.CurrentCoordinator, r.CoordinatorAgentID, r.CoordinatorGeneration}
}
func (r Record) Target() Principal {
	return Principal{r.SessionID, r.StableAgentID, r.AssigneeGeneration}
}

type Event struct {
	AssignmentID string    `json:"assignmentId"`
	Revision     uint64    `json:"revision"`
	Kind         string    `json:"kind"`
	Actor        Principal `json:"actor"`
	EvidenceRef  string    `json:"evidenceRef"`
	At           time.Time `json:"at"`
	Record       Record    `json:"record"`
}

// Ledger is persisted only inside the existing CanonicalState JSON commit.
// A nil ledger is pre-cutover legacy data; a non-nil ledger is never removed.
type Ledger struct {
	Version uint32            `json:"version"`
	Records map[string]Record `json:"records"`
	Events  []Event           `json:"events"`
}

func Empty() *Ledger { return &Ledger{Version: 1, Records: map[string]Record{}, Events: []Event{}} }

var hexSHA = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var digestSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)

func clean(s string) bool       { return s != "" && strings.TrimSpace(s) == s }
func (p Principal) Valid() bool { return clean(p.SessionID) && clean(p.AgentID) && p.Generation > 0 }

func (r Record) Validate() error {
	if !clean(r.AssignmentID) || !r.State.Valid() || !r.Coordinator().Valid() || !r.Target().Valid() ||
		!clean(r.Issuer) || !clean(r.IssuerAgentID) || r.IssuerGeneration == 0 || r.Assignee != r.SessionID ||
		!hexSHA.MatchString(r.SubjectSHA) || r.Revision == 0 || r.IssuedAt.IsZero() ||
		!r.ValidUntil.After(r.IssuedAt) || r.UpdatedAt.Before(r.IssuedAt) || r.HandoffEvidence == nil {
		return fmt.Errorf("%w: invalid assignment identity, state or clock", ErrInvalid)
	}
	switch r.Role {
	case "COORDINATOR", "EXECUTOR", "REVIEWER", "AUDITOR", "ANALYST", "DESIGNER", "IMPLEMENTER":
	default:
		return fmt.Errorf("%w: unknown role", ErrInvalid)
	}
	b := r.AdmissionBasis
	if (b.Kind != "POOL" && b.Kind != "R151" && b.Kind != "OWNER_EXCEPTION") || !clean(b.Reference) ||
		b.TargetSessionID != r.SessionID || b.TargetAgentID != r.StableAgentID || b.Role != r.Role || b.SubjectSHA != r.SubjectSHA {
		return fmt.Errorf("%w: admission basis does not bind the exact target/subject/role", ErrInvalid)
	}
	v := r.VisibilityEvidence
	if !clean(v.Reference) || v.SessionID != r.SessionID || v.ObservedAt.IsZero() || v.ObservedAt.After(r.IssuedAt) {
		return fmt.Errorf("%w: assignment-bound visibility evidence required", ErrInvalid)
	}
	current := Principal{r.Issuer, r.IssuerAgentID, r.IssuerGeneration}
	for _, h := range r.HandoffEvidence {
		if h.From != current || !h.To.Valid() || h.To == h.From || !clean(h.EvidenceRef) || h.At.Before(r.IssuedAt) || h.At.After(r.UpdatedAt) {
			return fmt.Errorf("%w: unconfirmed handoff", ErrInvalid)
		}
		current = h.To
	}
	if current != r.Coordinator() {
		return fmt.Errorf("%w: coordinator differs without handoff", ErrInvalid)
	}
	if r.RunBinding != nil && (!clean(r.RunBinding.TaskID) || !clean(r.RunBinding.RunID) || r.RunBinding.Fence == 0 || r.RunBinding.Attempt == 0) {
		return fmt.Errorf("%w: invalid optional Run binding", ErrInvalid)
	}
	if !r.WorkStartedAt.IsZero() && (r.WorkStartedAt.Before(r.IssuedAt) || r.WorkStartedAt.After(r.UpdatedAt)) {
		return fmt.Errorf("%w: invalid workStarted time", ErrInvalid)
	}
	if !r.AcknowledgedAt.IsZero() && (r.AcknowledgedAt.Before(r.IssuedAt) || r.AcknowledgedAt.After(r.UpdatedAt)) {
		return fmt.Errorf("%w: invalid ACK time", ErrInvalid)
	}
	if (r.State == Acked || r.State == Active || r.State == Completed) && r.AcknowledgedAt.IsZero() {
		return fmt.Errorf("%w: exact ACK not recorded", ErrInvalid)
	}
	if (r.State == Completed) != (r.AcceptedResult != nil) {
		return fmt.Errorf("%w: completion requires one result", ErrInvalid)
	}
	if r.AcceptedResult != nil {
		return r.MatchResult(*r.AcceptedResult)
	}
	return nil
}

func (r Record) MatchResult(v Result) error {
	if v.AssignmentID != r.AssignmentID || v.SubjectSHA != r.SubjectSHA || v.Assignee != r.Assignee ||
		v.SessionID != r.SessionID || v.StableAgentID != r.StableAgentID || v.Role != r.Role ||
		v.CoordinatorGeneration != r.CoordinatorGeneration || !clean(v.ResultID) || !digestSHA.MatchString(v.Digest) || !clean(v.EvidenceRef) {
		return fmt.Errorf("%w: result identity/subject/generation mismatch", ErrInvalid)
	}
	return nil
}

// Ready is the common start/resume/result predicate. Session freshness and any
// optional Task/Run fence are checked by the canonical owner in addition.
func (r Record) Ready(now time.Time) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if r.State != Active {
		return fmt.Errorf("%w: state %s is not ACTIVE", ErrInvalid, r.State)
	}
	if now.IsZero() || now.Before(r.IssuedAt) || !now.Before(r.ValidUntil) {
		return fmt.Errorf("%w: expired validUntil", ErrInvalid)
	}
	if r.CoordinatorLost {
		return fmt.Errorf("%w: current coordinator unavailable; verified handoff required", ErrInvalid)
	}
	return nil
}

// Append validates one coordinator-decided event before publishing its view.
// Transport observations are deliberately not a supported event kind.
func (l *Ledger) Append(e Event) error {
	if l == nil || l.Records == nil || l.Events == nil {
		return fmt.Errorf("%w: missing ledger", ErrInvalid)
	}
	prior, exists := l.Records[e.AssignmentID]
	if err := l.checkEvent(prior, exists, e); err != nil {
		return err
	}
	l.Records[e.AssignmentID] = e.Record
	l.Events = append(l.Events, e)
	return nil
}

// OpenCoordinatorLossObserver derives the current loss observer from history;
// it adds no persisted field or new authority. A handoff closes that interval.
func (l *Ledger) OpenCoordinatorLossObserver(id string) (Principal, bool) {
	if l == nil || !l.Records[id].CoordinatorLost || l.Records[id].State.Terminal() {
		return Principal{}, false
	}
	for i := len(l.Events) - 1; i >= 0; i-- {
		e := l.Events[i]
		if e.AssignmentID != id {
			continue
		}
		switch e.Kind {
		case "COORDINATOR_LOST":
			return e.Actor, true
		case "HANDOFF", "OFFER":
			return Principal{}, false
		}
	}
	return Principal{}, false
}

func (l *Ledger) checkEvent(prior Record, exists bool, e Event) error {
	r := e.Record
	if e.AssignmentID != r.AssignmentID || e.Revision != r.Revision || !e.At.Equal(r.UpdatedAt) || !e.Actor.Valid() || !clean(e.EvidenceRef) {
		return fmt.Errorf("%w: invalid event", ErrInvalid)
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if !exists {
		if e.Kind != "OFFER" || r.State != Offered || r.Revision != 1 || r.Issuer != r.CurrentCoordinator ||
			r.Coordinator() != e.Actor || r.IssuedAt.After(e.At) || r.CoordinatorLost || len(r.HandoffEvidence) != 0 || !r.WorkStartedAt.IsZero() || !r.AcknowledgedAt.IsZero() {
			return fmt.Errorf("%w: new ID must start OFFERED", ErrInvalid)
		}
		return nil
	}
	if prior.State.Terminal() {
		return fmt.Errorf("%w: terminal assignment cannot change or be reused", ErrInvalid)
	}
	if prior.Revision == ^uint64(0) || r.Revision != prior.Revision+1 || e.At.Before(prior.UpdatedAt) {
		return fmt.Errorf("%w: assignment revision/time mismatch", ErrInvalid)
	}
	want := prior
	want.Revision, want.UpdatedAt = r.Revision, e.At
	if e.Kind != "ACK" && e.Kind != "WORK_STARTED" && e.Kind != "COORDINATOR_LOST" && e.Kind != "HANDOFF" && e.Actor != prior.Coordinator() {
		return fmt.Errorf("%w: wrong currentCoordinator/generation", ErrInvalid)
	}
	if e.Kind != "VOID" && e.Kind != "EXPIRE" && e.Kind != "COORDINATOR_LOST" && e.Kind != "HANDOFF" &&
		(!e.At.Before(prior.ValidUntil) || prior.CoordinatorLost) {
		return fmt.Errorf("%w: authority expired or coordinator lost", ErrInvalid)
	}
	switch e.Kind {
	case "ACK":
		if prior.State != Offered || e.Actor != prior.Target() {
			return fmt.Errorf("%w: exact ACK required", ErrInvalid)
		}
		want.State = Acked
		want.AcknowledgedAt = e.At
	case "ACTIVATE":
		if prior.State != Acked {
			return fmt.Errorf("%w: ACKED required for activation", ErrInvalid)
		}
		want.State = Active
	case "SUSPEND":
		want.State = Suspended
	case "RECONCILE":
		if prior.State != Suspended {
			return fmt.Errorf("%w: SUSPENDED required", ErrInvalid)
		}
		want.State = Acked
		if prior.AcknowledgedAt.IsZero() {
			want.State = Offered
		}
	case "VOID":
		want.State = Void
	case "EXPIRE":
		if e.At.Before(prior.ValidUntil) {
			return fmt.Errorf("%w: cannot expire before deadline", ErrInvalid)
		}
		want.State = Expired
	case "WORK_STARTED":
		if e.Actor != prior.Target() || !prior.WorkStartedAt.IsZero() {
			return fmt.Errorf("%w: wrong or repeated workStarted", ErrInvalid)
		}
		if err := prior.Ready(e.At); err != nil {
			return err
		}
		want.WorkStartedAt = e.At
	case "COORDINATOR_LOST":
		if prior.CoordinatorLost || e.Actor == prior.Coordinator() || e.Actor == prior.Target() {
			return fmt.Errorf("%w: repeated or self-interested loss observation", ErrInvalid)
		}
		for _, candidate := range l.Records {
			if candidate.Coordinator() == prior.Coordinator() && !candidate.State.Terminal() && !candidate.CoordinatorLost && candidate.Target() == e.Actor {
				return fmt.Errorf("%w: observer targets another affected assignment", ErrInvalid)
			}
		}
		want.CoordinatorLost = true
	case "HANDOFF":
		actor := prior.Coordinator()
		if prior.CoordinatorLost {
			observer, lossOpen := l.OpenCoordinatorLossObserver(e.AssignmentID)
			if !lossOpen || r.Coordinator() == observer || r.Coordinator() == prior.Target() {
				return fmt.Errorf("%w: loss observer or target cannot recover to itself", ErrInvalid)
			}
			actor = r.Coordinator()
		}
		if e.Actor != actor {
			return fmt.Errorf("%w: wrong handoff actor", ErrInvalid)
		}
		if !e.At.Before(prior.ValidUntil) || len(r.HandoffEvidence) != len(prior.HandoffEvidence)+1 {
			return fmt.Errorf("%w: invalid handoff", ErrInvalid)
		}
		want.HandoffEvidence = append(append([]Handoff{}, prior.HandoffEvidence...), Handoff{prior.Coordinator(), r.Coordinator(), e.EvidenceRef, e.At})
		want.CurrentCoordinator, want.CoordinatorAgentID, want.CoordinatorGeneration = r.CurrentCoordinator, r.CoordinatorAgentID, r.CoordinatorGeneration
		want.CoordinatorLost = false
	case "RESULT":
		if err := prior.Ready(e.At); err != nil {
			return err
		}
		if r.AcceptedResult == nil {
			return fmt.Errorf("%w: missing result", ErrInvalid)
		}
		if err := prior.MatchResult(*r.AcceptedResult); err != nil {
			return err
		}
		want.State, want.AcceptedResult = Completed, r.AcceptedResult
	default:
		return fmt.Errorf("%w: unsupported authority event %s (transport is observation only)", ErrInvalid, e.Kind)
	}
	if !reflect.DeepEqual(want, r) {
		return fmt.Errorf("%w: immutable assignment fields changed", ErrInvalid)
	}
	return nil
}

func Validate(l *Ledger) error {
	if l == nil {
		return nil
	}
	if l.Version != 1 {
		return ErrUnsupportedFormat
	}
	if l.Records == nil || l.Events == nil {
		return fmt.Errorf("%w: ledger collections required", ErrInvalid)
	}
	replay := Empty()
	for _, e := range l.Events {
		if err := replay.Append(e); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(l.Records, replay.Records) {
		return fmt.Errorf("%w: projection differs from append-only history", ErrInvalid)
	}
	return nil
}

func ValidateTransition(before, after *Ledger) error {
	if before != nil {
		if after == nil || before.Version != after.Version {
			return ErrUnsupportedFormat
		}
		if after == nil || len(after.Events) < len(before.Events) || !reflect.DeepEqual(before.Events, after.Events[:len(before.Events)]) {
			return fmt.Errorf("%w: assignment ledger history cannot be removed or rewritten", ErrInvalid)
		}
	}
	return Validate(after)
}
