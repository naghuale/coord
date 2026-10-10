package baselinestore

import (
	"context"
	"errors"
	"testing"

	"coord/internal/baseline"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Root: t.TempDir()}
}

var testID = baseline.Identity{
	AdapterID:   "test-adapter",
	SuiteDigest: "abc123",
}

func TestStoreAndListRuns(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	run := baseline.Run{
		Identity:       testID,
		Digest:         "digest-001",
		Metrics:        map[string]float64{"score": 0.9},
		HardViolations: 0,
		NotEvaluated:   0,
	}
	if err := store.StoreRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	runs, err := store.ListRuns(ctx, testID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Digest != "digest-001" {
		t.Fatalf("unexpected runs: %v", runs)
	}
}

func TestStoreRunRejectsDuplicateDigest(t *testing.T) {
	store := newTestStore(t)
	run := baseline.Run{Identity: testID, Digest: "digest-001"}
	if err := store.StoreRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreRun(context.Background(), run); err == nil {
		t.Fatal("expected duplicate digest to be rejected")
	}
}

func TestListRunsEmpty(t *testing.T) {
	store := newTestStore(t)
	runs, err := store.ListRuns(context.Background(), testID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected empty, got %v", runs)
	}
}

func TestStoreAndLoadCandidate(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	candidate := baseline.Candidate{
		Identity:     testID,
		Sources:      []string{"digest-001"},
		Metrics:      map[string]float64{"score": 0.9},
		ReviewStatus: "REQUIRED",
	}
	if err := store.StoreCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadCandidate(ctx, testID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ReviewStatus != "REQUIRED" || len(loaded.Sources) != 1 {
		t.Fatalf("unexpected candidate: %v", loaded)
	}
}

func TestLoadCandidateNotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.LoadCandidate(context.Background(), testID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
