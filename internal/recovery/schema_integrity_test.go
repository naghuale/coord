package recovery

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

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
// enforces full SHA-40 at each of the 7 required fields by navigating the
// parsed schema document to each site and checking the pattern field directly.
//
// Required sites:
//
//	/properties/recovery/properties/head
//	/properties/recovery/properties/expectedHead/anyOf/0
//	/properties/assignment/properties/baseSha/anyOf/0
//	/properties/subjectBinding/properties/reviewSubjectSha/anyOf/0
//	/properties/subjectBinding/properties/auditSubjectSha/anyOf/0
//	/properties/subjectBinding/properties/ciSubjectSha/anyOf/0
//	/properties/coordinationRecovery/properties/committedSubjects/items/properties/sha
func TestSchemaFullSHAPattern(t *testing.T) {
	const schemaPath = "../../docs/schemas/recovery-report.schema.json"
	const fullSHAPattern = "^[0-9a-f]{40}$"

	if _, err := os.Stat(schemaPath); err != nil {
		t.Skipf("schema not available: %s", schemaPath)
	}
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}

	sites := []string{
		"/properties/recovery/properties/head",
		"/properties/recovery/properties/expectedHead/anyOf/0",
		"/properties/assignment/properties/baseSha/anyOf/0",
		"/properties/subjectBinding/properties/reviewSubjectSha/anyOf/0",
		"/properties/subjectBinding/properties/auditSubjectSha/anyOf/0",
		"/properties/subjectBinding/properties/ciSubjectSha/anyOf/0",
		"/properties/coordinationRecovery/properties/committedSubjects/items/properties/sha",
	}

	for _, site := range sites {
		node, err := jsonNavigate(schema, site)
		if err != nil {
			t.Errorf("site %s: navigate failed: %v", site, err)
			continue
		}
		obj, ok := node.(map[string]any)
		if !ok {
			t.Errorf("site %s: node is %T, expected object", site, node)
			continue
		}
		// Resolve $ref if present.
		obj, err = jsonResolveRef(schema, obj)
		if err != nil {
			t.Errorf("site %s: resolve $ref: %v", site, err)
			continue
		}
		got, _ := obj["pattern"].(string)
		if got != fullSHAPattern {
			t.Errorf("site %s: pattern = %q, want %q — weakening this field allows short SHAs to pass schema validation silently", site, got, fullSHAPattern)
		}
	}
}

// TestSchemaCoordinatorFullSHAPattern verifies the coordinator YAML schema
// enforces full SHA-40 structurally:
//  1. The $defs/fullSha definition carries pattern == "^[0-9a-f]{40}$".
//  2. Each SHA site that $refs fullSha still points at #/$defs/fullSha
//     and resolves to the correct pattern.
//
// This catches both weakening $defs/fullSha (with or without decoys) and
// repointing a site to a weaker definition.
func TestSchemaCoordinatorFullSHAPattern(t *testing.T) {
	const schemaPath = "../../docs/schemas/recovery-report.coordinator.schema.yaml"
	const fullSHAPattern = "^[0-9a-f]{40}$"
	const fullSHARef = "#/$defs/fullSha"

	if _, err := os.Stat(schemaPath); err != nil {
		t.Skipf("schema not available: %s", schemaPath)
	}
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var schema map[string]any
	if err := yaml.Unmarshal(data, &schema); err != nil {
		t.Fatalf("parse YAML schema: %v", err)
	}

	// 1. Assert the $defs/fullSha definition has the required pattern.
	defNode, err := jsonNavigate(schema, "/$defs/fullSha")
	if err != nil {
		t.Fatalf("$defs/fullSha not found: %v", err)
	}
	defObj, ok := defNode.(map[string]any)
	if !ok {
		t.Fatalf("$defs/fullSha is %T, expected object", defNode)
	}
	if got, _ := defObj["pattern"].(string); got != fullSHAPattern {
		t.Errorf("$defs/fullSha pattern = %q, want %q", got, fullSHAPattern)
	}

	// 2. Each SHA site must $ref #/$defs/fullSha and resolve to fullSHAPattern.
	sites := []string{
		"/properties/recovery/properties/head",
		"/properties/recovery/properties/expectedHead/anyOf/0",
		"/properties/assignment/properties/baseSha/anyOf/0",
		"/properties/coordinationRecovery/properties/committedSubjects/items/properties/sha",
	}
	for _, site := range sites {
		node, err := jsonNavigate(schema, site)
		if err != nil {
			t.Errorf("site %s: navigate failed: %v", site, err)
			continue
		}
		obj, ok := node.(map[string]any)
		if !ok {
			t.Errorf("site %s: node is %T, expected object", site, node)
			continue
		}
		// Assert site still points at the fullSha definition.
		ref, _ := obj["$ref"].(string)
		if ref != fullSHARef {
			t.Errorf("site %s: $ref = %q, want %q — site may have been repointed to a weaker definition", site, ref, fullSHARef)
		}
		// Resolve and assert the pattern on the definition.
		resolved, err := jsonResolveRef(schema, obj)
		if err != nil {
			t.Errorf("site %s: resolve $ref: %v", site, err)
			continue
		}
		got, _ := resolved["pattern"].(string)
		if got != fullSHAPattern {
			t.Errorf("site %s: resolved pattern = %q, want %q", site, got, fullSHAPattern)
		}
	}
}

// jsonNavigate walks a parsed JSON document following a JSON Pointer path
// (/key/key/index). Returns the node at that path or an error.
func jsonNavigate(root any, path string) (any, error) {
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	cur := root
	for _, seg := range segments {
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[seg]
			if !ok {
				return nil, fmt.Errorf("key %q not found", seg)
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil {
				return nil, fmt.Errorf("expected array index, got %q", seg)
			}
			if idx < 0 || idx >= len(v) {
				return nil, fmt.Errorf("index %d out of range [0,%d)", idx, len(v))
			}
			cur = v[idx]
		default:
			return nil, fmt.Errorf("cannot descend into %T with segment %q", cur, seg)
		}
	}
	return cur, nil
}

// jsonResolveRef resolves a $ref pointer of the form "#/path/to/def" within
// the root schema document. Returns the node itself when no $ref is present.
func jsonResolveRef(root map[string]any, node map[string]any) (map[string]any, error) {
	ref, ok := node["$ref"].(string)
	if !ok {
		return node, nil
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("unsupported $ref: %q", ref)
	}
	resolved, err := jsonNavigate(root, strings.TrimPrefix(ref, "#"))
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %v", ref, err)
	}
	m, ok := resolved.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("$ref %q target is not an object", ref)
	}
	return m, nil
}
