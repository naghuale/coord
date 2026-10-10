package baseline

import "testing"

var testID = Identity{Profile: "p1", AdapterID: "a1", AdapterVersion: "1.0", AdapterDigest: "d1", SuiteDigest: "s1", SchemaVersion: "v1", MetricVersion: "v1"}

func makeRun(hv, ne int, metrics map[string]float64) Run {
	return Run{Identity: testID, Digest: "r1", Metrics: metrics, HardViolations: hv, NotEvaluated: ne}
}

func TestPromoteNotReady(t *testing.T) {
	runs := []Run{makeRun(0, 0, map[string]float64{"accuracy": 0.9})}
	_, err := Promote(testID, runs, 3)
	if err == nil || err.Error() != "BASELINE_NOT_READY" {
		t.Fatalf("expected BASELINE_NOT_READY, got %v", err)
	}
}

func TestPromoteMedian(t *testing.T) {
	runs := []Run{
		makeRun(0, 0, map[string]float64{"accuracy": 0.8}),
		makeRun(0, 0, map[string]float64{"accuracy": 0.9}),
		makeRun(0, 0, map[string]float64{"accuracy": 1.0}),
	}
	c, err := Promote(testID, runs, 3)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if c.Metrics["accuracy"] != 0.9 {
		t.Fatalf("expected median 0.9, got %v", c.Metrics["accuracy"])
	}
	if c.ReviewStatus != "REQUIRED" {
		t.Fatalf("expected REQUIRED, got %s", c.ReviewStatus)
	}
}

func TestPromoteFiltersHardViolations(t *testing.T) {
	runs := []Run{
		makeRun(1, 0, map[string]float64{"accuracy": 0.99}),
		makeRun(0, 0, map[string]float64{"accuracy": 0.8}),
		makeRun(0, 0, map[string]float64{"accuracy": 0.85}),
	}
	_, err := Promote(testID, runs, 3)
	if err == nil {
		t.Fatal("expected BASELINE_NOT_READY (only 2 clean runs), got nil")
	}
}
