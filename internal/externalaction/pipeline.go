package externalaction

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type PipelineState string
type PipelineEvaluation string

const (
	PipelineQueued         PipelineState = "QUEUED"
	PipelineInProgress     PipelineState = "IN_PROGRESS"
	PipelineSuccess        PipelineState = "SUCCESS"
	PipelineFailure        PipelineState = "FAILURE"
	PipelineCancelled      PipelineState = "CANCELLED"
	PipelineSkipped        PipelineState = "SKIPPED"
	PipelineTimedOut       PipelineState = "TIMED_OUT"
	PipelineActionRequired PipelineState = "ACTION_REQUIRED"
	PipelineNotFound       PipelineState = "NOT_FOUND"
	PipelineUnknown        PipelineState = "UNKNOWN"

	PipelinePass         PipelineEvaluation = "PASS"
	PipelineFail         PipelineEvaluation = "FAIL"
	PipelineNotEvaluated PipelineEvaluation = "NOT_EVALUATED"

	ProviderGitHubActions = "GITHUB_ACTIONS"
)

type PipelineSelector struct {
	Repository string            `json:"repository"`
	Provider   string            `json:"provider"`
	CommitSHA  string            `json:"commit_sha"`
	Ref        string            `json:"ref,omitempty"`
	Workflow   string            `json:"workflow"`
	Matrix     map[string]string `json:"matrix,omitempty"`
}

type PipelineObservation struct {
	Selector   PipelineSelector   `json:"selector"`
	State      PipelineState      `json:"state"`
	Evaluation PipelineEvaluation `json:"evaluation"`
}

var pipelineRepoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var pipelineSHApattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (s PipelineSelector) Validate() error {
	if !pipelineRepoPattern.MatchString(s.Repository) || s.Provider != ProviderGitHubActions || !pipelineSHApattern.MatchString(s.CommitSHA) || strings.TrimSpace(s.Workflow) == "" || len(s.Workflow) > 256 {
		return fmt.Errorf("invalid or unsupported pipeline selector")
	}
	if len(s.Ref) > 512 || strings.ContainsAny(s.Ref, "\x00\r\n") {
		return fmt.Errorf("invalid pipeline ref filter")
	}
	if len(s.Matrix) > 32 {
		return fmt.Errorf("pipeline matrix filter exceeds limit")
	}
	for key, value := range s.Matrix {
		if key == "" || len(key) > 128 || value == "" || len(value) > 256 || strings.ContainsAny(key+value, "\x00\r\n") {
			return fmt.Errorf("invalid pipeline matrix filter")
		}
	}
	return nil
}

func (s PipelineSelector) Digest() string {
	keys := make([]string, 0, len(s.Matrix))
	for key := range s.Matrix {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ordered := make([][2]string, 0, len(keys))
	for _, key := range keys {
		ordered = append(ordered, [2]string{key, s.Matrix[key]})
	}
	return Digest(struct {
		Version                                        uint32
		Repository, Provider, CommitSHA, Ref, Workflow string
		Matrix                                         [][2]string
	}{1, s.Repository, s.Provider, s.CommitSHA, s.Ref, s.Workflow, ordered})
}

func MapPipelineState(raw string) (PipelineState, PipelineEvaluation) {
	state := PipelineState(raw)
	switch state {
	case PipelineQueued, PipelineInProgress, PipelineSuccess, PipelineFailure, PipelineCancelled, PipelineSkipped, PipelineTimedOut, PipelineActionRequired, PipelineNotFound, PipelineUnknown:
	default:
		state = PipelineUnknown
	}
	switch state {
	case PipelineSuccess:
		return state, PipelinePass
	case PipelineFailure, PipelineCancelled, PipelineTimedOut:
		return state, PipelineFail
	default:
		return state, PipelineNotEvaluated
	}
}
