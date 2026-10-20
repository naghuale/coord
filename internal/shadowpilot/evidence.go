package shadowpilot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// EvidenceRef is a read-only reference to a canonical domain event or record.
// It holds only the stable ID; the referenced object is never embedded or copied
// into the shadow evidence bundle. The shadow pilot holds no production authority
// and must not dereference these IDs to modify the referenced objects.
type EvidenceRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Evidence kinds used by the shadow pilot.
const (
	EvidenceReviewResult   = "review_result"
	EvidenceAuditResult    = "audit_result"
	EvidenceDiscussionNode = "discussion_node"
	EvidenceExecutorResult = "executor_result"
	EvidenceReadiness      = "readiness_evaluation"
	EvidenceRestartCycle   = "restart_cycle"
)

func (e EvidenceRef) Validate() error {
	if strings.TrimSpace(e.Kind) == "" {
		return errors.New("evidence ref: kind required")
	}
	if strings.TrimSpace(e.ID) == "" {
		return errors.New("evidence ref: id required")
	}
	return nil
}

// EvidenceBundle is the complete read-only evidence record for a shadow run.
// It contains only references (IDs) to canonical events; the events themselves
// live in the shadow CanonicalStore and are never mutated through this bundle.
// The shadow pilot may read from the shadow store by these IDs for comparison
// purposes only.
//
// ProductionAuthorityAsserted is always false and validated as such. Any
// attempt to set it to true is a stop condition (StopProductionStateMutation).
type EvidenceBundle struct {
	ShadowRunID  ShadowRunID   `json:"shadow_run_id"`
	Refs         []EvidenceRef `json:"refs"`
	Manifest     []EvidenceRef `json:"manifest"`
	BundleDigest string        `json:"bundle_digest"`
	// Provenance identifies the evidence source. It is a caller-supplied
	// attestation, not proof of authentication or production authority.
	Provenance                  string `json:"provenance"`
	ProductionAuthorityAsserted bool   `json:"production_authority_asserted"`
}

func (b EvidenceBundle) Validate() error {
	if err := b.ShadowRunID.Validate(); err != nil {
		return fmt.Errorf("evidence bundle: %w", err)
	}
	if b.ProductionAuthorityAsserted {
		return errors.New("evidence bundle: production_authority_asserted must be false; shadow pilot holds no production authority")
	}
	if len(b.Manifest) == 0 {
		return errors.New("evidence bundle: non-empty manifest required")
	}
	if strings.TrimSpace(b.Provenance) == "" {
		return errors.New("evidence bundle: provenance required")
	}
	seen := make(map[EvidenceRef]bool, len(b.Refs))
	for i, ref := range b.Refs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("evidence bundle: ref[%d]: %w", i, err)
		}
		if seen[ref] {
			return fmt.Errorf("evidence bundle: duplicate ref %q/%q", ref.Kind, ref.ID)
		}
		seen[ref] = true
	}
	if !slices.Equal(orderedEvidence(b.Refs), orderedEvidence(b.Manifest)) {
		return errors.New("evidence bundle: refs do not match manifest")
	}
	digest, err := EvidenceBundleDigest(b.ShadowRunID, b.Manifest, b.Provenance)
	if err != nil {
		return err
	}
	if b.BundleDigest != digest {
		return errors.New("evidence bundle: digest mismatch")
	}
	return nil
}

func orderedEvidence(refs []EvidenceRef) []EvidenceRef {
	out := slices.Clone(refs)
	slices.SortFunc(out, func(a, b EvidenceRef) int {
		if c := strings.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// EvidenceBundleDigest deterministically binds the reference set, run identity
// and provenance. Sorting a copy makes insertion order irrelevant.
func EvidenceBundleDigest(id ShadowRunID, manifest []EvidenceRef, provenance string) (string, error) {
	data, err := json.Marshal(struct {
		Domain     string
		RunID      ShadowRunID
		Manifest   []EvidenceRef
		Provenance string
	}{"shadow-evidence:v1", id, orderedEvidence(manifest), provenance})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Verify binds a loaded evidence bundle to the run being evaluated.
func (b EvidenceBundle) Verify(expectedRunID ShadowRunID) error {
	if b.ShadowRunID != expectedRunID {
		return errors.New("evidence bundle: shadow_run_id mismatch")
	}
	return b.Validate()
}

// Seal finalizes a builder with explicit provenance. An empty draft cannot be
// used as evidence. Invalid input leaves the builder unchanged.
func (b *EvidenceBundle) Seal(provenance string) error {
	candidate := *b
	candidate.Provenance = provenance
	candidate.Manifest = slices.Clone(b.Refs)
	digest, err := EvidenceBundleDigest(candidate.ShadowRunID, candidate.Manifest, provenance)
	if err != nil {
		return err
	}
	candidate.BundleDigest = digest
	if err := candidate.Validate(); err != nil {
		return err
	}
	*b = candidate
	return nil
}

// AddRef appends a single evidence reference to the bundle. It returns an error
// if the reference already exists or is invalid.
func (b *EvidenceBundle) AddRef(ref EvidenceRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	key := ref.Kind + ":" + ref.ID
	for _, existing := range b.Refs {
		if existing.Kind+":"+existing.ID == key {
			return fmt.Errorf("evidence bundle: ref %q/%q already present", ref.Kind, ref.ID)
		}
	}
	b.Refs = append(slices.Clone(b.Refs), ref)
	b.Manifest = slices.Clone(b.Refs)
	digest, err := EvidenceBundleDigest(b.ShadowRunID, b.Manifest, b.Provenance)
	if err != nil {
		return err
	}
	b.BundleDigest = digest
	return nil
}

// NewEvidenceBundle creates an empty evidence bundle for the given shadow run.
// ProductionAuthorityAsserted is always false.
func NewEvidenceBundle(id ShadowRunID) (EvidenceBundle, error) {
	if err := id.Validate(); err != nil {
		return EvidenceBundle{}, err
	}
	return EvidenceBundle{ShadowRunID: id, Refs: []EvidenceRef{}, ProductionAuthorityAsserted: false}, nil
}
