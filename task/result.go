package task

import (
	"fmt"
	"strings"
	"time"
)

type ReviewVerdict string

const (
	ReviewApproved        ReviewVerdict = "APPROVED"
	ReviewChangesRequired ReviewVerdict = "CHANGES_REQUIRED"
)

type AuditVerdict string

const (
	AuditPass            AuditVerdict = "PASS"
	AuditChangesRequired AuditVerdict = "CHANGES_REQUIRED"
	AuditBlocked         AuditVerdict = "AUDIT_BLOCKED"
)

type FindingSeverity string

const (
	SeverityCritical FindingSeverity = "CRITICAL"
	SeverityMajor    FindingSeverity = "MAJOR"
	SeverityMinor    FindingSeverity = "MINOR"
)

type FindingStatus string

const (
	FindingOpen      FindingStatus = "OPEN"
	FindingResolved  FindingStatus = "RESOLVED"
	FindingDismissed FindingStatus = "DISMISSED"
)

type DecisionClass string

const (
	DecisionClassFD   DecisionClass = "FD"
	DecisionClassTD   DecisionClass = "TD"
	DecisionClassTDR2 DecisionClass = "TD-R2"
)

// SubjectBinding identifies the exact version under review or audit.
// CommitSHA must be the full 40-character lowercase hex object name.
// TreeHash is the git tree object hash for the reviewed tree.
// PatchDigest is optional and identifies a specific patch within the tree.
type SubjectBinding struct {
	RepositoryID string `json:"repository_id"`
	CommitSHA    string `json:"commit_sha"`
	TreeHash     string `json:"tree_hash"`
	PatchDigest  string `json:"patch_digest,omitempty"`
}

func (s SubjectBinding) IsZero() bool {
	return s.RepositoryID == "" && s.CommitSHA == "" && s.TreeHash == ""
}

func (s SubjectBinding) Validate() error {
	if s.RepositoryID == "" {
		return fmt.Errorf("subject binding requires repository_id")
	}
	if len(s.CommitSHA) != 40 {
		return fmt.Errorf("subject binding requires full 40-character commit SHA, got %d characters", len(s.CommitSHA))
	}
	if s.TreeHash == "" {
		return fmt.Errorf("subject binding requires tree_hash")
	}
	return nil
}

// AuditedProperty records one named property checked by an Auditor.
type AuditedProperty struct {
	Name    string `json:"name"`
	Verdict string `json:"verdict"`
	Note    string `json:"note,omitempty"`
}

// Reference identifies a canonical object associated with a Task. Artifact
// references are reserved until canonical Artifact metadata is defined.
type Reference struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

const (
	ReferenceTask        = "TASK"
	ReferenceRun         = "RUN"
	ReferenceDomainEvent = "DOMAIN_EVENT"
	ReferenceArtifact    = "ARTIFACT"
)

type Finding struct {
	ID        string          `json:"id"`
	Severity  FindingSeverity `json:"severity"`
	Summary   string          `json:"summary"`
	Evidence  []Reference     `json:"evidence"`
	CreatedAt time.Time       `json:"created_at"`
	// Canonical fields set by Coord after the agent submits the finding.
	Class    DecisionClass `json:"class,omitempty"`
	Property string        `json:"property,omitempty"`
	Path     string        `json:"path,omitempty"`
	Line     int           `json:"line,omitempty"`
	Status   FindingStatus `json:"status,omitempty"`
}

type ReviewResult struct {
	Verdict            ReviewVerdict `json:"verdict"`
	Findings           []Finding     `json:"findings"`
	Summary            string        `json:"summary"`
	InputArtifactRefs  []string      `json:"input_artifact_refs"`
	OutputArtifactRefs []string      `json:"output_artifact_refs"`
	CreatedAt          time.Time     `json:"created_at"`
	// Canonical fields assigned by Coord — callers must leave these empty.
	ResultID     string          `json:"result_id,omitempty"`
	Subject      *SubjectBinding `json:"subject,omitempty"`
	AssignmentID string          `json:"assignment_id,omitempty"`
	ReviewerID   string          `json:"reviewer_id,omitempty"`
	Round        uint32          `json:"round,omitempty"`
}

type AuditResult struct {
	Verdict            AuditVerdict `json:"verdict"`
	Findings           []Finding    `json:"findings"`
	Summary            string       `json:"summary"`
	InputArtifactRefs  []string     `json:"input_artifact_refs"`
	OutputArtifactRefs []string     `json:"output_artifact_refs"`
	CreatedAt          time.Time    `json:"created_at"`
	// Canonical fields assigned by Coord — callers must leave these empty.
	ResultID     string            `json:"result_id,omitempty"`
	Subject      *SubjectBinding   `json:"subject,omitempty"`
	AssignmentID string            `json:"assignment_id,omitempty"`
	AuditorID    string            `json:"auditor_id,omitempty"`
	Round        uint32            `json:"round,omitempty"`
	Properties   []AuditedProperty `json:"properties,omitempty"`
}

func (r ReviewResult) Validate() error {
	if r.Verdict != ReviewApproved && r.Verdict != ReviewChangesRequired {
		return fmt.Errorf("unsupported Review verdict %q", r.Verdict)
	}
	if r.Subject != nil {
		if err := r.Subject.Validate(); err != nil {
			return fmt.Errorf("ReviewResult subject: %w", err)
		}
	}
	return validateResult(r.Summary, r.Findings, r.InputArtifactRefs, r.OutputArtifactRefs, r.CreatedAt)
}

func (r AuditResult) Validate() error {
	switch r.Verdict {
	case AuditPass, AuditChangesRequired, AuditBlocked:
	default:
		return fmt.Errorf("unsupported Audit verdict %q", r.Verdict)
	}
	if r.Subject != nil {
		if err := r.Subject.Validate(); err != nil {
			return fmt.Errorf("AuditResult subject: %w", err)
		}
	}
	for i, p := range r.Properties {
		if strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.Verdict) == "" {
			return fmt.Errorf("audited property %d requires name and verdict", i)
		}
	}
	return validateResult(r.Summary, r.Findings, r.InputArtifactRefs, r.OutputArtifactRefs, r.CreatedAt)
}

type ExecutorOutcome string

const (
	ExecutorSuccess ExecutorOutcome = "SUCCESS"
	ExecutorFailure ExecutorOutcome = "FAILURE"
	ExecutorPartial ExecutorOutcome = "PARTIAL"
)

type ExecutorResult struct {
	Outcome            ExecutorOutcome `json:"outcome"`
	Summary            string          `json:"summary"`
	OutputArtifactRefs []string        `json:"output_artifact_refs"`
	CreatedAt          time.Time       `json:"created_at"`
}

func (r ExecutorResult) Validate() error {
	switch r.Outcome {
	case ExecutorSuccess, ExecutorFailure, ExecutorPartial:
	default:
		return fmt.Errorf("unsupported Executor outcome %q", r.Outcome)
	}
	if strings.TrimSpace(r.Summary) == "" || r.CreatedAt.IsZero() {
		return fmt.Errorf("summary and creation time are required")
	}
	if r.OutputArtifactRefs == nil {
		return fmt.Errorf("output_artifact_refs must be present")
	}
	if len(r.OutputArtifactRefs) > 0 {
		return fmt.Errorf("artifact metadata not defined")
	}
	return nil
}

func validateResult(summary string, findings []Finding, inputs, outputs []string, createdAt time.Time) error {
	if strings.TrimSpace(summary) == "" || createdAt.IsZero() {
		return fmt.Errorf("summary and creation time are required")
	}
	if findings == nil {
		return fmt.Errorf("findings must be present (an empty list is allowed)")
	}
	for i, finding := range findings {
		if strings.TrimSpace(finding.ID) == "" || strings.TrimSpace(finding.Summary) == "" || finding.CreatedAt.IsZero() {
			return fmt.Errorf("finding %d requires ID, summary and creation time", i)
		}
		switch finding.Severity {
		case SeverityCritical, SeverityMajor, SeverityMinor:
		default:
			return fmt.Errorf("finding %q has unsupported severity %q", finding.ID, finding.Severity)
		}
		if len(finding.Evidence) == 0 {
			return fmt.Errorf("finding %q requires evidence references", finding.ID)
		}
		if finding.Status != "" {
			switch finding.Status {
			case FindingOpen, FindingResolved, FindingDismissed:
			default:
				return fmt.Errorf("finding %q has unsupported status %q", finding.ID, finding.Status)
			}
		}
		if finding.Class != "" {
			switch finding.Class {
			case DecisionClassFD, DecisionClassTD, DecisionClassTDR2:
			default:
				return fmt.Errorf("finding %q has unsupported class %q", finding.ID, finding.Class)
			}
		}
	}
	if inputs == nil || outputs == nil {
		return fmt.Errorf("input_artifact_refs and output_artifact_refs must be present")
	}
	if len(inputs) > 0 || len(outputs) > 0 {
		return fmt.Errorf("artifact metadata not defined")
	}
	return nil
}
