// Package externalaction defines durable operations owned by the canonical store.
// It has no IO and grants no process or mutation authority.
package externalaction

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"time"
)

type Family string

const (
	AgentSession    Family = "AGENT_SESSION"
	GitHub          Family = "GITHUB"
	ContractVersion uint32 = 3
)

type Kind string
type Status string

const (
	Deliver     Kind   = "DELIVER_ASSIGNMENT"
	Observe     Kind   = "OBSERVE_ASSIGNMENT"
	Fence       Kind   = "FENCE_SESSION"
	Stop        Kind   = "VERIFY_STOP_PROOF"
	Replace     Kind   = "CREATE_REPLACEMENT_ASSIGNMENT"
	Pending     Status = "PENDING"
	Claimed     Status = "CLAIMED"
	Dispatching Status = "DISPATCHING"
	Dispatched  Status = "DISPATCHED"
	Ambiguous   Status = "AMBIGUOUS"
	Observed    Status = "OBSERVED"
	Completed   Status = "COMPLETED"
	Failed      Status = "FAILED"
	Blocked     Status = "BLOCKED"
	Fenced      Status = "FENCED"
)

type Binding struct {
	ProjectID    string `json:"project_id"`
	TaskID       string `json:"task_id"`
	RunID        string `json:"run_id"`
	AssignmentID string `json:"assignment_id"`
	SessionID    string `json:"session_id"`
	SubjectSHA   string `json:"subject_sha"`
	Generation   uint64 `json:"generation"`
	Fence        uint64 `json:"fence"`
}
type Claim struct {
	DispatcherID string    `json:"dispatcher_id"`
	Epoch        uint64    `json:"epoch"`
	Deadline     time.Time `json:"deadline"`
}
type Action struct {
	Family              Family          `json:"family,omitempty"`
	SchemaVersion       uint32          `json:"schema_version,omitempty"`
	ID                  string          `json:"id"`
	OperationInstanceID string          `json:"operation_instance_id"`
	Key                 string          `json:"idempotency_key"`
	Kind                Kind            `json:"kind"`
	Binding             Binding         `json:"binding"`
	ParentID            string          `json:"parent_id,omitempty"`
	Request             json.RawMessage `json:"request"`
	RequestDigest       string          `json:"request_digest"`
	Status              Status          `json:"status"`
	Claim               Claim           `json:"claim"`
	Attempts            uint32          `json:"attempts"`
	MaxAttempts         uint32          `json:"max_attempts"`
	NextAttemptAt       time.Time       `json:"next_attempt_at"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
	Blocker             string          `json:"blocker,omitempty"`
	Result              *Result         `json:"result,omitempty"`
}

// Result is returned only by the configured trusted adapter. The request/action
// identity and claim epoch are supplied by the runtime, never by an LLM.
type Result struct {
	GitHub     *GitHubObservation `json:"github,omitempty"`
	Outcome    string             `json:"outcome"`
	Evidence   string             `json:"evidence"`
	StopMethod string             `json:"stop_method,omitempty"`
	Stopped    bool               `json:"stopped,omitempty"`
}
type Observation struct {
	ID            string    `json:"id"`
	ActionID      string    `json:"action_id"`
	Source        string    `json:"source"`
	ClaimEpoch    uint64    `json:"claim_epoch"`
	RequestDigest string    `json:"request_digest"`
	Result        Result    `json:"result"`
	Digest        string    `json:"digest"`
	At            time.Time `json:"at"`
	Stale         bool      `json:"stale"`
}
type Receipt struct {
	ObservationID string    `json:"observation_id"`
	ActionID      string    `json:"action_id"`
	Digest        string    `json:"digest"`
	Revision      uint64    `json:"revision"`
	AckPending    bool      `json:"ack_pending"`
	AckAttempts   uint32    `json:"ack_attempts"`
	NextAckAt     time.Time `json:"next_ack_at"`
	AckBlocker    string    `json:"ack_blocker,omitempty"`
}
type Event struct {
	ActionID string    `json:"action_id"`
	Revision uint64    `json:"revision"`
	Digest   string    `json:"digest"`
	At       time.Time `json:"at"`
}
type State struct {
	Actions      map[string]Action      `json:"actions"`
	Observations map[string]Observation `json:"observations"`
	Receipts     map[string]Receipt     `json:"receipts"`
	Events       []Event                `json:"events"`
}

func Empty() State {
	return State{map[string]Action{}, map[string]Observation{}, map[string]Receipt{}, []Event{}}
}
func Digest(v any) string {
	// Session event digests deliberately retain the schema-2 preimage.
	if a, ok := v.(Action); ok && a.Family == AgentSession {
		a.Family = ""
		a.SchemaVersion = 0
		v = a
	}
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func Key(a Action) string {
	if a.Family == GitHub {
		var payload GitHubActionPayload
		if err := decodeRequest(a.Request, &payload); err != nil {
			return ""
		}
		return Digest(struct {
			Version           uint32
			Family            Family
			Kind              Kind
			Instance, Request string
		}{ContractVersion, a.Family, a.Kind, a.OperationInstanceID, GitHubPayloadDigest(payload)})
	}
	return Digest(struct {
		Version  int
		Kind     Kind
		Instance string
		Binding  Binding
		Request  string
	}{1, a.Kind, a.OperationInstanceID, a.Binding, a.RequestDigest})
}
func New(id string, kind Kind, binding Binding, parent string, request json.RawMessage, at time.Time) Action {
	a := Action{Family: AgentSession, SchemaVersion: ContractVersion, ID: id, OperationInstanceID: id, Kind: kind, Binding: binding, ParentID: parent, Request: request, RequestDigest: Digest(request), Status: Pending, MaxAttempts: 3, CreatedAt: at, UpdatedAt: at, NextAttemptAt: at}
	a.Key = Key(a)
	return a
}
func (a Action) Terminal() bool {
	return a.Status == Completed || a.Status == Failed || a.Status == Blocked || a.Status == Fenced
}
func (a Action) Validate() error {
	if a.SchemaVersion != ContractVersion {
		return fmt.Errorf("unsupported action schema version")
	}
	if a.Family != AgentSession && a.Family != GitHub {
		return fmt.Errorf("unknown action family")
	}
	if a.ID == "" || a.OperationInstanceID == "" || a.Key != Key(a) || !json.Valid(a.Request) || a.RequestDigest != Digest(a.Request) || a.MaxAttempts == 0 || a.MaxAttempts > 16 || a.Attempts > a.MaxAttempts || a.CreatedAt.IsZero() || a.UpdatedAt.Before(a.CreatedAt) {
		return fmt.Errorf("invalid external action %q identity/request/policy", a.ID)
	}
	if a.Family == GitHub {
		return a.validateGitHub()
	}
	if a.Binding.ProjectID == "" || a.Binding.TaskID == "" || a.Binding.RunID == "" || a.Binding.AssignmentID == "" || a.Binding.SessionID == "" || len(a.Binding.SubjectSHA) != 40 || a.Binding.Generation == 0 {
		return fmt.Errorf("incomplete action binding %q", a.ID)
	}
	var request struct {
		Assignment      string `json:"assignment,omitempty"`
		PreviousSession string `json:"previousSession,omitempty"`
		Generation      uint64 `json:"generation,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(a.Request))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("unsupported action request: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing action request")
	}
	if a.Kind == Replace {
		if a.Binding.Generation == ^uint64(0) || request.Generation != a.Binding.Generation+1 || request.PreviousSession != a.Binding.SessionID || request.Assignment != "" {
			return fmt.Errorf("invalid replacement request")
		}
	} else if request.Assignment != a.Binding.AssignmentID || request.PreviousSession != "" || request.Generation != 0 {
		return fmt.Errorf("invalid assignment request")
	}
	switch a.Kind {
	case Deliver, Observe, Fence, Stop, Replace:
	default:
		return fmt.Errorf("unknown action kind")
	}
	switch a.Status {
	case Pending, Claimed, Dispatching, Dispatched, Ambiguous, Observed, Completed, Failed, Blocked, Fenced:
	default:
		return fmt.Errorf("unknown action status")
	}
	if a.Result != nil && a.Result.GitHub != nil {
		return fmt.Errorf("GitHub result in session action")
	}
	if a.Status == Completed && a.Result == nil {
		return fmt.Errorf("completed action without result")
	}
	if a.Status == Blocked && a.Blocker == "" {
		return fmt.Errorf("blocked action without blocker")
	}
	return nil
}
func Validate(s State, revision uint64) error {
	if s.Actions == nil || s.Observations == nil || s.Receipts == nil || s.Events == nil {
		return fmt.Errorf("missing orchestration collections")
	}
	keys := map[string]string{}
	instances := map[string]string{}
	latest := map[string]Event{}
	for _, e := range s.Events {
		if e.Revision == 0 || e.Revision > revision || e.At.IsZero() {
			return fmt.Errorf("invalid action event revision")
		}
		latest[e.ActionID] = e
	}
	for id, a := range s.Actions {
		if id != a.ID {
			return fmt.Errorf("action map identity mismatch")
		}
		if err := a.Validate(); err != nil {
			return err
		}
		if prior := keys[a.Key]; prior != "" && prior != id {
			return fmt.Errorf("duplicate action key")
		}
		keys[a.Key] = id
		if prior := instances[a.OperationInstanceID]; prior != "" && prior != id {
			return fmt.Errorf("duplicate operation instance")
		}
		instances[a.OperationInstanceID] = id
		if latest[id].Digest != Digest(a) {
			return fmt.Errorf("action %q differs from event history", id)
		}
		if a.Family == GitHub {
			if a.ParentID != "" {
				return fmt.Errorf("GitHub parent unsupported")
			}
			continue
		}
		if a.ParentID != "" {
			p, ok := s.Actions[a.ParentID]
			if !ok || p.Binding != a.Binding || p.Status != Completed {
				return fmt.Errorf("invalid parent action")
			}
			switch a.Kind {
			case Observe:
				if p.Kind != Deliver && (p.Kind != Observe || p.Result == nil || p.Result.Outcome != "RUNNING") {
					return fmt.Errorf("invalid observation predecessor")
				}
			case Fence:
				if p.Kind != Observe || p.Result == nil || p.Result.Outcome != "UNAVAILABLE" {
					return fmt.Errorf("fence requires unavailable observation")
				}
			case Stop:
				if p.Kind != Fence || p.Result == nil || p.Result.Outcome != "FENCED" {
					return fmt.Errorf("stop proof requires confirmed logical fence")
				}
			case Replace:
				if p.Kind != Stop || p.Result == nil || !p.Result.Stopped || p.Result.StopMethod != "SUPERVISOR_CONFIRMED_EXIT" {
					return fmt.Errorf("replacement requires accepted stop proof")
				}
			default:
				return fmt.Errorf("unexpected action predecessor")
			}

		}
	}
	for _, a := range s.Actions {
		if a.Family == AgentSession && a.Kind != Deliver && a.ParentID == "" {
			return fmt.Errorf("action missing predecessor")
		}
		if a.Status == Observed {
			found := false
			for _, o := range s.Observations {
				if o.ActionID == a.ID && !o.Stale && o.ClaimEpoch == a.Claim.Epoch && a.Result != nil && o.Digest == Digest(*a.Result) {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("observed action missing evidence")
			}
		}
		if a.Status == Completed {
			found := false
			for _, r := range s.Receipts {
				if r.ActionID == a.ID {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("completed action missing receipt intent")
			}
		}
	}
	for id, o := range s.Observations {
		if id != o.ID || o.Source == "" || o.At.IsZero() || o.Digest != Digest(o.Result) {
			return fmt.Errorf("invalid observation")
		}
		if _, ok := s.Actions[o.ActionID]; !ok {
			return fmt.Errorf("orphan observation")
		}
	}
	for id, r := range s.Receipts {
		o, ok := s.Observations[id]
		a := s.Actions[r.ActionID]
		if a.Family == GitHub && (a.Result == nil || o.ClaimEpoch != a.Claim.Epoch || o.RequestDigest != a.RequestDigest || o.Digest != Digest(*a.Result)) {
			return fmt.Errorf("completed GitHub action lacks bound positive observation")
		}
		if !ok || o.Stale || r.ObservationID != id || r.ActionID != o.ActionID || r.Digest != o.Digest || r.Revision == 0 || r.Revision > revision || a.Status != Completed {
			return fmt.Errorf("invalid action receipt")
		}
	}
	return nil
}
func ValidateTransition(prev, next State, revision uint64) error {
	if len(next.Events) < len(prev.Events) || !reflect.DeepEqual(prev.Events, next.Events[:len(prev.Events)]) {
		return fmt.Errorf("action history not append-only")
	}
	events := map[string]Event{}
	for _, e := range next.Events[len(prev.Events):] {
		if e.Revision != revision {
			return fmt.Errorf("action event outside commit")
		}
		events[e.ActionID] = e
	}
	for id, a := range next.Actions {
		p, ok := prev.Actions[id]
		if !ok && a.Status != Pending {
			return fmt.Errorf("new action must begin PENDING")
		}
		if ok && reflect.DeepEqual(p, a) {
			continue
		}
		if events[id].Digest != Digest(a) {
			return fmt.Errorf("action change without same-commit event")
		}
		if ok {
			immutableP, immutableA := p, a
			immutableP.Status = immutableA.Status
			immutableP.Claim = immutableA.Claim
			immutableP.Attempts = immutableA.Attempts
			immutableP.NextAttemptAt = immutableA.NextAttemptAt
			immutableP.UpdatedAt = immutableA.UpdatedAt
			immutableP.Blocker = immutableA.Blocker
			immutableP.Result = immutableA.Result
			if !reflect.DeepEqual(immutableP, immutableA) || a.Claim.Epoch < p.Claim.Epoch || a.Attempts < p.Attempts {
				return fmt.Errorf("action immutable identity changed")
			}
			if p.Terminal() {
				return fmt.Errorf("terminal action changed")
			}
			allowed := map[Status]map[Status]bool{
				Pending:     {Claimed: true},
				Claimed:     {Claimed: true, Dispatching: true, Ambiguous: true},
				Dispatching: {Ambiguous: true, Observed: true, Blocked: true, Fenced: true},
				Dispatched:  {Ambiguous: true},
				Ambiguous:   {Ambiguous: true, Observed: true, Blocked: true, Fenced: true},
				Observed:    {Completed: true, Fenced: true},
			}
			if !allowed[p.Status][a.Status] {
				return fmt.Errorf("illegal action transition %s -> %s", p.Status, a.Status)
			}

		}
	}
	for id := range prev.Actions {
		if _, ok := next.Actions[id]; !ok {
			return fmt.Errorf("action deleted")
		}
	}
	for id, o := range prev.Observations {
		if n, ok := next.Observations[id]; !ok || !reflect.DeepEqual(n, o) {
			return fmt.Errorf("observation changed")
		}
	}
	for id, r := range prev.Receipts {
		n, ok := next.Receipts[id]
		if !ok {
			return fmt.Errorf("receipt deleted")
		}
		if n.AckAttempts < r.AckAttempts || n.AckAttempts > 3 {
			return fmt.Errorf("invalid acknowledgement retry count")
		}
		r.AckPending = n.AckPending
		r.AckAttempts = n.AckAttempts
		r.NextAckAt = n.NextAckAt
		r.AckBlocker = n.AckBlocker
		if !reflect.DeepEqual(r, n) || (!prev.Receipts[id].AckPending && n.AckPending) {
			return fmt.Errorf("receipt changed")
		}
	}
	return nil
}
