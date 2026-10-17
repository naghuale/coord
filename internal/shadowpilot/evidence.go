package shadowpilot

import (
	"errors"
	"fmt"
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
	ShadowRunID                 ShadowRunID   `json:"shadow_run_id"`
	Refs                        []EvidenceRef `json:"refs"`
	ProductionAuthorityAsserted bool          `json:"production_authority_asserted"`
}

func (b EvidenceBundle) Validate() error {
	if err := b.ShadowRunID.Validate(); err != nil {
		return fmt.Errorf("evidence bundle: %w", err)
	}
	if b.ProductionAuthorityAsserted {
		return errors.New("evidence bundle: production_authority_asserted must be false; shadow pilot holds no production authority")
	}
	seen := make(map[string]bool, len(b.Refs))
	for i, ref := range b.Refs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("evidence bundle: ref[%d]: %w", i, err)
		}
		key := ref.Kind + ":" + ref.ID
		if seen[key] {
			return fmt.Errorf("evidence bundle: duplicate ref %q/%q", ref.Kind, ref.ID)
		}
		seen[key] = true
	}
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
	b.Refs = append(b.Refs, ref)
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
