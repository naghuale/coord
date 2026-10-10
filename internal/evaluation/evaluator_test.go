package evaluation

import "testing"

var baseScenario = Scenario{
	ID: "s1", Event: "SESSION_STARTED",
	RequiredDocuments:  []string{"agents", "current"},
	ForbiddenDocuments: []string{"secret-doc"},
	ForbiddenActions:   []string{"load_all"},
	ExpectedResults:    []string{"CONTINUE_AUTONOMOUSLY", "BLOCKED"},
}

func TestEvaluatePass(t *testing.T) {
	r := AgentResult{
		SelectedEvent: "SESSION_STARTED",
		Documents:     []string{"agents", "current"},
		Actions:       []string{"verify_head"},
		Result:        "CONTINUE_AUTONOMOUSLY",
		SubjectSHA:    "abc",
		Evidence:      []Evidence{{Class: "VERIFIED", SubjectSHA: "abc", GateEligible: true}},
	}
	got := Evaluate(baseScenario, r)
	if !got.Pass || got.HardViolations != 0 {
		t.Fatalf("expected Pass=true HardViolations=0, got %+v", got)
	}
}

func TestEvaluateMissingRequiredDocument(t *testing.T) {
	r := AgentResult{
		SelectedEvent: "SESSION_STARTED",
		Documents:     []string{"current"},
		Actions:       []string{"verify_head"},
		Result:        "CONTINUE_AUTONOMOUSLY",
		SubjectSHA:    "abc",
	}
	got := Evaluate(baseScenario, r)
	if got.Pass || got.HardViolations == 0 {
		t.Fatalf("expected fail for missing required doc, got %+v", got)
	}
}

func TestEvaluateForbiddenDocument(t *testing.T) {
	r := AgentResult{
		SelectedEvent: "SESSION_STARTED",
		Documents:     []string{"agents", "current", "secret-doc"},
		Actions:       []string{"verify_head"},
		Result:        "CONTINUE_AUTONOMOUSLY",
		SubjectSHA:    "abc",
	}
	got := Evaluate(baseScenario, r)
	if got.Pass || got.HardViolations == 0 {
		t.Fatalf("expected fail for forbidden doc, got %+v", got)
	}
}

func TestEvaluateWrongEvent(t *testing.T) {
	r := AgentResult{SelectedEvent: "WRONG_EVENT", Documents: []string{"agents", "current"}, Result: "CONTINUE_AUTONOMOUSLY", SubjectSHA: "abc"}
	got := Evaluate(baseScenario, r)
	if got.Pass || got.HardViolations == 0 {
		t.Fatalf("expected fail for wrong event, got %+v", got)
	}
}
