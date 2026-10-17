// Package shadowpilot implements the Shadow Validation pilot model.
// It provides identity, comparison, and evidence types for a single read-only
// agent workflow through the existing coordd. It holds no production authority:
// it does not write to the canonical store, does not acquire the namespace lock,
// and does not send messages via the production Telepathy transport.
//
// Decision: SHADOW-VALIDATION-PILOT-R1 (ACCEPTED 2026-10-17)
package shadowpilot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ShadowRunID is the stable identity of one shadow validation pass.
type ShadowRunID string

func (id ShadowRunID) Validate() error {
	if strings.TrimSpace(string(id)) == "" || len(id) > 128 {
		return errors.New("shadow run ID must be a non-empty string of at most 128 characters")
	}
	return nil
}

// ShadowRun is the immutable identity record of a single shadow validation run.
// It is created once and never mutated; comparisons and evidence are recorded
// in separate types that reference this identity by ShadowRunID.
type ShadowRun struct {
	ID           ShadowRunID `json:"id"`
	AssignmentID string      `json:"assignment_id"`
	AgentID      string      `json:"agent_id"`
	SessionID    string      `json:"session_id"`
	Generation   uint64      `json:"generation"`
	SubjectSHA   string      `json:"subject_sha"`
	InputDigest  string      `json:"input_digest"`
	CreatedAt    time.Time   `json:"created_at"`
}

var sha40 = func(s string) bool { return len(s) == 40 || len(s) == 64 }

func (r ShadowRun) Validate() error {
	if err := r.ID.Validate(); err != nil {
		return fmt.Errorf("shadow run: %w", err)
	}
	if strings.TrimSpace(r.AssignmentID) == "" {
		return errors.New("shadow run: assignment_id required")
	}
	if strings.TrimSpace(r.AgentID) == "" {
		return errors.New("shadow run: agent_id required")
	}
	if strings.TrimSpace(r.SessionID) == "" {
		return errors.New("shadow run: session_id required")
	}
	if r.Generation == 0 {
		return errors.New("shadow run: generation must be positive")
	}
	if !sha40(r.SubjectSHA) {
		return errors.New("shadow run: subject_sha must be a 40 or 64 hex character commit SHA")
	}
	if r.InputDigest == "" {
		return errors.New("shadow run: input_digest required")
	}
	if r.CreatedAt.IsZero() {
		return errors.New("shadow run: created_at required")
	}
	return nil
}

// AgentResult is the typed structured output produced by the executor in the
// shadow scenario. It carries no production write authority; it is a read-only
// artifact that feeds the comparison stage.
type AgentResult struct {
	Kind         string    `json:"kind"`
	AssignmentID string    `json:"assignment_id"`
	AgentID      string    `json:"agent_id"`
	SessionID    string    `json:"session_id"`
	Generation   uint64    `json:"generation"`
	SubjectSHA   string    `json:"subject_sha"`
	InputDigest  string    `json:"input_digest"`
	Findings     []Finding `json:"findings"`
	CompletedAt  time.Time `json:"completed_at"`
}

const AgentResultKind = "AgentResult"

func (r AgentResult) Validate() error {
	if r.Kind != AgentResultKind {
		return fmt.Errorf("agent result: kind must be %q, got %q", AgentResultKind, r.Kind)
	}
	if strings.TrimSpace(r.AssignmentID) == "" {
		return errors.New("agent result: assignment_id required")
	}
	if strings.TrimSpace(r.AgentID) == "" {
		return errors.New("agent result: agent_id required")
	}
	if strings.TrimSpace(r.SessionID) == "" {
		return errors.New("agent result: session_id required")
	}
	if r.Generation == 0 {
		return errors.New("agent result: generation must be positive")
	}
	if !sha40(r.SubjectSHA) {
		return errors.New("agent result: subject_sha must be a 40 or 64 hex character commit SHA")
	}
	if r.InputDigest == "" {
		return errors.New("agent result: input_digest required")
	}
	if r.CompletedAt.IsZero() {
		return errors.New("agent result: completed_at required")
	}
	for i, f := range r.Findings {
		if err := f.Validate(); err != nil {
			return fmt.Errorf("agent result: finding[%d]: %w", i, err)
		}
	}
	return nil
}

// AgentResultDigest produces a stable hex digest of an AgentResult for
// comparison. The Findings slice is sorted by ID before hashing to ensure
// deterministic output regardless of insertion order.
func AgentResultDigest(r AgentResult) (string, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Finding is a single named observation from an agent result.
type Finding struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
}

func (f Finding) Validate() error {
	if strings.TrimSpace(f.ID) == "" {
		return errors.New("finding: id required")
	}
	if strings.TrimSpace(f.Summary) == "" {
		return errors.New("finding: summary required")
	}
	return nil
}

// BaselineResult is the control result against which the shadow run is compared.
// It is computed once from a stable committed fixture and is immutable.
type BaselineResult struct {
	SubjectSHA  string    `json:"subject_sha"`
	InputDigest string    `json:"input_digest"`
	Findings    []Finding `json:"findings"`
	RecordedAt  time.Time `json:"recorded_at"`
	Digest      string    `json:"digest"`
}

// NewBaselineResult constructs a BaselineResult and sets its digest.
func NewBaselineResult(subjectSHA, inputDigest string, findings []Finding, at time.Time) (BaselineResult, error) {
	b := BaselineResult{SubjectSHA: subjectSHA, InputDigest: inputDigest, Findings: findings, RecordedAt: at}
	if err := b.validate(); err != nil {
		return BaselineResult{}, err
	}
	d, err := baselineDigest(b)
	if err != nil {
		return BaselineResult{}, err
	}
	b.Digest = d
	return b, nil
}

func (b BaselineResult) validate() error {
	if !sha40(b.SubjectSHA) {
		return errors.New("baseline: subject_sha must be a 40 or 64 hex character commit SHA")
	}
	if b.InputDigest == "" {
		return errors.New("baseline: input_digest required")
	}
	if b.RecordedAt.IsZero() {
		return errors.New("baseline: recorded_at required")
	}
	return nil
}

func baselineDigest(b BaselineResult) (string, error) {
	value := struct {
		SubjectSHA  string
		InputDigest string
		Findings    []Finding
	}{b.SubjectSHA, b.InputDigest, b.Findings}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
