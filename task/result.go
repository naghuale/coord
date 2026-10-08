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
)

type FindingSeverity string

const (
	SeverityCritical FindingSeverity = "CRITICAL"
	SeverityMajor    FindingSeverity = "MAJOR"
	SeverityMinor    FindingSeverity = "MINOR"
)

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
}

type ReviewResult struct {
	Verdict            ReviewVerdict `json:"verdict"`
	Findings           []Finding     `json:"findings"`
	Summary            string        `json:"summary"`
	InputArtifactRefs  []string      `json:"input_artifact_refs"`
	OutputArtifactRefs []string      `json:"output_artifact_refs"`
	CreatedAt          time.Time     `json:"created_at"`
}

type AuditResult struct {
	Verdict            AuditVerdict `json:"verdict"`
	Findings           []Finding    `json:"findings"`
	Summary            string       `json:"summary"`
	InputArtifactRefs  []string     `json:"input_artifact_refs"`
	OutputArtifactRefs []string     `json:"output_artifact_refs"`
	CreatedAt          time.Time    `json:"created_at"`
}

func (r ReviewResult) Validate() error {
	if r.Verdict != ReviewApproved && r.Verdict != ReviewChangesRequired {
		return fmt.Errorf("unsupported Review verdict %q", r.Verdict)
	}
	return validateResult(r.Summary, r.Findings, r.InputArtifactRefs, r.OutputArtifactRefs, r.CreatedAt)
}

func (r AuditResult) Validate() error {
	if r.Verdict != AuditPass && r.Verdict != AuditChangesRequired {
		return fmt.Errorf("unsupported Audit verdict %q", r.Verdict)
	}
	return validateResult(r.Summary, r.Findings, r.InputArtifactRefs, r.OutputArtifactRefs, r.CreatedAt)
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
	}
	if inputs == nil || outputs == nil {
		return fmt.Errorf("input_artifact_refs and output_artifact_refs must be present")
	}
	if len(inputs) > 0 || len(outputs) > 0 {
		return fmt.Errorf("artifact metadata not defined")
	}
	return nil
}
