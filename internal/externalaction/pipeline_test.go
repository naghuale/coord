package externalaction

import (
	"strings"
	"testing"
)

func pipelineFixture() PipelineSelector {
	return PipelineSelector{Repository: "naghuale/coord", Provider: ProviderGitHubActions, CommitSHA: strings.Repeat("a", 40), Ref: "refs/heads/main", Workflow: "checks.yml", Matrix: map[string]string{"go": "1.24", "os": "linux"}}
}

func TestPipelineSelectorRequiresExactSupportedIdentity(t *testing.T) {
	s := pipelineFixture()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*PipelineSelector){
		"provider": func(s *PipelineSelector) { s.Provider = "unknown" },
		"commit":   func(s *PipelineSelector) { s.CommitSHA = "main" },
		"workflow": func(s *PipelineSelector) { s.Workflow = " " },
		"matrix":   func(s *PipelineSelector) { s.Matrix = map[string]string{"bad\nkey": "v"} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := pipelineFixture()
			mutate(&changed)
			if err := changed.Validate(); err == nil {
				t.Fatal("invalid selector accepted")
			}
		})
	}
}

func TestPipelineSelectorDigestIncludesCommitAndSortsMatrix(t *testing.T) {
	a, b := pipelineFixture(), pipelineFixture()
	b.Matrix = map[string]string{"os": "linux", "go": "1.24"}
	if a.Digest() != b.Digest() {
		t.Fatal("map insertion order changed selector digest")
	}
	b.CommitSHA = strings.Repeat("b", 40)
	if a.Digest() == b.Digest() {
		t.Fatal("selector digest ignored exact commit")
	}
}

func TestUnknownPipelineStateIsNotEvaluated(t *testing.T) {
	for raw, want := range map[string]PipelineState{
		"QUEUED": PipelineQueued, "IN_PROGRESS": PipelineInProgress, "SUCCESS": PipelineSuccess,
		"FAILURE": PipelineFailure, "CANCELLED": PipelineCancelled, "SKIPPED": PipelineSkipped,
		"TIMED_OUT": PipelineTimedOut, "ACTION_REQUIRED": PipelineActionRequired,
		"NOT_FOUND": PipelineNotFound, "UNKNOWN": PipelineUnknown, "SURPRISE": PipelineUnknown,
	} {
		got, evaluation := MapPipelineState(raw)
		if got != want {
			t.Fatalf("MapPipelineState(%q)=%q", raw, got)
		}
		if raw == "SURPRISE" && evaluation != PipelineNotEvaluated {
			t.Fatalf("unknown state evaluation=%q", evaluation)
		}
		if raw != "SUCCESS" && raw != "FAILURE" && raw != "CANCELLED" && raw != "TIMED_OUT" && evaluation == PipelinePass {
			t.Fatalf("non-success state %q passed", raw)
		}
	}
}
