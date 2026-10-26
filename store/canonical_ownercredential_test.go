package store

import (
	"coord/assignment"
	"coord/ownercredential"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func bootstrapTestCredentials(t *testing.T) []ownercredential.Credential {
	t.Helper()
	out := make([]ownercredential.Credential, 0, 2)
	for _, id := range []string{"owner-primary", "owner-backup"} {
		public, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key := append([]byte(nil), public...)
		out = append(out, ownercredential.Credential{ID: id, Algorithm: ownercredential.EdDSA, PublicKey: key, Fingerprint: ownercredential.Fingerprint(ownercredential.EdDSA, key)})
	}
	return out
}

func ownerEnrolledCanonicalState(t *testing.T, state CanonicalState) CanonicalState {
	t.Helper()
	if state.OwnerCredentials == nil {
		state.OwnerCredentials = map[string]ownercredential.Aggregate{}
	}
	aggregate, err := ownercredential.Bootstrap("owner", "owner-bootstrap", bootstrapTestCredentials(t), time.Unix(50, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	state.OwnerCredentials["owner"] = aggregate
	return state
}

func TestBootstrapOwnerCredentialsIsOneTimeAndAtomic(t *testing.T) {
	p := &canonicalMemory{state: EmptyCanonicalState()}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close canonical store: %v", err)
		}
	})
	credentials := bootstrapTestCredentials(t)
	receipt, err := s.BootstrapOwnerCredentials("owner-1", "bootstrap-1", credentials)
	if err != nil {
		t.Fatal(err)
	}
	if p.saveCalls != 1 || p.state.Revision != 1 || p.state.SchemaVersion != CanonicalSchemaVersion || len(p.state.OwnerCredentials["owner-1"].Credentials) != 2 {
		t.Fatalf("bootstrap was not one canonical commit: saves=%d state=%+v", p.saveCalls, p.state)
	}
	retry, err := s.BootstrapOwnerCredentials("owner-1", "bootstrap-1", credentials)
	if err != nil || !reflect.DeepEqual(retry, receipt) || p.saveCalls != 1 {
		t.Fatalf("exact lost-response retry changed bootstrap: receipt=%+v err=%v saves=%d", retry, err, p.saveCalls)
	}
	if _, err := s.BootstrapOwnerCredentials("owner-1", "bootstrap-2", credentials); !errors.Is(err, ErrOwnerBootstrapClosed) {
		t.Fatalf("second bootstrap err=%v", err)
	}
	if _, err := s.BootstrapOwnerCredentials("owner-2", "bootstrap-other-principal", bootstrapTestCredentials(t)); !errors.Is(err, ErrOwnerBootstrapClosed) {
		t.Fatalf("bootstrap for a second principal reopened initialized state: %v", err)
	}
	before, _ := json.Marshal(p.state)
	if err := s.UpdateCanonical(func(state *CanonicalState) error {
		state.OwnerCredentials["forged"] = p.state.OwnerCredentials["owner-1"]
		return nil
	}); !errors.Is(err, ErrOwnerCommandRequired) {
		t.Fatalf("generic mutator wrote OwnerCredentials: %v", err)
	}
	after, _ := json.Marshal(p.state)
	if !reflect.DeepEqual(before, after) || p.saveCalls != 1 {
		t.Fatal("rejected direct store mutation changed canonical state")
	}
}

func TestBootstrapOwnerCredentialsRequiresTwoKeysAndFailedSavePublishesNothing(t *testing.T) {
	p := &canonicalMemory{state: EmptyCanonicalState(), fail: true}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close canonical store: %v", err)
		}
	})
	credentials := bootstrapTestCredentials(t)
	before, _ := json.Marshal(p.state)
	if _, err := s.BootstrapOwnerCredentials("owner-1", "bootstrap-1", credentials[:1]); err == nil {
		t.Fatal("bootstrap accepted one credential")
	}
	if p.saveCalls != 0 {
		t.Fatal("invalid bootstrap attempted durable write")
	}
	if _, err := s.BootstrapOwnerCredentials("owner-1", "bootstrap-1", credentials); !errors.Is(err, ErrPersistence) {
		t.Fatalf("failed persistence error=%v", err)
	}
	after, _ := json.Marshal(p.state)
	if !reflect.DeepEqual(before, after) || len(p.state.OwnerCredentials) != 0 {
		t.Fatal("failed bootstrap published partial OwnerCredential state")
	}
	if _, err := s.BootstrapOwnerCredentials("owner-1", "bootstrap-1", credentials); err == nil {
		t.Fatal("poisoned store accepted a retry without reopen")
	}
}

func TestSchemaThreeLoadsWithoutEnrollmentAndSchemaSixKeepsLedgerStatusReadable(t *testing.T) {
	p := &canonicalMemory{state: EmptyCanonicalState()}
	p.state.SchemaVersion = 3
	p.state.OwnerCredentials = nil
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	oldSnapshot, err := s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if oldSnapshot.SchemaVersion != 3 || len(oldSnapshot.OwnerCredentials) != 0 || p.saveCalls != 0 {
		t.Fatalf("schema 3 load enrolled or wrote OwnerCredential: version=%d owners=%v saves=%d", oldSnapshot.SchemaVersion, oldSnapshot.OwnerCredentials, p.saveCalls)
	}
	_ = s.Close()

	f := newLedgerFixture(t)
	if state, err := f.s.CanonicalSnapshot(); err != nil || state.SchemaVersion != CanonicalSchemaVersion {
		t.Fatalf("schema 6 snapshot=%d err=%v", state.SchemaVersion, err)
	}
	f.s.FreezeAssignmentLedger()
	state, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	status, err := InspectAssignment(state, f.id, f.now)
	if err != nil || status.State != "OFFERED" || status.WorkMayStart {
		t.Fatalf("schema 6 frozen ledger status=%+v err=%v", status, err)
	}
}

func TestSchemaSevenFailsClosed(t *testing.T) {
	state := EmptyCanonicalState()
	state.SchemaVersion = 7
	if err := ValidateCanonicalState(state); err == nil {
		t.Fatal("accepted unsupported schema 7")
	}
}

func TestSchemaThreeCommitPublishesSchemaSix(t *testing.T) {
	p := &canonicalMemory{state: EmptyCanonicalState()}
	p.state.SchemaVersion = 3
	p.state.OwnerCredentials = nil
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close canonical store: %v", err)
		}
	})
	if err := s.UpdateCanonical(func(*CanonicalState) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if p.state.SchemaVersion != 6 || p.saveCalls != 1 {
		t.Fatalf("first commit did not upgrade schema 3 to schema 6: version=%d saves=%d", p.state.SchemaVersion, p.saveCalls)
	}
}

func TestSchemaFourLoadsReadOnlyAndNextCommitPublishesSix(t *testing.T) {
	p := &canonicalMemory{state: EmptyCanonicalState()}
	p.state.SchemaVersion = 4
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	loaded, err := s.CanonicalSnapshot()
	if err != nil || loaded.SchemaVersion != 4 || p.saveCalls != 0 {
		t.Fatalf("schema 4 load version=%d saves=%d err=%v", loaded.SchemaVersion, p.saveCalls, err)
	}
	if err := s.UpdateCanonical(func(*CanonicalState) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if p.state.SchemaVersion != 6 || p.saveCalls != 1 {
		t.Fatalf("schema 4 first write version=%d saves=%d", p.state.SchemaVersion, p.saveCalls)
	}
}

func TestSchemaFiveLoadsReadOnlyAndNextCommitPublishesSix(t *testing.T) {
	p := &canonicalMemory{state: EmptyCanonicalState()}
	p.state.SchemaVersion = 5
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	loaded, err := s.CanonicalSnapshot()
	if err != nil || loaded.SchemaVersion != 5 || p.saveCalls != 0 {
		t.Fatalf("schema 5 load version=%d saves=%d err=%v", loaded.SchemaVersion, p.saveCalls, err)
	}
	if err := s.UpdateCanonical(func(*CanonicalState) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if p.state.SchemaVersion != 6 || p.saveCalls != 1 {
		t.Fatalf("schema 5 first write version=%d saves=%d", p.state.SchemaVersion, p.saveCalls)
	}
}

func TestSchemaFourAssignmentLedgerFreezeAndStatusRemainReadable(t *testing.T) {
	f := newLedgerFixture(t)
	state, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	state.SchemaVersion = 4
	p := &canonicalMemory{state: state}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.FreezeAssignmentLedger()
	loaded, err := s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	status, err := InspectAssignment(loaded, f.id, f.now)
	if err != nil || status.State != assignment.Offered || status.WorkMayStart || p.saveCalls != 0 {
		t.Fatalf("schema 4 frozen assignment status=%+v saves=%d err=%v", status, p.saveCalls, err)
	}
}

func TestSchemaThreeNormalizationUsesNoClockSideEffects(t *testing.T) {
	state := EmptyCanonicalState()
	state.SchemaVersion = 3
	state.OwnerCredentials = nil
	normalized := NormalizeCanonicalState(state)
	if normalized.SchemaVersion != 3 || normalized.OwnerCredentials == nil || len(normalized.OwnerCredentials) != 0 {
		t.Fatalf("schema 3 normalization=%+v", normalized)
	}
	if normalized.Revision != state.Revision || !reflect.DeepEqual(normalized.Orchestration, state.Orchestration) {
		t.Fatal("normalization changed unrelated canonical data")
	}
}
