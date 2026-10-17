package orchestration

import (
	"bytes"
	"coord/checkpoint"
	"coord/internal/externalaction"
	"coord/store"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Downgrade only the fixture representation to the real schema-2 shape; do not
// duplicate production conversion code. Existing event hashes remain unchanged.
func schema2Fixture(t *testing.T) (checkpoint.CanonicalFile, store.CanonicalState, []byte) {
	t.Helper()
	s, file, a, now := fixture(t)
	resolved, err := filepath.EvalSymlinks(file.Path)
	must(t, err)
	file.Path = resolved
	must(t, s.EnqueueAction(a))
	r := New(s, fake(), "dispatcher", func() time.Time { return *now })
	tick(t, r)
	tick(t, r)
	original := snapshot(t, s)
	must(t, s.Close())
	raw, err := json.Marshal(original)
	must(t, err)
	var document map[string]any
	must(t, json.Unmarshal(raw, &document))
	document["schema_version"] = 2
	for _, value := range document["orchestration"].(map[string]any)["actions"].(map[string]any) {
		action := value.(map[string]any)
		delete(action, "family")
		delete(action, "schema_version")
	}
	raw, err = json.Marshal(document)
	must(t, err)
	must(t, os.WriteFile(file.Path, raw, 0600))
	old, err := file.Load()
	must(t, err)
	if old.SchemaVersion != 2 {
		t.Fatal("read-only Load migrated state")
	}
	still, err := os.ReadFile(file.Path)
	must(t, err)
	if !bytes.Equal(raw, still) {
		t.Fatal("read-only Load wrote snapshot")
	}
	return file, original, raw
}
func TestExistingSchema2SessionActionMigratesToAgentSession(t *testing.T) {
	file, old, _ := schema2Fixture(t)
	s, err := store.NewCanonical(file)
	must(t, err)
	defer func() { _ = s.Close() }()
	migrated := snapshot(t, s)
	if migrated.SchemaVersion != 3 || len(migrated.SchemaMigrations) != 1 {
		t.Fatal("missing schema3 migration event")
	}
	for id, a := range migrated.Orchestration.Actions {
		if a.Family != externalaction.AgentSession || a.SchemaVersion != 3 {
			t.Fatal("missing action discriminator")
		}
		before := old.Orchestration.Actions[id]
		if a.Claim != before.Claim || a.Status != before.Status || a.Binding != before.Binding || a.Attempts != before.Attempts || !reflect.DeepEqual(a.Result, before.Result) {
			t.Fatal("session semantics changed")
		}
	}
	if !reflect.DeepEqual(old.Events, migrated.Events) || !reflect.DeepEqual(old.Orchestration.Events, migrated.Orchestration.Events) || !reflect.DeepEqual(old.Orchestration.Receipts, migrated.Orchestration.Receipts) {
		t.Fatal("history or receipts rewritten")
	}
}
func TestMigrationPreservesIdempotencyKey(t *testing.T) {
	file, old, _ := schema2Fixture(t)
	s, err := store.NewCanonical(file)
	must(t, err)
	defer func() { _ = s.Close() }()
	for id, a := range snapshot(t, s).Orchestration.Actions {
		b := old.Orchestration.Actions[id]
		if a.Key != b.Key || a.ID != b.ID || a.OperationInstanceID != b.OperationInstanceID {
			t.Fatal("migration changed operation identity")
		}
	}
}
func TestMigrationPreservesDigests(t *testing.T) {
	file, old, _ := schema2Fixture(t)
	s, err := store.NewCanonical(file)
	must(t, err)
	defer func() { _ = s.Close() }()
	for id, a := range snapshot(t, s).Orchestration.Actions {
		b := old.Orchestration.Actions[id]
		if a.RequestDigest != b.RequestDigest || externalaction.Digest(a) != externalaction.Digest(b) || !bytes.Equal(a.Request, b.Request) {
			t.Fatal("migration changed digest preimage")
		}
	}
}
func TestMigrationReplayIsIdempotent(t *testing.T) {
	file, _, _ := schema2Fixture(t)
	for i := 0; i < 2; i++ {
		s, err := store.NewCanonical(file)
		must(t, err)
		must(t, s.Close())
	}
	first, err := os.ReadFile(file.Path)
	must(t, err)
	s, err := store.NewCanonical(file)
	must(t, err)
	must(t, s.Close())
	second, err := os.ReadFile(file.Path)
	must(t, err)
	if !bytes.Equal(first, second) {
		t.Fatal("migration replay rewrote snapshot")
	}
}
func TestPartialMigrationDoesNotBecomeCurrent(t *testing.T) {
	file, _, raw := schema2Fixture(t)
	var doc map[string]any
	must(t, json.Unmarshal(raw, &doc))
	for _, v := range doc["orchestration"].(map[string]any)["actions"].(map[string]any) {
		v.(map[string]any)["family"] = "AGENT_SESSION"
		v.(map[string]any)["schema_version"] = 3
		break
	}
	partial, err := json.Marshal(doc)
	must(t, err)
	must(t, os.WriteFile(file.Path, partial, 0600))
	if s, err := store.NewCanonical(file); err == nil {
		_ = s.Close()
		t.Fatal("partial migration became current")
	}
	after, err := os.ReadFile(file.Path)
	must(t, err)
	if !bytes.Equal(partial, after) {
		t.Fatal("rejected partial migration modified source")
	}
}
func TestUnsafeDowngradeRejectedWithSchema3Records(t *testing.T) {
	s, file, _, now := fixture(t)
	a, err := externalaction.NewGitHub("github-1", externalaction.GitHubActionPayload{Operation: externalaction.CreateIssue, ProjectID: "coord", OriginAssignmentID: "assignment", ScopeDigest: strings.Repeat("a", 64), Target: externalaction.GitHubTarget{Repository: "naghuale/coord", ExpectedState: "OPEN"}, Request: externalaction.GitHubRequest{Title: "Tracking"}}, *now)
	must(t, err)
	must(t, s.UpdateCanonical(func(state *store.CanonicalState) error {
		state.Orchestration.Actions[a.ID] = a
		state.Orchestration.Events = append(state.Orchestration.Events, externalaction.Event{ActionID: a.ID, Revision: state.Revision + 1, Digest: externalaction.Digest(a), At: *now})
		return nil
	}))
	// Canonical persistence must reject merge without an authoritative gate,
	// even when the typed payload itself is structurally well-formed.
	merge, err := externalaction.NewGitHub("merge-1", externalaction.GitHubActionPayload{Operation: externalaction.MergePullRequest, ProjectID: "coord", OriginAssignmentID: "assignment", ScopeDigest: strings.Repeat("a", 64), Target: externalaction.GitHubTarget{Repository: "naghuale/coord", PullRequestNumber: 1, ExpectedHead: sha, ExpectedState: "MERGED"}, Request: externalaction.GitHubRequest{MergeMethod: "SQUASH"}, MergeGateRef: "gate:missing"}, *now)
	must(t, err)
	err = s.UpdateCanonical(func(state *store.CanonicalState) error {
		state.Orchestration.Actions[merge.ID] = merge
		state.Orchestration.Events = append(state.Orchestration.Events, externalaction.Event{ActionID: merge.ID, Revision: state.Revision + 1, Digest: externalaction.Digest(merge), At: *now})
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "MERGE_GATE_NOT_FOUND") {
		t.Fatalf("canonical merge without gate: %v", err)
	}
	state := snapshot(t, s)
	if _, exists := state.Orchestration.Actions[merge.ID]; exists {
		t.Fatal("rejected merge persisted")
	}
	must(t, s.Close())
	if err := checkpoint.CheckSchemaTarget(3, 2); !errors.Is(err, checkpoint.ErrUnsafeDowngrade) {
		t.Fatalf("unsafe downgrade permission: %v", err)
	}
	before, err := os.ReadFile(file.Path)
	must(t, err)
	state.SchemaVersion = 2
	if err := file.Save(state); !errors.Is(err, checkpoint.ErrUnsafeDowngrade) {
		t.Fatalf("unsafe save: %v", err)
	}
	after, err := os.ReadFile(file.Path)
	must(t, err)
	if !bytes.Equal(before, after) {
		t.Fatal("downgrade overwrote GitHub records")
	}
}
