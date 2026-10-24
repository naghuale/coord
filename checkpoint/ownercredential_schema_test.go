package checkpoint

import (
	"coord/store"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestSchemaThreeSnapshotLoadsWithoutWriteAndFirstCommitPublishesFive(t *testing.T) {
	file := migrationTestFile(t)
	state := store.EmptyCanonicalState()
	state.SchemaVersion = 3
	state.OwnerCredentials = nil
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "owner_credentials") // v3 predates this field
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file.Path, raw, 0600); err != nil {
		t.Fatal(err)
	}

	loaded, err := DecodeCanonicalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != 3 || len(loaded.OwnerCredentials) != 0 {
		t.Fatalf("schema 3 decode=%+v", loaded)
	}
	unchanged, err := os.ReadFile(file.Path)
	if err != nil || string(unchanged) != string(raw) {
		t.Fatalf("read changed source snapshot: err=%v", err)
	}

	s, err := store.NewCanonical(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close canonical store: %v", err)
		}
	})
	if err := s.UpdateCanonical(func(*store.CanonicalState) error { return nil }); err != nil {
		t.Fatal(err)
	}
	upgraded, err := os.ReadFile(file.Path)
	if err != nil {
		t.Fatal(err)
	}
	var current store.CanonicalState
	if err := json.Unmarshal(upgraded, &current); err != nil {
		t.Fatal(err)
	}
	if current.SchemaVersion != 5 || len(current.OwnerCredentials) != 0 {
		t.Fatalf("schema 4 first commit=%+v", current)
	}
}

func TestSchemaFiveRejectsUnknownFutureAndOldWriterDowngrade(t *testing.T) {
	state := store.EmptyCanonicalState()
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if loaded, err := DecodeCanonicalSnapshot(encoded); err != nil || loaded.SchemaVersion != 5 {
		t.Fatalf("schema 5 decode version=%d err=%v", loaded.SchemaVersion, err)
	}
	if err := CheckSchemaTarget(4, 3); !errors.Is(err, ErrUnsafeDowngrade) {
		t.Fatalf("old schema 3 writer downgrade error=%v", err)
	}
	if err := CheckSchemaTarget(5, 4); !errors.Is(err, ErrUnsafeDowngrade) {
		t.Fatalf("schema 4 writer downgrade error=%v", err)
	}
	state.SchemaVersion = 6
	encoded, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCanonicalSnapshot(encoded); err == nil {
		t.Fatal("unknown schema 6 decoded")
	}
}

func TestSchemaFourSnapshotLoadsWithoutWriteAndAdvancesOnCommit(t *testing.T) {
	file := migrationTestFile(t)
	state := store.EmptyCanonicalState()
	state.SchemaVersion = 4
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file.Path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := DecodeCanonicalSnapshot(raw)
	if err != nil || loaded.SchemaVersion != 4 {
		t.Fatalf("schema 4 decode version=%d err=%v", loaded.SchemaVersion, err)
	}
	s, err := store.NewCanonical(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	unchanged, err := os.ReadFile(file.Path)
	if err != nil || string(unchanged) != string(raw) {
		t.Fatalf("schema 4 open wrote snapshot: err=%v", err)
	}
	if err := s.UpdateCanonical(func(*store.CanonicalState) error { return nil }); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(file.Path)
	if err != nil {
		t.Fatal(err)
	}
	var upgraded store.CanonicalState
	if err := json.Unmarshal(current, &upgraded); err != nil || upgraded.SchemaVersion != 5 {
		t.Fatalf("schema 4 first commit version=%d err=%v", upgraded.SchemaVersion, err)
	}
}
