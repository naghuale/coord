package shadowpilot_test

import (
	"testing"

	"coord/internal/shadowpilot"
)

func TestNewEvidenceBundleIsEmpty(t *testing.T) {
	b, err := shadowpilot.NewEvidenceBundle("sv-run-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(b.Refs) != 0 {
		t.Fatalf("expected empty refs, got %d", len(b.Refs))
	}
	if b.ProductionAuthorityAsserted {
		t.Fatal("production_authority_asserted must be false")
	}
}

func TestEvidenceBundleValidateRejectsProductionAuthority(t *testing.T) {
	b := shadowpilot.EvidenceBundle{
		ShadowRunID:                 "sv-run-1",
		Refs:                        []shadowpilot.EvidenceRef{},
		ProductionAuthorityAsserted: true,
	}
	if err := b.Validate(); err == nil {
		t.Fatal("expected error when production_authority_asserted is true")
	}
}

func TestEvidenceBundleAddRefAcceptsValid(t *testing.T) {
	b, _ := shadowpilot.NewEvidenceBundle("sv-run-1")
	ref := shadowpilot.EvidenceRef{Kind: shadowpilot.EvidenceReviewResult, ID: "event-001"}
	if err := b.AddRef(ref); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(b.Refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(b.Refs))
	}
}

func TestEvidenceBundleAddRefRejectsDuplicate(t *testing.T) {
	b, _ := shadowpilot.NewEvidenceBundle("sv-run-1")
	ref := shadowpilot.EvidenceRef{Kind: shadowpilot.EvidenceReviewResult, ID: "event-001"}
	_ = b.AddRef(ref)
	if err := b.AddRef(ref); err == nil {
		t.Fatal("expected error for duplicate ref")
	}
}

func TestEvidenceBundleAddRefRejectsEmptyKind(t *testing.T) {
	b, _ := shadowpilot.NewEvidenceBundle("sv-run-1")
	ref := shadowpilot.EvidenceRef{Kind: "", ID: "event-001"}
	if err := b.AddRef(ref); err == nil {
		t.Fatal("expected error for empty kind")
	}
}

func TestEvidenceBundleAddRefRejectsEmptyID(t *testing.T) {
	b, _ := shadowpilot.NewEvidenceBundle("sv-run-1")
	ref := shadowpilot.EvidenceRef{Kind: shadowpilot.EvidenceAuditResult, ID: ""}
	if err := b.AddRef(ref); err == nil {
		t.Fatal("expected error for empty id")
	}
}

func TestEvidenceBundleValidateAcceptsMultipleKinds(t *testing.T) {
	b, _ := shadowpilot.NewEvidenceBundle("sv-run-1")
	_ = b.AddRef(shadowpilot.EvidenceRef{Kind: shadowpilot.EvidenceReviewResult, ID: "rr-001"})
	_ = b.AddRef(shadowpilot.EvidenceRef{Kind: shadowpilot.EvidenceAuditResult, ID: "ar-001"})
	_ = b.AddRef(shadowpilot.EvidenceRef{Kind: shadowpilot.EvidenceDiscussionNode, ID: "dn-001"})
	if err := b.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(b.Refs) != 3 {
		t.Fatalf("expected 3 refs, got %d", len(b.Refs))
	}
}
