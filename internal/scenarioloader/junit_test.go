package scenarioloader

import (
	"bytes"
	"strings"
	"testing"

	"coord/internal/evaluation"
)

func TestWriteJUnitAllPass(t *testing.T) {
	results := []evaluation.Result{
		{Pass: true},
		{Pass: true},
	}
	var buf bytes.Buffer
	if err := WriteJUnit(&buf, "suite1", results); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, `failures="0"`) {
		t.Fatalf("expected failures=0 in output: %s", out)
	}
	if strings.Contains(out, "<failure") {
		t.Fatalf("unexpected failure element in: %s", out)
	}
}

func TestWriteJUnitWithFailures(t *testing.T) {
	results := []evaluation.Result{
		{Pass: false, HardViolations: 2},
		{Pass: true},
	}
	var buf bytes.Buffer
	if err := WriteJUnit(&buf, "suite1", results); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, `failures="1"`) {
		t.Fatalf("expected failures=1: %s", out)
	}
	if !strings.Contains(out, "hard=2") {
		t.Fatalf("expected hard=2 in failure message: %s", out)
	}
}
