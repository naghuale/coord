package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"coord/internal/evaluation"
	"coord/internal/scenarioloader"
)

func runRunScenarios(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("run-scenarios", flag.ContinueOnError)
	suitePath := fs.String("suite", "", "path to ScenarioSuite JSON file")
	junitPath := fs.String("junit-output", "", "path to write JUnit XML output (optional)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *suitePath == "" {
		return fmt.Errorf("--suite is required")
	}

	file, err := os.Open(*suitePath)
	if err != nil {
		return fmt.Errorf("open suite: %w", err)
	}
	defer func() { _ = file.Close() }()

	scenarios, err := scenarioloader.Load(file)
	if err != nil {
		return err
	}

	// This command validates the suite and emits a deterministic empty-result
	// report until an AgentResult source is connected to the CLI.
	results := make([]evaluation.Result, 0, len(scenarios))
	scenarioIDs := make([]string, 0, len(scenarios))
	for _, scenario := range scenarios {
		result := evaluation.Evaluate(scenario, evaluation.AgentResult{})
		results = append(results, result)
		scenarioIDs = append(scenarioIDs, scenario.ID)
	}

	pass, fail := 0, 0
	for _, result := range results {
		if result.Pass {
			pass++
		} else {
			fail++
		}
	}
	fmt.Printf("scenarios=%d pass=%d fail=%d\n", len(results), pass, fail)

	if *junitPath != "" {
		junitFile, err := os.Create(*junitPath)
		if err != nil {
			return fmt.Errorf("create junit: %w", err)
		}
		writeErr := scenarioloader.WriteJUnitWithScenarioIDs(junitFile, *suitePath, scenarioIDs, results)
		closeErr := junitFile.Close()
		if writeErr != nil {
			return fmt.Errorf("write junit: %w", writeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close junit: %w", closeErr)
		}
		_, _ = fmt.Fprintf(os.Stderr, "JUnit written: %s\n", *junitPath)
	}
	return nil
}
