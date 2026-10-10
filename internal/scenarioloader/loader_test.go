package scenarioloader

import (
	"strings"
	"testing"
)

const validSuite = `{
  "apiVersion": "coord.dev/v1alpha1",
  "kind": "ScenarioSuite",
  "scenarios": [
    {"id": "s1", "event": "TASK_STARTED", "requiredDocuments": ["ADR-0001"]}
  ]
}`

func TestLoadValid(t *testing.T) {
	scenarios, err := Load(strings.NewReader(validSuite))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(scenarios) != 1 || scenarios[0].ID != "s1" {
		t.Fatalf("unexpected scenarios: %v", scenarios)
	}
}

func TestLoadWrongKind(t *testing.T) {
	bad := `{"apiVersion":"v1","kind":"NotASuite","scenarios":[]}`
	_, err := Load(strings.NewReader(bad))
	if err == nil || !strings.Contains(err.Error(), "SCENARIO_KIND_INVALID") {
		t.Fatalf("expected SCENARIO_KIND_INVALID, got %v", err)
	}
}

func TestLoadMissingAPIVersion(t *testing.T) {
	bad := `{"kind":"ScenarioSuite","scenarios":[]}`
	_, err := Load(strings.NewReader(bad))
	if err == nil || !strings.Contains(err.Error(), "SCENARIO_API_VERSION_MISSING") {
		t.Fatalf("expected SCENARIO_API_VERSION_MISSING, got %v", err)
	}
}

func TestLoadEmptyScenarioID(t *testing.T) {
	bad := `{"apiVersion":"v1","kind":"ScenarioSuite","scenarios":[{"id":"","event":"E1"}]}`
	_, err := Load(strings.NewReader(bad))
	if err == nil || !strings.Contains(err.Error(), "SCENARIO_SCHEMA_INVALID") {
		t.Fatalf("expected SCENARIO_SCHEMA_INVALID, got %v", err)
	}
}
