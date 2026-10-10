package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"coord/internal/baseline"
	"coord/internal/baselinestore"
)

func runBaselineStoreRun(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("baseline store-run", flag.ContinueOnError)
	storeRoot := fs.String("store", "", "path to baseline store root (required)")
	adapterID := fs.String("adapter-id", "", "adapter identity (required)")
	suiteDigest := fs.String("suite-digest", "", "suite digest (required)")
	runDigest := fs.String("run-digest", "", "run digest (required)")
	hardViolations := fs.String("hard-violations", "0", "hard violation count")
	notEvaluated := fs.String("not-evaluated", "0", "not-evaluated count")
	metricsValue := fs.String("metrics", "{}", "JSON object of metric name to float64")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *storeRoot == "" || *adapterID == "" || *suiteDigest == "" || *runDigest == "" {
		return fmt.Errorf("--store, --adapter-id, --suite-digest and --run-digest are required")
	}
	hard, err := strconv.Atoi(*hardViolations)
	if err != nil || hard < 0 {
		return fmt.Errorf("--hard-violations must be a non-negative integer")
	}
	notEval, err := strconv.Atoi(*notEvaluated)
	if err != nil || notEval < 0 {
		return fmt.Errorf("--not-evaluated must be a non-negative integer")
	}
	var metrics map[string]float64
	if err := json.Unmarshal([]byte(*metricsValue), &metrics); err != nil || metrics == nil {
		return fmt.Errorf("--metrics must be a JSON object of finite numbers")
	}
	id := baseline.Identity{AdapterID: *adapterID, SuiteDigest: *suiteDigest}
	run := baseline.Run{Identity: id, Digest: *runDigest, Metrics: metrics, HardViolations: hard, NotEvaluated: notEval}
	store := &baselinestore.Store{Root: *storeRoot}
	if err := store.StoreRun(context.Background(), run); err != nil {
		return err
	}
	_, _ = fmt.Printf("stored run %s\n", *runDigest)
	return nil
}

func runBaselineCompare(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("baseline compare", flag.ContinueOnError)
	storeRoot := fs.String("store", "", "path to baseline store root (required)")
	adapterID := fs.String("adapter-id", "", "adapter identity (required)")
	suiteDigest := fs.String("suite-digest", "", "suite digest (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *storeRoot == "" || *adapterID == "" || *suiteDigest == "" {
		return fmt.Errorf("--store, --adapter-id and --suite-digest are required")
	}
	id := baseline.Identity{AdapterID: *adapterID, SuiteDigest: *suiteDigest}
	store := &baselinestore.Store{Root: *storeRoot}
	candidate, err := store.LoadCandidate(context.Background(), id)
	if err != nil {
		return fmt.Errorf("load candidate: %w", err)
	}
	runs, err := store.ListRuns(context.Background(), id)
	if err != nil {
		return fmt.Errorf("list runs: %w", err)
	}
	_, _ = fmt.Printf("candidate sources=%d review=%s\nstored runs=%d\n", len(candidate.Sources), candidate.ReviewStatus, len(runs))
	metricNames := make([]string, 0, len(candidate.Metrics))
	for name := range candidate.Metrics {
		metricNames = append(metricNames, name)
	}
	sort.Strings(metricNames)
	var lines []string
	for _, name := range metricNames {
		lines = append(lines, fmt.Sprintf("  %s=%.4f", name, candidate.Metrics[name]))
	}
	_, _ = fmt.Fprintf(os.Stdout, "metrics:\n%s\n", strings.Join(lines, "\n"))
	return nil
}

func runBaselinePromote(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("baseline promote", flag.ContinueOnError)
	storeRoot := fs.String("store", "", "path to baseline store root (required)")
	adapterID := fs.String("adapter-id", "", "adapter identity (required)")
	suiteDigest := fs.String("suite-digest", "", "suite digest (required)")
	minRuns := fs.Int("min-runs", 5, "minimum clean runs required for promotion")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *storeRoot == "" || *adapterID == "" || *suiteDigest == "" {
		return fmt.Errorf("--store, --adapter-id and --suite-digest are required")
	}
	if *minRuns < 1 {
		return fmt.Errorf("--min-runs must be positive")
	}
	id := baseline.Identity{AdapterID: *adapterID, SuiteDigest: *suiteDigest}
	store := &baselinestore.Store{Root: *storeRoot}
	runs, err := store.ListRuns(context.Background(), id)
	if err != nil {
		return fmt.Errorf("list runs: %w", err)
	}
	candidate, err := baseline.Promote(id, runs, *minRuns)
	if err != nil {
		return err
	}
	if err := store.StoreCandidate(context.Background(), candidate); err != nil {
		return fmt.Errorf("store candidate: %w", err)
	}
	_, _ = fmt.Printf("promoted candidate sources=%d review=%s\n", len(candidate.Sources), candidate.ReviewStatus)
	return nil
}
