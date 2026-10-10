package scenarioloader

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"coord/internal/evaluation"
	"coord/internal/governanceio"
)

// ScenarioFile is the strict schema for a JSON scenario suite.
type ScenarioFile struct {
	APIVersion string                `json:"apiVersion"`
	Kind       string                `json:"kind"`
	Scenarios  []evaluation.Scenario `json:"scenarios"`
}

// Load reads and strictly validates a scenario suite from r.
func Load(r io.Reader) ([]evaluation.Scenario, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	sf, err := governanceio.DecodeStrict[ScenarioFile](bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if sf.Kind != "ScenarioSuite" {
		return nil, fmt.Errorf("SCENARIO_KIND_INVALID: got %q", sf.Kind)
	}
	if sf.APIVersion == "" {
		return nil, fmt.Errorf("SCENARIO_API_VERSION_MISSING")
	}
	var errs []string
	for i, scenario := range sf.Scenarios {
		if scenario.ID == "" {
			errs = append(errs, fmt.Sprintf("scenario[%d]: ID empty", i))
		}
		if scenario.Event == "" {
			errs = append(errs, fmt.Sprintf("scenario[%d]: Event empty", i))
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("SCENARIO_SCHEMA_INVALID: %s", strings.Join(errs, "; "))
	}
	return sf.Scenarios, nil
}
