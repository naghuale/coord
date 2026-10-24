package checkpoint

import (
	"coord/store"
	"encoding/json"
	"strings"
	"testing"
)

func TestAssignmentSnapshotRejectsDuplicateAuthorityFields(t *testing.T) {
	raw, err := json.Marshal(store.EmptyCanonicalState())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCanonicalSnapshot(raw); err != nil {
		t.Fatal(err)
	}
	duplicate := strings.TrimSuffix(string(raw), "}") + `,"assignment_ledger":null,"assignment_ledger":null}`
	if _, err := DecodeCanonicalSnapshot([]byte(duplicate)); err == nil || !strings.Contains(err.Error(), "duplicate canonical JSON field") {
		t.Fatalf("duplicate authority could hide ledger: %v", err)
	}
}
