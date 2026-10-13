package actionmap

import "gopkg.in/yaml.v3"

// ActionMap is the strict YAML model for docs/operations/ACTION-MAP.yaml.
type ActionMap struct {
	APIVersion          string                        `yaml:"apiVersion"`
	Kind                string                        `yaml:"kind"`
	Metadata            yaml.Node                     `yaml:"metadata"`
	Events              map[string]EventEntry         `yaml:"events"`
	RecoveryTransitions map[string]RecoveryTransition `yaml:"recoveryTransitions"`
}

// EventEntry is a single event specification.
// Fields match the four keys present in the committed ACTION-MAP.yaml.
type EventEntry struct {
	RequiredDocuments []string `yaml:"requiredDocuments"`
	AllowedActions    []string `yaml:"allowedActions"`
	ForbiddenActions  []string `yaml:"forbiddenActions"`
	ExpectedResults   []string `yaml:"expectedResults"`
}

// RecoveryTransition is a single recovery transition specification.
type RecoveryTransition struct {
	When             string   `yaml:"when"`
	AllowedActions   []string `yaml:"allowedActions"`
	ForbiddenActions []string `yaml:"forbiddenActions"`
	ResultsIn        []string `yaml:"resultsIn"`
	// ReasonCodes is an optional constraint list; when present its entries
	// must be valid for the transition type.
	ReasonCodes []string `yaml:"reasonCodes"`
}

var requiredRecoveryEvents = []string{
	"SESSION_INTERRUPTED", "SESSION_RESUMED", "SESSION_REPLACED",
	"CONTEXT_LOST", "WORKTREE_STATE_UNKNOWN",
	"REQUIRED_SOURCE_MISSING", "SUBJECT_DRIFT_DETECTED",
}

var requiredRecoveryTransitions = []string{
	"RESUME_ASSIGNMENT", "FREEZE_FOR_REVIEW", "RECOVER_COORDINATION_STATE",
	"PREPARE_CORRECTION", "BLOCKED_EXTERNAL",
	"HUMAN_ACTION_REQUIRED", "OWNER_REQUIRED", "PRESERVE_AND_STOP",
}

// knownExpectedResults is the closed set of valid expectedResults values.
var knownExpectedResults = map[string]bool{
	"CONTINUE_AUTONOMOUSLY": true, "BLOCKED": true, "COMPLETE": true,
	"CONTEXT_READY": true, "CHANGES_REQUIRED": true,
	"COMMITTED": true, "PUBLISHED": true, "CLOSED": true,
	"ESCALATED": true, "ACCEPTED": true, "INVALID_ADMISSION": true,
	"OWNER_NOTIFICATION": true, "QUESTION_SUPPRESSED": true,
	"READY_FOR_COMMIT": true, "READY_FOR_REVIEW": true,
	"NOT_EVALUATED": true, "RECOVER_SESSION": true,
	// recovery transitions (valid as expectedResults)
	"RESUME_ASSIGNMENT": true, "FREEZE_FOR_REVIEW": true,
	"RECOVER_COORDINATION_STATE": true, "PREPARE_CORRECTION": true,
	"BLOCKED_EXTERNAL": true, "HUMAN_ACTION_REQUIRED": true,
	"OWNER_REQUIRED": true, "PRESERVE_AND_STOP": true,
}

// ownerRequiredReasonCodes is the closed set valid for OWNER_REQUIRED.
var ownerRequiredReasonCodes = map[string]bool{
	"NEW_NORM_REQUIRED": true, "ACCEPTED_NORM_CONFLICT": true,
	"RESERVED_OWNER_DECISION": true, "TRUST_BOUNDARY_CHANGE": true,
	"DATA_BOUNDARY_CHANGE": true, "PROVIDER_BOUNDARY_CHANGE": true,
	"FAIL_CLOSED_WEAKENING": true, "RISK_ACCEPTANCE_REQUIRED": true,
}

// humanActionRequiredReasonCodes is the closed set valid for HUMAN_ACTION_REQUIRED.
var humanActionRequiredReasonCodes = map[string]bool{
	"MFA_REQUIRED": true, "CREDENTIAL_INPUT_REQUIRED": true,
	"PROTECTED_RUNNER_REQUIRED": true, "SYSTEM_DIALOG_REQUIRED": true,
	"PHYSICAL_FRONTEND_LAUNCH_REQUIRED": true, "INFRASTRUCTURE_ACCESS_REQUIRED": true,
}
