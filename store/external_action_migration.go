package store

import (
	"coord/internal/externalaction"
	"encoding/json"
	"fmt"
)

// SchemaMigration records the canonical publication of a format conversion.
// Existing domain and action events are preserved byte-for-byte in meaning.
type SchemaMigration struct {
	ID             string `json:"id"`
	From           uint32 `json:"from"`
	To             uint32 `json:"to"`
	SourceDigest   string `json:"source_digest"`
	SourceRevision uint64 `json:"source_revision"`
	Revision       uint64 `json:"revision"`
}

// UpgradeExternalActions is pure. Only the exclusive persistence owner may
// publish its result, after verified backup/manifest. Schema-2 records carrying
// any schema-3 discriminator are partial/corrupt, not a normalization shortcut.
func UpgradeExternalActions(source CanonicalState, sourceDigest string) (CanonicalState, error) {
	if source.SchemaVersion != 2 {
		return CanonicalState{}, fmt.Errorf("migration requires schema 2")
	}
	if len(source.SchemaMigrations) != 0 {
		return CanonicalState{}, fmt.Errorf("partial migration history")
	}
	data, err := json.Marshal(source)
	if err != nil {
		return CanonicalState{}, err
	}
	var target CanonicalState
	if err := json.Unmarshal(data, &target); err != nil {
		return CanonicalState{}, err
	}
	for id, a := range target.Orchestration.Actions {
		if a.Family != "" || a.SchemaVersion != 0 {
			return CanonicalState{}, fmt.Errorf("partial action migration")
		}
		a.Family = externalaction.AgentSession
		a.SchemaVersion = externalaction.ContractVersion
		target.Orchestration.Actions[id] = a
	}
	target.SchemaVersion = 3
	// Validate converted old data before adding the migration event. Digest(a)
	// retains the old session preimage, so old keys and event hashes must match.
	if err := ValidateCanonicalState(target); err != nil {
		return CanonicalState{}, err
	}
	if sourceDigest != "" {
		if target.Revision == ^uint64(0) {
			return CanonicalState{}, fmt.Errorf("migration revision exhausted")
		}
		target.Revision++
		target.SchemaMigrations = []SchemaMigration{{"external-action-schema-2-to-3", 2, 3, sourceDigest, source.Revision, target.Revision}}
	}
	return target, ValidateCanonicalState(target)
}

func validateSchemaMigrations(s CanonicalState) error {
	if len(s.SchemaMigrations) > 1 {
		return fmt.Errorf("duplicate schema migration")
	}
	for _, e := range s.SchemaMigrations {
		if e.ID != "external-action-schema-2-to-3" || e.From != 2 || e.To != 3 || len(e.SourceDigest) != 64 || e.SourceRevision == ^uint64(0) || e.Revision != e.SourceRevision+1 || e.Revision > s.Revision {
			return fmt.Errorf("invalid schema migration event")
		}
	}
	return nil
}

// ValidateSchema2 verifies the complete previous canonical schema without
// upgrading persisted state or accepting a partially migrated input.
func ValidateSchema2(s CanonicalState) error {
	_, err := UpgradeExternalActions(s, "")
	return err
}
