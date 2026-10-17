package actionmap

import "time"

// Workflow types refine ActionMap; none of these identifiers grant authority.
type FactID string
type Operator string
type ObservationKind string
type WorkflowOutcomeCode string
type WorkflowAction string

const (
	ObservationAgentResponse     ObservationKind     = "AGENT_RESPONSE_OBSERVED"
	OutcomeStaleResponseRejected WorkflowOutcomeCode = "STALE_RESPONSE_REJECTED"
)

// Value is a closed scalar/collection union. YAML objects and expressions are
// rejected by UnmarshalYAML. Missing values remain distinct from false/zero.
type Value struct {
	Kind    string
	Boolean bool
	Text    string
	Number  uint64
	Items   []string
}

func BoolValue(v bool) Value     { return Value{Kind: "boolean", Boolean: v} }
func TextValue(v string) Value   { return Value{Kind: "string", Text: v} }
func NumberValue(v uint64) Value { return Value{Kind: "integer", Number: v} }
func ListValue(v ...string) Value {
	return Value{Kind: "collection", Items: append([]string(nil), v...)}
}

type Condition struct {
	Fact      FactID   `yaml:"fact"`
	Operator  Operator `yaml:"operator"`
	Value     *Value   `yaml:"value,omitempty"`
	ValueFrom FactID   `yaml:"valueFrom,omitempty"`
}

type Conditions struct {
	All []Condition `yaml:"all,omitempty"`
	Any []Condition `yaml:"any,omitempty"`
}

type WorkflowMetadata struct {
	Status             string `yaml:"status"`
	NormativeAuthority *bool  `yaml:"normativeAuthority"`
	CanonicalActionMap string `yaml:"canonicalActionMap"`
}

type ObservationDefinition struct {
	Kind           ObservationKind `yaml:"kind"`
	Scope          string          `yaml:"scope"`
	CanonicalEvent *bool           `yaml:"canonicalEvent"`
}

type OutcomeDefinition struct {
	Kind                WorkflowOutcomeCode `yaml:"kind"`
	Scope               string              `yaml:"scope"`
	CanonicalTransition *bool               `yaml:"canonicalTransition"`
}

type WorkflowIdentifiers struct {
	Observations []ObservationDefinition `yaml:"observations"`
	Outcomes     []OutcomeDefinition     `yaml:"outcomes"`
}

type OutcomeDescription struct {
	Code                   WorkflowOutcomeCode `yaml:"code"`
	CurrentAssignmentState string              `yaml:"currentAssignmentState,omitempty"`
	CurrentSessionState    string              `yaml:"currentSessionState,omitempty"`
}

type Workflow struct {
	ID                  string             `yaml:"id"`
	CanonicalEvent      string             `yaml:"canonicalEvent"`
	CanonicalTransition string             `yaml:"canonicalTransition"`
	Observation         ObservationKind    `yaml:"observation"`
	Priority            int                `yaml:"priority"`
	When                Conditions         `yaml:"when"`
	Actions             []WorkflowAction   `yaml:"actions"`
	Outcome             OutcomeDescription `yaml:"outcome"`
	Forbidden           []WorkflowAction   `yaml:"forbidden"`
}

type WorkflowCatalog struct {
	APIVersion  string              `yaml:"apiVersion"`
	Kind        string              `yaml:"kind"`
	Metadata    WorkflowMetadata    `yaml:"metadata"`
	Identifiers WorkflowIdentifiers `yaml:"identifiers"`
	Workflows   []Workflow          `yaml:"workflows"`
}

type CompletionGuard struct {
	ID        string     `yaml:"id"`
	When      Conditions `yaml:"when"`
	Require   Conditions `yaml:"require"`
	Violation struct {
		ReasonCode string `yaml:"reasonCode"`
	} `yaml:"violation"`
}

type CompletionPolicy struct {
	APIVersion  string            `yaml:"apiVersion"`
	Kind        string            `yaml:"kind"`
	Metadata    WorkflowMetadata  `yaml:"metadata"`
	ReasonCodes []string          `yaml:"reasonCodes"`
	Guards      []CompletionGuard `yaml:"guards"`
}

type Facts map[FactID]Value

// These are trusted, validated Inbox projections, not message body identity.
// This slice does not implement their production ingestion or authentication.
type AgentResponseObservation struct {
	ObservationID string
	AssignmentID  string
	SessionID     string
	Generation    uint64
	SubjectSHA    string
	ResultType    string
	ResultDigest  string
	ObservedAt    time.Time
}

type CurrentAssignment struct {
	ID         string
	SessionID  string
	Generation uint64
	SubjectSHA string
	State      string
}

type CurrentSession struct {
	ID         string
	Generation uint64
	State      string
}

type WorkflowOutcome struct {
	WorkflowID    string
	Code          WorkflowOutcomeCode
	ObservationID string
	AssignmentID  string
	SessionID     string
	Generation    uint64
	ReasonCodes   []string
}

type StaleResponseExecution struct {
	Outcome    WorkflowOutcome
	Assignment CurrentAssignment
	Session    CurrentSession
	Actions    []WorkflowAction
}

type Availability string

const (
	AvailabilityAvailable   Availability = "AVAILABLE"
	AvailabilityUnavailable Availability = "UNAVAILABLE"
	AvailabilityUnknown     Availability = "UNKNOWN"
)

// BoundaryObservation must come from the trusted canonical/adapter projection.
// A proposed result supplies only its ID; it cannot supply these facts itself.
type BoundaryObservation struct {
	RequestDigest                           string
	ID, Source, Digest                      string
	ActionID, AssignmentID, SubjectSHA      string
	ObservedAt, ValidUntil                  time.Time
	ReasonCode                              string
	EvidenceRefs                            []string
	NormRef, NormDigest, NormStatus         string
	DecisionAlreadyAccepted                 bool
	OwnerBoundaryAbsent                     bool
	Automation, Replacement, InternalRemedy Availability
	PhysicalActionRequired                  bool
	ExternalUnavailable                     bool
}

type CompletionContext struct {
	Now          time.Time
	Observations map[string]BoundaryObservation
	Evidence     map[string]BoundaryEvidence
	Facts        Facts
}

// BoundaryEvidence is a resolved reference in a trusted projection, not a new
// canonical Evidence aggregate. Accepted-norm provenance must be independently
// resolved by that projection; a reason label or result text cannot supply it.
type BoundaryEvidence struct {
	ID, Source, Digest                 string
	ActionID, AssignmentID, SubjectSHA string
	NormRef, NormStatus                string
}

type HumanActionCard struct {
	Command, WorkingDirectory, ExpectedResult string
	PostChecks                                []string
	FailureTransition, Continuation           string
}

type CompletionDecision struct {
	OwnerRequired, HumanActionRequired, BlockedExternal             bool
	ObservationID, PendingActionID, PendingAssignmentID, SubjectSHA string
	ReasonCode                                                      string
	HumanAction                                                     HumanActionCard
}
