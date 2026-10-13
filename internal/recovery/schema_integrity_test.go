package recovery

import (
	"os"
	"strings"
	"testing"
)

// TestSchemaWildcardBanPattern verifies that both recovery schemas enforce the
// wildcard ban on path fields. The JSON schema uses "[*?\\[]" and the YAML
// coordinator schema uses "[*?\\[]". Removing either pattern passes CI silently
// without this test (proven by Auditor audit on be2262b).
func TestSchemaWildcardBanPattern(t *testing.T) {
	schemas := []struct {
		path    string
		pattern string
	}{
		{"../../docs/schemas/recovery-report.schema.json", `[*?`},
		{"../../docs/schemas/recovery-report.coordinator.schema.yaml", `[*?`},
	}
	for _, s := range schemas {
		if _, err := os.Stat(s.path); err != nil {
			t.Skipf("schema not available: %s", s.path)
		}
		data, err := os.ReadFile(s.path)
		if err != nil {
			t.Fatalf("read %s: %v", s.path, err)
		}
		if !strings.Contains(string(data), s.pattern) {
			t.Errorf("wildcard ban pattern %q missing in %s — removing it would allow wildcard paths to pass schema validation silently", s.pattern, s.path)
		}
	}
}

// TestSchemaFullSHAPattern verifies that the recovery report JSON schema
// enforces 40-character hex SHA values. Replacing the fullSha definition with
// {type: string} passes CI silently without this test.
func TestSchemaFullSHAPattern(t *testing.T) {
	const path = "../../docs/schemas/recovery-report.schema.json"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("schema not available: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if !strings.Contains(string(data), "[0-9a-f]{40}") {
		t.Errorf("full-SHA pattern [0-9a-f]{40} missing in %s — removing it would allow short SHAs to pass schema validation silently", path)
	}
}

// TestSchemaCoordinatorFullSHAPattern verifies the coordinator YAML schema also
// enforces full SHAs.
func TestSchemaCoordinatorFullSHAPattern(t *testing.T) {
	const path = "../../docs/schemas/recovery-report.coordinator.schema.yaml"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("schema not available: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if !strings.Contains(string(data), "[0-9a-f]{40}") {
		t.Errorf("full-SHA pattern [0-9a-f]{40} missing in %s", path)
	}
}
