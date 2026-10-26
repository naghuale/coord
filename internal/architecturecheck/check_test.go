package architecturecheck

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveRegistryPasses(t *testing.T) {
	root := repoRoot(t)
	res, err := Check(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Violations) != 0 {
		for _, v := range res.Violations {
			t.Logf("- [%s] %s", v.Code, v.Detail)
		}
		t.Fatalf("live registry expected PASS, got %d violations", len(res.Violations))
	}
	if res.Components < 1 {
		t.Fatalf("expected components, got %d", res.Components)
	}
}

func TestGoldenCases(t *testing.T) {
	casesDir := filepath.Join("testdata", "golden")
	entries, err := os.ReadDir(casesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(casesDir, name)
			want := loadGoldenFindings(t, filepath.Join(dir, "findings.json"))
			res, err := Check(Options{Root: dir, SkipChangedDocs: true})
			if err != nil {
				t.Fatal(err)
			}
			got := codes(res.Violations)
			if !sameCodes(got, want) {
				t.Fatalf("codes mismatch\n got: %v\nwant: %v\nfull: %v", got, want, res.Violations)
			}
		})
	}
}

func TestMissingSymbolKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		reg.Components[0].Evidence.Symbols = []string{"NoSuchSymbolXYZ"}
		reg.Components[0].Evidence.Tests = []string{"TestFixtureOK"}
	})
	res, err := Check(Options{Root: dir, SkipChangedDocs: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeSymbolMissing) {
		t.Fatalf("expected %s, got %v", CodeSymbolMissing, res.Violations)
	}
}

func TestMissingTestKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		reg.Components[0].Evidence.Tests = []string{"TestDoesNotExist"}
	})
	res, err := Check(Options{Root: dir, SkipChangedDocs: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeTestMissing) {
		t.Fatalf("expected %s, got %v", CodeTestMissing, res.Violations)
	}
}

func TestPartialRequiresMissing(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		reg.Components[0].Missing = nil
	})
	res, err := Check(Options{Root: dir, SkipChangedDocs: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeStatusInvalid) {
		t.Fatalf("expected %s, got %v", CodeStatusInvalid, res.Violations)
	}
}

func TestProposedMustBeNonNormative(t *testing.T) {
	dir := t.TempDir()
	truthy := true
	writeMinimalFixture(t, dir, func(reg *Registry) {
		reg.Components[0].Status = "proposed"
		reg.Components[0].NormativeForCurrentProduct = &truthy
		reg.Components[0].Implemented = nil
		reg.Components[0].Missing = []string{"future"}
		reg.Components[0].Evidence.Symbols = nil
		reg.Components[0].Evidence.Tests = nil
	})
	res, err := Check(Options{Root: dir, SkipChangedDocs: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeProposedAsImplemented) {
		t.Fatalf("expected %s, got %v", CodeProposedAsImplemented, res.Violations)
	}
}

func TestConnectedWithoutEntrypointKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		reg.Components[0].Code = "PARTIAL"
		reg.Components[0].Connection = "PRODUCTION_CONNECTED"
		reg.Components[0].Qualification = "NOT_EVALUATED"
		reg.Components[0].Production = "DISABLED"
		reg.Components[0].ProductionEntrypoints = nil
	})
	res, err := Check(Options{Root: dir, SkipChangedDocs: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeConnectedNoEntrypoint) {
		t.Fatalf("expected %s, got %v", CodeConnectedNoEntrypoint, res.Violations)
	}
}

func TestActiveWithoutRuntimeKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		reg.Components[0].Code = "PARTIAL"
		reg.Components[0].Connection = "NOT_CONNECTED"
		reg.Components[0].Qualification = "NOT_EVALUATED"
		reg.Components[0].Production = "ACTIVE"
		reg.Components[0].Evidence.Symbols = nil
		reg.Components[0].ProductionEntrypoints = nil
		// Keep a test so we isolate the active-runtime rule.
		reg.Components[0].Evidence.Tests = []string{"TestFixtureOK"}
	})
	res, err := Check(Options{Root: dir, SkipChangedDocs: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeActiveNoRuntime) {
		t.Fatalf("expected %s, got %v", CodeActiveNoRuntime, res.Violations)
	}
}

func TestProposedProductionAuthorityKills(t *testing.T) {
	// Legacy self-declared PROPOSED row: still fails when registry source is proposed.
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		reg.Components[0].Code = "PARTIAL"
		reg.Components[0].Connection = "TEST_ONLY"
		reg.Components[0].Qualification = "PARTIAL"
		reg.Components[0].Production = "ACTIVE"
		reg.Components[0].Evidence.Symbols = []string{"FixtureSymbol"}
		reg.Components[0].Evidence.Tests = []string{"TestFixtureOK"}
		reg.Requirements = map[string]Requirement{
			"CM-TEST": {
				SourceDocumentID: "src-proposed",
				Section:          "1",
				Component:        "fixture-component",
				NormativeStatus:  "ACCEPTED", // row cannot raise; registry wins (F97-1)
			},
		}
	})
	writeDocRegistry(t, dir, `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: src-proposed
    path: docs/proposed.md
    status: proposed
`)
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeProdAuthWrongStatus) {
		t.Fatalf("expected %s, got %v", CodeProdAuthWrongStatus, res.Violations)
	}
}

// F97-1: registry says proposed, row claims ACCEPTED, Production ACTIVE -> FAIL.
func TestF97_1_RegistryProposedRowAcceptedProductionActiveKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		setAxes(&reg.Components[0], "PARTIAL", "TEST_ONLY", "PARTIAL", "ACTIVE")
		reg.Requirements = map[string]Requirement{
			"CM-F97-1": {
				SourceDocumentID: "src-proposed",
				Section:          "1",
				Component:        "fixture-component",
				NormativeStatus:  "ACCEPTED",
			},
		}
	})
	writeDocRegistry(t, dir, docRegistryYAML("src-proposed", "proposed"))
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeProdAuthWrongStatus) {
		t.Fatalf("expected %s, got %v", CodeProdAuthWrongStatus, res.Violations)
	}
}

// F97-1: source missing from registry -> FAIL for production claim.
func TestF97_1_MissingSourceDocumentKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		setAxes(&reg.Components[0], "PARTIAL", "TEST_ONLY", "PARTIAL", "ACTIVE")
		reg.Requirements = map[string]Requirement{
			"CM-MISSING": {
				SourceDocumentID: "does-not-exist",
				Section:          "1",
				Component:        "fixture-component",
				NormativeStatus:  "ACCEPTED",
			},
		}
	})
	writeDocRegistry(t, dir, `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: other-doc
    path: docs/other.md
    status: accepted
`)
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeUnknownRequirementID) {
		t.Fatalf("expected unresolved source %s, got %v", CodeUnknownRequirementID, res.Violations)
	}
	if !hasCode(res.Violations, CodeProposedProductionAuth) {
		t.Fatalf("expected %s for missing source production claim, got %v", CodeProposedProductionAuth, res.Violations)
	}
}

// F97-1: requirement has no sourceDocumentId -> FAIL.
func TestF97_1_RequirementMissingSourceFieldKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		setAxes(&reg.Components[0], "PARTIAL", "TEST_ONLY", "PARTIAL", "ACTIVE")
		reg.Requirements = map[string]Requirement{
			"CM-NOSRC": {
				SourceDocumentID: "",
				Section:          "1",
				Component:        "fixture-component",
				NormativeStatus:  "ACCEPTED",
			},
		}
	})
	res, err := Check(Options{Root: dir, SkipChangedDocs: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeUnknownRequirementID) {
		t.Fatalf("expected %s for missing sourceDocumentId, got %v", CodeUnknownRequirementID, res.Violations)
	}
	if !hasCode(res.Violations, CodeProposedProductionAuth) {
		t.Fatalf("expected %s for unsourced production claim, got %v", CodeProposedProductionAuth, res.Violations)
	}
}

// F97-1: historical / superseded source cannot authorize production claims.
func TestF97_1_HistoricalAndSupersededSourceKillProductionClaims(t *testing.T) {
	for _, status := range []string{"historical", "superseded"} {
		status := status
		t.Run(status, func(t *testing.T) {
			dir := t.TempDir()
			writeMinimalFixture(t, dir, func(reg *Registry) {
				setAxes(&reg.Components[0], "IMPLEMENTED", "PRODUCTION_CONNECTED", "PASS", "DISABLED")
				reg.Components[0].ProductionEntrypoints = []string{"FixtureSymbol"}
				reg.Requirements = map[string]Requirement{
					"CM-HIST": {
						SourceDocumentID: "src-" + status,
						Section:          "1",
						Component:        "fixture-component",
						NormativeStatus:  "ACCEPTED",
					},
				}
			})
			writeDocRegistry(t, dir, docRegistryYAML("src-"+status, status))
			res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
			if err != nil {
				t.Fatal(err)
			}
			if !hasCode(res.Violations, CodeProdAuthWrongStatus) {
				t.Fatalf("expected %s for status=%s, got %v", CodeProdAuthWrongStatus, status, res.Violations)
			}
		})
	}
}

// F97-1 / R3-F1 positive control: full PRODUCTION-AUTHORITY-SOURCE-01 source -> PASS.
func TestF97_1_AcceptedSourceAllowsProductionClaim(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		setAxes(&reg.Components[0], "IMPLEMENTED", "PRODUCTION_CONNECTED", "PASS", "ACTIVE")
		reg.Components[0].ProductionEntrypoints = []string{"FixtureSymbol"}
		reg.Requirements = map[string]Requirement{
			"CM-OK": {
				SourceDocumentID: "src-accepted",
				Section:          "1",
				Component:        "fixture-component",
				NormativeStatus:  "PROPOSED", // row cannot raise; registry four-flag rule wins
			},
		}
	})
	writeDocRegistry(t, dir, docRegistryAuthorityYAML("src-accepted", "accepted", true, true, "product"))
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if hasCode(res.Violations, CodeProposedProductionAuth) ||
		hasCode(res.Violations, CodeProdAuthWrongStatus) ||
		hasCode(res.Violations, CodeProdAuthWrongAuthorityClass) ||
		hasCode(res.Violations, CodeProdAuthMissingCanonical) ||
		hasCode(res.Violations, CodeProdAuthMissingCurrentNormative) ||
		hasCode(res.Violations, CodeProdAuthInvalidField) {
		t.Fatalf("full-authority source must allow production claim, got %v", res.Violations)
	}
	if hasCode(res.Violations, CodeUnknownRequirementID) {
		t.Fatalf("accepted source must resolve, got %v", res.Violations)
	}
}

// F97-1 residual: absent requirements + production claim -> FAIL.
func TestF97_1_AbsentRequirementsWithProductionClaimKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		setAxes(&reg.Components[0], "PARTIAL", "TEST_ONLY", "PARTIAL", "ACTIVE")
		reg.Requirements = nil
	})
	res, err := Check(Options{Root: dir, SkipChangedDocs: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeProposedProductionAuth) {
		t.Fatalf("expected %s for absent requirements, got %v", CodeProposedProductionAuth, res.Violations)
	}
}

// F97-1 residual: empty requirements inventory + production claim -> FAIL.
func TestF97_1_EmptyRequirementsWithProductionClaimKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		setAxes(&reg.Components[0], "PARTIAL", "TEST_ONLY", "PASS", "DISABLED")
		reg.Requirements = map[string]Requirement{}
	})
	res, err := Check(Options{Root: dir, SkipChangedDocs: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeProposedProductionAuth) {
		t.Fatalf("expected %s for empty inventory, got %v", CodeProposedProductionAuth, res.Violations)
	}
}

// F97-1 residual: production-claimed component with no linked requirement -> FAIL.
func TestF97_1_ProductionClaimWithoutLinkedRequirementKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		setAxes(&reg.Components[0], "IMPLEMENTED", "PRODUCTION_CONNECTED", "PASS", "ACTIVE")
		reg.Components[0].ProductionEntrypoints = []string{"FixtureSymbol"}
		reg.Requirements = map[string]Requirement{
			"CM-OTHER": {
				SourceDocumentID: "src-accepted",
				Section:          "1",
				Component:        "other-component", // different component
				NormativeStatus:  "ACCEPTED",
			},
		}
		reg.Components = append(reg.Components, Component{
			ID: "other-component", Title: "Other", Status: "partial",
			NormativeForCurrentProduct: reg.Components[0].NormativeForCurrentProduct,
			NormativeSources:           []string{"docs/ADR-FIXTURE.md"},
			Implemented:                []string{"x"}, Missing: []string{"y"},
			Evidence:  Evidence{Symbols: []string{"FixtureSymbol"}, Tests: []string{"TestFixtureOK"}},
			Documents: ComponentDocs{CurrentAnchor: "other-component", TargetAnchor: "other-component"},
			Code:      "PARTIAL", Connection: "NOT_CONNECTED", Qualification: "NOT_EVALUATED", Production: "DISABLED",
		})
	})
	writeDocRegistry(t, dir, docRegistryAuthorityYAML("src-accepted", "accepted", true, true, "product"))
	mustWrite(t, filepath.Join(dir, "docs/ARCHITECTURE.md"), "<!-- fixture-component -->\n<!-- other-component -->\n")
	mustWrite(t, filepath.Join(dir, "docs/TARGET-ARCHITECTURE.md"), "<!-- fixture-component -->\n<!-- other-component -->\n")
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeProposedProductionAuth) {
		t.Fatalf("expected %s for unlinked production claim, got %v", CodeProposedProductionAuth, res.Violations)
	}
}

// F97-1 residual: requirement with empty component -> FAIL.
func TestF97_1_RequirementEmptyComponentKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		reg.Requirements = map[string]Requirement{
			"CM-EMPTY": {
				SourceDocumentID: "src-accepted",
				Section:          "1",
				Component:        "",
				NormativeStatus:  "ACCEPTED",
			},
		}
	})
	writeDocRegistry(t, dir, docRegistryYAML("src-accepted", "accepted"))
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeUnknownRequirementID) {
		t.Fatalf("expected %s for empty component, got %v", CodeUnknownRequirementID, res.Violations)
	}
}

func TestF97_1_RequirementUnknownComponentKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		reg.Requirements = map[string]Requirement{
			"CM-UNK": {
				SourceDocumentID: "src-accepted",
				Section:          "1",
				Component:        "no-such-component",
				NormativeStatus:  "ACCEPTED",
			},
		}
	})
	writeDocRegistry(t, dir, docRegistryYAML("src-accepted", "accepted"))
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeUnknownRequirementID) {
		t.Fatalf("expected %s for unknown component, got %v", CodeUnknownRequirementID, res.Violations)
	}
}

func TestUnknownRequirementComponentKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		reg.Requirements = map[string]Requirement{
			"CM-X": {Component: "no-such-component", NormativeStatus: "ACCEPTED"},
		}
	})
	res, err := Check(Options{Root: dir, SkipChangedDocs: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeUnknownRequirementID) {
		t.Fatalf("expected %s, got %v", CodeUnknownRequirementID, res.Violations)
	}
}

func TestAuthoritativeActionMapWhileProposedKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		reg.ActionMapReferenceClassifications = []ActionMapReferenceClassification{{
			Path:  "docs/ARCHITECTURE.md",
			Line:  1,
			Class: "AUTHORITATIVE_REFERENCE",
			Note:  "test",
		}}
	})
	// Make ACTION-MAP.yaml proposed in a stub document registry.
	mustWrite(t, filepath.Join(dir, "docs/DOCUMENT-REGISTRY.yaml"), `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - path: docs/operations/ACTION-MAP.yaml
    status: proposed
`)
	mustWrite(t, filepath.Join(dir, "docs/ARCHITECTURE.md"), "Action Map is required.\n")
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeAuthoritativeActionMap) {
		t.Fatalf("expected %s, got %v", CodeAuthoritativeActionMap, res.Violations)
	}
}

func TestUnclassifiedActionMapHitKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		// Presence of the section activates the scanner; leave the hit unclassified.
		reg.ActionMapReferenceClassifications = []ActionMapReferenceClassification{{
			Path:  "docs/ARCHITECTURE.md",
			Line:  999,
			Class: "NON_NORMATIVE_CHECKLIST",
			Note:  "decoy",
		}}
	})
	mustWrite(t, filepath.Join(dir, "docs/ARCHITECTURE.md"), "<!-- fixture-component -->\nAction Map checklist line.\n")
	res, err := Check(Options{Root: dir, SkipChangedDocs: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeUnclassifiedActionMap) {
		t.Fatalf("expected %s, got %v", CodeUnclassifiedActionMap, res.Violations)
	}
}

// F97-3: empty / absent classification inventory must not disable the scan.
func TestF97_3_EmptyClassificationsWithHitKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		reg.ActionMapReferenceClassifications = nil
	})
	mustWrite(t, filepath.Join(dir, "docs/ARCHITECTURE.md"), "<!-- fixture-component -->\nAction Map checklist line.\n")
	res, err := Check(Options{Root: dir, SkipChangedDocs: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeUnclassifiedActionMap) {
		t.Fatalf("expected %s with empty classifications, got %v", CodeUnclassifiedActionMap, res.Violations)
	}
}

func TestF97_3_AbsentClassificationsWithHitKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		reg.ActionMapReferenceClassifications = []ActionMapReferenceClassification{}
	})
	mustWrite(t, filepath.Join(dir, "docs/ARCHITECTURE.md"), "<!-- fixture-component -->\nSee ACTION-MAP.yaml for steps.\n")
	res, err := Check(Options{Root: dir, SkipChangedDocs: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeUnclassifiedActionMap) {
		t.Fatalf("expected %s with absent/empty inventory, got %v", CodeUnclassifiedActionMap, res.Violations)
	}
}

// R3-F1 / PRODUCTION-AUTHORITY-SOURCE-01 cross-mutations and field failures.
func TestR3_F1_ActiveStatusEvenWithPositiveFlagsFails(t *testing.T) {
	assertR3F1AuthFailure(t, "active", true, true, "product", CodeProdAuthWrongStatus)
}

func TestR3_F1_AcceptedFalseCanonicalFails(t *testing.T) {
	assertR3F1AuthFailure(t, "accepted", false, true, "product", CodeProdAuthMissingCanonical)
}

func TestR3_F1_AcceptedFalseCurrentNormativeFails(t *testing.T) {
	assertR3F1AuthFailure(t, "accepted", true, false, "product", CodeProdAuthMissingCurrentNormative)
}

func TestR3_F1_AcceptedProcessAuthorityFails(t *testing.T) {
	assertR3F1AuthFailure(t, "accepted", true, true, "process", CodeProdAuthWrongAuthorityClass)
}

func TestR3_F1_AcceptedProductAllFlagsPass(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		setAxes(&reg.Components[0], "IMPLEMENTED", "PRODUCTION_CONNECTED", "PASS", "ACTIVE")
		reg.Components[0].ProductionEntrypoints = []string{"FixtureSymbol"}
		reg.Requirements = map[string]Requirement{
			"CM-R3F1-OK": {SourceDocumentID: "src-ok", Section: "1", Component: "fixture-component"},
		}
	})
	writeDocRegistry(t, dir, docRegistryAuthorityYAML("src-ok", "accepted", true, true, "product"))
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{
		CodeProposedProductionAuth, CodeProdAuthWrongStatus, CodeProdAuthWrongAuthorityClass,
		CodeProdAuthMissingCanonical, CodeProdAuthMissingCurrentNormative, CodeProdAuthInvalidField,
	} {
		if hasCode(res.Violations, code) {
			t.Fatalf("positive control must not emit %s, got %v", code, res.Violations)
		}
	}
}

func TestR3_F1_MissingCanonicalFieldFails(t *testing.T) {
	dir := t.TempDir()
	writeProductionClaimFixture(t, dir, "src-miss")
	writeDocRegistry(t, dir, `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: src-miss
    path: docs/src-miss.md
    status: accepted
    currentNormativeAuthority: true
    normativeAuthority: product
`)
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeProdAuthMissingCanonical) {
		t.Fatalf("expected %s, got %v", CodeProdAuthMissingCanonical, res.Violations)
	}
}

func TestR3_F1_MissingCurrentNormativeFieldFails(t *testing.T) {
	dir := t.TempDir()
	writeProductionClaimFixture(t, dir, "src-miss")
	writeDocRegistry(t, dir, `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: src-miss
    path: docs/src-miss.md
    status: accepted
    canonicalProductionAuthority: true
    normativeAuthority: product
`)
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeProdAuthMissingCurrentNormative) {
		t.Fatalf("expected %s, got %v", CodeProdAuthMissingCurrentNormative, res.Violations)
	}
}

func TestR3_F1_MissingNormativeAuthorityFieldFails(t *testing.T) {
	dir := t.TempDir()
	writeProductionClaimFixture(t, dir, "src-miss")
	writeDocRegistry(t, dir, `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: src-miss
    path: docs/src-miss.md
    status: accepted
    canonicalProductionAuthority: true
    currentNormativeAuthority: true
`)
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeProdAuthInvalidField) {
		t.Fatalf("expected %s for missing normativeAuthority, got %v", CodeProdAuthInvalidField, res.Violations)
	}
}

func TestR3_F1_UnknownNormativeAuthorityFails(t *testing.T) {
	assertR3F1AuthFailure(t, "accepted", true, true, "unknown_class", CodeProdAuthWrongAuthorityClass)
}

func TestR3_F1_WrongTypeCanonicalFails(t *testing.T) {
	dir := t.TempDir()
	writeProductionClaimFixture(t, dir, "src-badtype")
	writeDocRegistry(t, dir, `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: src-badtype
    path: docs/src-badtype.md
    status: accepted
    canonicalProductionAuthority: "yes"
    currentNormativeAuthority: true
    normativeAuthority: product
`)
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeProdAuthInvalidField) {
		t.Fatalf("expected %s for wrong-type canonicalProductionAuthority, got %v", CodeProdAuthInvalidField, res.Violations)
	}
}

func TestR3_F1_SplitPropertiesAcrossTwoSourcesFails(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		setAxes(&reg.Components[0], "IMPLEMENTED", "PRODUCTION_CONNECTED", "PASS", "ACTIVE")
		reg.Components[0].ProductionEntrypoints = []string{"FixtureSymbol"}
		reg.Requirements = map[string]Requirement{
			"CM-A": {SourceDocumentID: "src-a", Section: "1", Component: "fixture-component"},
			"CM-B": {SourceDocumentID: "src-b", Section: "1", Component: "fixture-component"},
		}
	})
	// Properties split: neither source alone carries all four.
	writeDocRegistry(t, dir, `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: src-a
    path: docs/src-a.md
    status: accepted
    canonicalProductionAuthority: true
    currentNormativeAuthority: false
    normativeAuthority: process
  - id: src-b
    path: docs/src-b.md
    status: accepted
    canonicalProductionAuthority: false
    currentNormativeAuthority: true
    normativeAuthority: product
`)
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	// Neither source alone authorizes; composition across sources is forbidden.
	if !hasCode(res.Violations, CodeProdAuthWrongAuthorityClass) &&
		!hasCode(res.Violations, CodeProdAuthMissingCanonical) &&
		!hasCode(res.Violations, CodeProdAuthMissingCurrentNormative) &&
		!hasCode(res.Violations, CodeProposedProductionAuth) {
		t.Fatalf("split sources must fail closed, got %v", res.Violations)
	}
}

func assertR3F1AuthFailure(t *testing.T, status string, cpa, cna bool, normativeAuthority, wantCode string) {
	t.Helper()
	dir := t.TempDir()
	writeProductionClaimFixture(t, dir, "src-r3f1")
	writeDocRegistry(t, dir, docRegistryAuthorityYAML("src-r3f1", status, cpa, cna, normativeAuthority))
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, wantCode) {
		t.Fatalf("expected %s for status=%s cpa=%t cna=%t na=%s, got %v", wantCode, status, cpa, cna, normativeAuthority, res.Violations)
	}
}

func writeProductionClaimFixture(t *testing.T, dir, srcID string) {
	t.Helper()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		setAxes(&reg.Components[0], "IMPLEMENTED", "PRODUCTION_CONNECTED", "PASS", "ACTIVE")
		reg.Components[0].ProductionEntrypoints = []string{"FixtureSymbol"}
		reg.Requirements = map[string]Requirement{
			"CM-R3F1": {SourceDocumentID: srcID, Section: "1", Component: "fixture-component"},
		}
	})
}

// R3-F2: DOCUMENT-REGISTRY load failures must surface and fail closed.
func TestR3_F2_MissingDocumentRegistryKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, nil)
	_ = os.Remove(filepath.Join(dir, "docs/DOCUMENT-REGISTRY.yaml"))
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeDocumentRegistryUnavailable) {
		t.Fatalf("expected %s for missing registry, got %v", CodeDocumentRegistryUnavailable, res.Violations)
	}
}

func TestR3_F2_MalformedDocumentRegistryKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, nil)
	writeDocRegistry(t, dir, "not: [valid: yaml: [[[")
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeDocumentRegistryUnavailable) {
		t.Fatalf("expected %s for malformed registry, got %v", CodeDocumentRegistryUnavailable, res.Violations)
	}
}

func TestR3_F2_EmptyDocumentRegistryKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, nil)
	writeDocRegistry(t, dir, `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents: []
`)
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeDocumentRegistryUnavailable) {
		t.Fatalf("expected %s for empty registry, got %v", CodeDocumentRegistryUnavailable, res.Violations)
	}
}

func TestR3_F2_UnreadableDocumentRegistryKills(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, nil)
	path := filepath.Join(dir, "docs/DOCUMENT-REGISTRY.yaml")
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0600) })
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeDocumentRegistryUnavailable) {
		t.Fatalf("expected %s for unreadable registry, got %v", CodeDocumentRegistryUnavailable, res.Violations)
	}
}

func TestR3_F2_AuthoritativeReferenceFailsClosedWhenRegistryUnavailable(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		reg.ActionMapReferenceClassifications = []ActionMapReferenceClassification{{
			Path: "docs/ARCHITECTURE.md", Line: 1, Class: "AUTHORITATIVE_REFERENCE", Note: "test",
		}}
	})
	_ = os.Remove(filepath.Join(dir, "docs/DOCUMENT-REGISTRY.yaml"))
	mustWrite(t, filepath.Join(dir, "docs/ARCHITECTURE.md"), "<!-- fixture-component -->\nAction Map is required.\n")
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeDocumentRegistryUnavailable) {
		t.Fatalf("expected %s, got %v", CodeDocumentRegistryUnavailable, res.Violations)
	}
	if !hasCode(res.Violations, CodeAuthoritativeActionMap) {
		t.Fatalf("expected %s fail-closed, got %v", CodeAuthoritativeActionMap, res.Violations)
	}
}

func TestR3_F2_AuthoritativeReferenceFailsClosedWhenActionMapRowMissing(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		reg.ActionMapReferenceClassifications = []ActionMapReferenceClassification{{
			Path: "docs/ARCHITECTURE.md", Line: 1, Class: "AUTHORITATIVE_REFERENCE", Note: "test",
		}}
	})
	// Registry loads but has no ACTION-MAP.yaml row.
	writeDocRegistry(t, dir, docRegistryYAML("fixture-doc", "accepted"))
	mustWrite(t, filepath.Join(dir, "docs/ARCHITECTURE.md"), "<!-- fixture-component -->\nAction Map is required.\n")
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeAuthoritativeActionMap) {
		t.Fatalf("expected %s when action-map row missing, got %v", CodeAuthoritativeActionMap, res.Violations)
	}
}

// R4-F1: ACTION-MAP row status must be recognized and not non-authoritative.
func TestR4_F1_ActionMapStatusMissingFails(t *testing.T) {
	assertR4F1ActionMapStatus(t, "", CodeActionMapStatusInvalid, true)
}

func TestR4_F1_ActionMapStatusWrongTypeFails(t *testing.T) {
	dir := t.TempDir()
	writeAuthoritativeActionMapFixture(t, dir)
	writeDocRegistry(t, dir, `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: action-map-yaml
    path: docs/operations/ACTION-MAP.yaml
    status: true
`)
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeActionMapStatusInvalid) {
		t.Fatalf("expected %s for wrong-type status, got %v", CodeActionMapStatusInvalid, res.Violations)
	}
}

func TestR4_F1_ActionMapStatusUnknownACCEPTEDFails(t *testing.T) {
	assertR4F1ActionMapStatus(t, "ACCEPTED", CodeActionMapStatusInvalid, false)
}

func TestR4_F1_ActionMapStatusUnknownApprovedFails(t *testing.T) {
	assertR4F1ActionMapStatus(t, "approved", CodeActionMapStatusInvalid, false)
}

func TestR4_F1_ActionMapStatusHistoricalFails(t *testing.T) {
	assertR4F1ActionMapStatus(t, "historical", CodeAuthoritativeActionMap, false)
}

func TestR4_F1_ActionMapStatusSupersededFails(t *testing.T) {
	assertR4F1ActionMapStatus(t, "superseded", CodeAuthoritativeActionMap, false)
}

func TestR4_F1_ActionMapStatusProposedFails(t *testing.T) {
	assertR4F1ActionMapStatus(t, "proposed", CodeAuthoritativeActionMap, false)
}

func TestR5_F1_ActionMapStatusLegacyFails(t *testing.T) {
	assertR4F1ActionMapStatus(t, "legacy", CodeAuthoritativeActionMap, false)
}

func TestR6_F1_ActionMapStatusActiveFails(t *testing.T) {
	dir := t.TempDir()
	writeAuthoritativeActionMapFixture(t, dir)
	mustMkdir(t, filepath.Join(dir, "docs/operations"))
	mustWrite(t, filepath.Join(dir, "docs/operations/ACTION-MAP.yaml"), "apiVersion: v1\n")
	writeDocRegistry(t, dir, actionMapRegistryYAML("active", false, "docs/operations/ACTION-MAP.yaml"))
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeAuthoritativeActionMap) {
		t.Fatalf("expected %s for active, got %v", CodeAuthoritativeActionMap, res.Violations)
	}
}

func TestR6_F1_ActionMapAcceptedOptionalMissingFails(t *testing.T) {
	assertR6Publication(t, true, "docs/operations/ACTION-MAP.yaml", false, false, CodeActionMapNotPublished)
}

func TestR6_F1_ActionMapAcceptedRequiredMissingFails(t *testing.T) {
	assertR6Publication(t, false, "docs/operations/ACTION-MAP.yaml", false, false, CodeActionMapNotPublished)
}

func TestR7_F2_ActionMapAcceptedEmptyPathFailsAtCheck(t *testing.T) {
	assertActionMapNotPublishedReason(t, func(dir string) {
		writeDocRegistry(t, dir, `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: action-map-yaml
    path: ""
    status: accepted
`)
	}, "EMPTY_PATH")
}

func TestR6_F1_ActionMapAcceptedDirectoryFails(t *testing.T) {
	assertActionMapNotPublishedReason(t, func(dir string) {
		mustMkdir(t, filepath.Join(dir, "docs/operations/ACTION-MAP.yaml"))
		writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", false, "docs/operations/ACTION-MAP.yaml"))
	}, "NOT_REGULAR_FILE")
}

func TestR7_F2_ActionMapLexicalEscapeExistingOutsideFails(t *testing.T) {
	assertActionMapNotPublishedReason(t, func(dir string) {
		outside := filepath.Join(filepath.Dir(dir), "outside-action-map-"+filepath.Base(dir)+".yaml")
		mustWrite(t, outside, "outside\n")
		t.Cleanup(func() { _ = os.Remove(outside) })
		writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", false, "../"+filepath.Base(outside)))
	}, "PATH_OUTSIDE_REPOSITORY")
}

func TestR7_F2_ActionMapAbsoluteOutsideExistingFails(t *testing.T) {
	assertActionMapNotPublishedReason(t, func(dir string) {
		outside := filepath.Join(t.TempDir(), "abs-outside.yaml")
		mustWrite(t, outside, "outside\n")
		writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", false, outside))
	}, "PATH_OUTSIDE_REPOSITORY")
}

func TestR7_F1_ActionMapSymlinkEscapeExistingOutsideFails(t *testing.T) {
	assertActionMapNotPublishedReason(t, func(dir string) {
		outside := filepath.Join(filepath.Dir(dir), "symlink-outside-"+filepath.Base(dir)+".yaml")
		mustWrite(t, outside, "outside\n")
		t.Cleanup(func() { _ = os.Remove(outside) })
		mustMkdir(t, filepath.Join(dir, "docs/operations"))
		link := filepath.Join(dir, "docs/operations/ACTION-MAP.yaml")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", false, "docs/operations/ACTION-MAP.yaml"))
	}, "SYMLINK_TARGET_OUTSIDE_REPOSITORY")
}

func TestA97_R1_DanglingSymlinkToMissingTargetFails(t *testing.T) {
	assertActionMapNotPublishedReason(t, func(dir string) {
		mustMkdir(t, filepath.Join(dir, "docs/operations"))
		link := filepath.Join(dir, "docs/operations/ACTION-MAP.yaml")
		if err := os.Symlink("missing-target.yaml", link); err != nil {
			t.Fatal(err)
		}
		writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", false, "docs/operations/ACTION-MAP.yaml"))
	}, "FILE_MISSING")
}

func TestA97_R2_DanglingSymlinkOutsideFails(t *testing.T) {
	assertActionMapNotPublishedReason(t, func(dir string) {
		mustMkdir(t, filepath.Join(dir, "docs/operations"))
		link := filepath.Join(dir, "docs/operations/ACTION-MAP.yaml")
		if err := os.Symlink("/tmp/coord-a97-r2-missing-outside.yaml", link); err != nil {
			t.Fatal(err)
		}
		writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", false, "docs/operations/ACTION-MAP.yaml"))
	}, "SYMLINK_TARGET_OUTSIDE_REPOSITORY")
}

func TestA97_R2_DanglingRelativeEscapeFails(t *testing.T) {
	// From docs/operations, ../../ is the repo root (still inside). Three hops
	// (../../../) leave the repository — that is the D3 relative-escape branch.
	assertActionMapNotPublishedReason(t, func(dir string) {
		mustMkdir(t, filepath.Join(dir, "docs/operations"))
		link := filepath.Join(dir, "docs/operations/ACTION-MAP.yaml")
		if err := os.Symlink("../../../outside-missing.yaml", link); err != nil {
			t.Fatal(err)
		}
		writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", false, "docs/operations/ACTION-MAP.yaml"))
	}, "SYMLINK_TARGET_OUTSIDE_REPOSITORY")
}

func TestA97_R2_DanglingAbsoluteInsideMissingFails(t *testing.T) {
	assertActionMapNotPublishedReason(t, func(dir string) {
		mustMkdir(t, filepath.Join(dir, "docs/operations"))
		link := filepath.Join(dir, "docs/operations/ACTION-MAP.yaml")
		abs := filepath.Join(dir, "docs/operations/missing.yaml")
		if err := os.Symlink(abs, link); err != nil {
			t.Fatal(err)
		}
		writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", false, "docs/operations/ACTION-MAP.yaml"))
	}, "FILE_MISSING")
}

func TestR8_M12_HostileGitConfigDoesNotChangePublication(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "hostile-gitconfig")
	mustWrite(t, cfg, "this is not a gitconfig\n")
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_SYSTEM", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
	assertR6Publication(t, false, "docs/operations/ACTION-MAP.yaml", true, true, "")
}

func TestR7_F1_ActionMapUntrackedRegularFileFails(t *testing.T) {
	dir := t.TempDir()
	writeAuthoritativeActionMapFixture(t, dir)
	mustMkdir(t, filepath.Join(dir, "docs/operations"))
	mustWrite(t, filepath.Join(dir, "docs/operations/ACTION-MAP.yaml"), "apiVersion: v1\n")
	writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", false, "docs/operations/ACTION-MAP.yaml"))
	// Init git but do NOT add the ACTION-MAP file.
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "fixture@test")
	runGit(t, dir, "config", "user.name", "fixture")
	runGit(t, dir, "add", "docs/DOCUMENT-REGISTRY.yaml", "docs/ARCHITECTURE.md", "docs/architecture")
	runGit(t, dir, "commit", "-m", "partial")
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeActionMapNotPublished) || !detailHas(res.Violations, CodeActionMapNotPublished, "FILE_NOT_TRACKED") {
		t.Fatalf("expected FILE_NOT_TRACKED, got %v", res.Violations)
	}
}

func TestR8_F1_YAMLIdentityNotHistoricalActionMap(t *testing.T) {
	t.Run("empty_path_on_yaml_row_not_historical_fallback", func(t *testing.T) {
		dir := t.TempDir()
		writeAuthoritativeActionMapFixture(t, dir)
		mustMkdir(t, filepath.Join(dir, "docs/operations"))
		mustWrite(t, filepath.Join(dir, "docs/operations/ACTION-MAP.md"), "# historical\n")
		writeDocRegistry(t, dir, `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: action-map
    path: docs/operations/ACTION-MAP.md
    status: accepted
  - id: action-map-yaml
    path: ""
    status: accepted
`)
		initFixtureGit(t, dir)
		res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
		if err != nil {
			t.Fatal(err)
		}
		if !hasCode(res.Violations, CodeActionMapNotPublished) || !detailHas(res.Violations, CodeActionMapNotPublished, "EMPTY_PATH") {
			t.Fatalf("expected EMPTY_PATH on action-map-yaml, not historical fallback, got %v", res.Violations)
		}
	})
	t.Run("historical_accepted_committed_yaml_row_absent", func(t *testing.T) {
		dir := t.TempDir()
		writeAuthoritativeActionMapFixture(t, dir)
		mustMkdir(t, filepath.Join(dir, "docs/operations"))
		mustWrite(t, filepath.Join(dir, "docs/operations/ACTION-MAP.md"), "# historical\n")
		writeDocRegistry(t, dir, `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: action-map
    path: docs/operations/ACTION-MAP.md
    status: accepted
`)
		initFixtureGit(t, dir)
		res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
		if err != nil {
			t.Fatal(err)
		}
		if !hasCode(res.Violations, CodeAuthoritativeActionMap) {
			t.Fatalf("expected row-missing %s, got %v", CodeAuthoritativeActionMap, res.Violations)
		}
		if hasCode(res.Violations, CodeActionMapNotPublished) {
			t.Fatalf("historical action-map must not be published as YAML row, got %v", res.Violations)
		}
	})
	t.Run("both_rows_yaml_validated_regardless_of_order", func(t *testing.T) {
		for _, yamlFirst := range []bool{true, false} {
			dir := t.TempDir()
			writeAuthoritativeActionMapFixture(t, dir)
			mustMkdir(t, filepath.Join(dir, "docs/operations"))
			mustWrite(t, filepath.Join(dir, "docs/operations/ACTION-MAP.yaml"), "apiVersion: v1\nkinds: []\n")
			mustWrite(t, filepath.Join(dir, "docs/operations/ACTION-MAP.md"), "# historical\n")
			hist := `  - id: action-map
    path: docs/operations/ACTION-MAP.md
    status: accepted
`
			yamlRow := `  - id: action-map-yaml
    path: docs/operations/ACTION-MAP.yaml
    status: accepted
`
			body := hist + yamlRow
			if yamlFirst {
				body = yamlRow + hist
			}
			writeDocRegistry(t, dir, "apiVersion: coord.dev/v1alpha1\nkind: DocumentRegistry\ndocuments:\n"+body)
			initFixtureGit(t, dir)
			res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
			if err != nil {
				t.Fatal(err)
			}
			for _, code := range []string{CodeAuthoritativeActionMap, CodeActionMapStatusInvalid, CodeActionMapNotPublished} {
				if hasCode(res.Violations, code) {
					t.Fatalf("yamlFirst=%v: expected pass, got %v", yamlFirst, res.Violations)
				}
			}
			sha, ok := gitHeadBlobSHA(dir, "docs/operations/ACTION-MAP.yaml")
			if !ok || sha == "" {
				t.Fatal("expected HEAD blob SHA for published yaml")
			}
		}
	})
	t.Run("positive_valid_yaml_row", func(t *testing.T) {
		assertR6Publication(t, false, "docs/operations/ACTION-MAP.yaml", true, true, "")
	})
}

func TestR8_F2_PublicationIsHEADTreeNotIndex(t *testing.T) {
	t.Run("staged_only_file_not_tracked", func(t *testing.T) {
		dir := t.TempDir()
		writeAuthoritativeActionMapFixture(t, dir)
		writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", false, "docs/operations/ACTION-MAP.yaml"))
		initFixtureGit(t, dir)
		mustMkdir(t, filepath.Join(dir, "docs/operations"))
		mustWrite(t, filepath.Join(dir, "docs/operations/ACTION-MAP.yaml"), "apiVersion: v1\nkinds: []\n")
		runGit(t, dir, "add", "docs/operations/ACTION-MAP.yaml")
		res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
		if err != nil {
			t.Fatal(err)
		}
		if !hasCode(res.Violations, CodeActionMapNotPublished) || !detailHas(res.Violations, CodeActionMapNotPublished, "FILE_NOT_TRACKED") {
			t.Fatalf("expected FILE_NOT_TRACKED for staged-only file, got %v", res.Violations)
		}
		if gitTrackedPath(dir, "docs/operations/ACTION-MAP.yaml") {
			t.Fatal("index membership must not count as HEAD publication")
		}
	})
	t.Run("untracked_file_not_tracked", func(t *testing.T) {
		dir := t.TempDir()
		writeAuthoritativeActionMapFixture(t, dir)
		writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", false, "docs/operations/ACTION-MAP.yaml"))
		initFixtureGit(t, dir)
		mustMkdir(t, filepath.Join(dir, "docs/operations"))
		mustWrite(t, filepath.Join(dir, "docs/operations/ACTION-MAP.yaml"), "apiVersion: v1\nkinds: []\n")
		res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
		if err != nil {
			t.Fatal(err)
		}
		if !hasCode(res.Violations, CodeActionMapNotPublished) || !detailHas(res.Violations, CodeActionMapNotPublished, "FILE_NOT_TRACKED") {
			t.Fatalf("expected FILE_NOT_TRACKED for untracked file, got %v", res.Violations)
		}
	})
	t.Run("in_head_deleted_from_worktree", func(t *testing.T) {
		dir := t.TempDir()
		writeAuthoritativeActionMapFixture(t, dir)
		mustMkdir(t, filepath.Join(dir, "docs/operations"))
		mustWrite(t, filepath.Join(dir, "docs/operations/ACTION-MAP.yaml"), "apiVersion: v1\nkinds: []\n")
		writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", false, "docs/operations/ACTION-MAP.yaml"))
		initFixtureGit(t, dir)
		if err := os.Remove(filepath.Join(dir, "docs/operations/ACTION-MAP.yaml")); err != nil {
			t.Fatal(err)
		}
		res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
		if err != nil {
			t.Fatal(err)
		}
		if !hasCode(res.Violations, CodeActionMapNotPublished) || !detailHas(res.Violations, CodeActionMapNotPublished, "FILE_MISSING") {
			t.Fatalf("expected FILE_MISSING when HEAD blob exists but worktree file is gone, got %v", res.Violations)
		}
	})
	t.Run("committed_symlink_to_untracked_target", func(t *testing.T) {
		dir := t.TempDir()
		writeAuthoritativeActionMapFixture(t, dir)
		mustMkdir(t, filepath.Join(dir, "docs/operations"))
		mustWrite(t, filepath.Join(dir, "docs/operations/real.yaml"), "apiVersion: v1\nkinds: []\n")
		link := filepath.Join(dir, "docs/operations/ACTION-MAP.yaml")
		if err := os.Symlink("real.yaml", link); err != nil {
			t.Fatal(err)
		}
		writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", false, "docs/operations/ACTION-MAP.yaml"))
		initFixtureGit(t, dir)
		runGit(t, dir, "rm", "--cached", "docs/operations/real.yaml")
		runGit(t, dir, "commit", "-m", "drop target from HEAD")
		res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
		if err != nil {
			t.Fatal(err)
		}
		if !hasCode(res.Violations, CodeActionMapNotPublished) || !detailHas(res.Violations, CodeActionMapNotPublished, "FILE_NOT_TRACKED") {
			t.Fatalf("expected FILE_NOT_TRACKED for committed symlink to untracked target, got %v", res.Violations)
		}
	})
	t.Run("uncommitted_symlink_to_committed_target", func(t *testing.T) {
		dir := t.TempDir()
		writeAuthoritativeActionMapFixture(t, dir)
		mustMkdir(t, filepath.Join(dir, "docs/operations"))
		mustWrite(t, filepath.Join(dir, "docs/operations/real.yaml"), "apiVersion: v1\nkinds: []\n")
		writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", false, "docs/operations/ACTION-MAP.yaml"))
		initFixtureGit(t, dir)
		link := filepath.Join(dir, "docs/operations/ACTION-MAP.yaml")
		if err := os.Symlink("real.yaml", link); err != nil {
			t.Fatal(err)
		}
		res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
		if err != nil {
			t.Fatal(err)
		}
		if !hasCode(res.Violations, CodeActionMapNotPublished) || !detailHas(res.Violations, CodeActionMapNotPublished, "FILE_NOT_TRACKED") {
			t.Fatalf("expected FILE_NOT_TRACKED for uncommitted symlink to committed target, got %v", res.Violations)
		}
	})
	t.Run("head_tree_replaced_by_symlink_to_committed_file", func(t *testing.T) {
		dir := t.TempDir()
		writeAuthoritativeActionMapFixture(t, dir)
		mustMkdir(t, filepath.Join(dir, "docs/operations/ACTION-MAP.yaml"))
		mustWrite(t, filepath.Join(dir, "docs/operations/ACTION-MAP.yaml/inside.txt"), "nested\n")
		mustWrite(t, filepath.Join(dir, "docs/operations/real.yaml"), "apiVersion: v1\nkinds: []\n")
		writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", false, "docs/operations/ACTION-MAP.yaml"))
		initFixtureGit(t, dir)
		if err := os.RemoveAll(filepath.Join(dir, "docs/operations/ACTION-MAP.yaml")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("real.yaml", filepath.Join(dir, "docs/operations/ACTION-MAP.yaml")); err != nil {
			t.Fatal(err)
		}
		res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
		if err != nil {
			t.Fatal(err)
		}
		if !hasCode(res.Violations, CodeActionMapNotPublished) || !detailHas(res.Violations, CodeActionMapNotPublished, "FILE_NOT_TRACKED") {
			t.Fatalf("expected FILE_NOT_TRACKED when HEAD registry path is a tree and worktree is a symlink to a committed file, got %v", res.Violations)
		}
	})
	t.Run("head_symlink_replaced_by_uncommitted_regular_file", func(t *testing.T) {
		dir := t.TempDir()
		writeAuthoritativeActionMapFixture(t, dir)
		mustMkdir(t, filepath.Join(dir, "docs/operations"))
		mustWrite(t, filepath.Join(dir, "docs/operations/real.yaml"), "apiVersion: v1\nkinds: []\n")
		link := filepath.Join(dir, "docs/operations/ACTION-MAP.yaml")
		if err := os.Symlink("real.yaml", link); err != nil {
			t.Fatal(err)
		}
		writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", false, "docs/operations/ACTION-MAP.yaml"))
		initFixtureGit(t, dir)
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, link, "apiVersion: v1\nkinds: []\n")
		res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
		if err != nil {
			t.Fatal(err)
		}
		if !hasCode(res.Violations, CodeActionMapNotPublished) || !detailHas(res.Violations, CodeActionMapNotPublished, "FILE_NOT_TRACKED") {
			t.Fatalf("expected FILE_NOT_TRACKED when HEAD records a symlink (120000) and worktree is a regular file, got %v", res.Violations)
		}
	})
	t.Run("committed_symlink_to_committed_target", func(t *testing.T) {
		dir := t.TempDir()
		writeAuthoritativeActionMapFixture(t, dir)
		mustMkdir(t, filepath.Join(dir, "docs/operations"))
		mustWrite(t, filepath.Join(dir, "docs/operations/real.yaml"), "apiVersion: v1\nkinds: []\n")
		link := filepath.Join(dir, "docs/operations/ACTION-MAP.yaml")
		if err := os.Symlink("real.yaml", link); err != nil {
			t.Fatal(err)
		}
		writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", false, "docs/operations/ACTION-MAP.yaml"))
		initFixtureGit(t, dir)
		res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
		if err != nil {
			t.Fatal(err)
		}
		for _, code := range []string{CodeAuthoritativeActionMap, CodeActionMapStatusInvalid, CodeActionMapNotPublished} {
			if hasCode(res.Violations, code) {
				t.Fatalf("committed symlink to committed target must pass, got %v", res.Violations)
			}
		}
		sha, ok := gitHeadBlobSHA(dir, "docs/operations/real.yaml")
		if !ok || len(sha) != 40 {
			t.Fatalf("expected HEAD blob SHA of resolved target, got %q ok=%v", sha, ok)
		}
	})
	t.Run("positive_committed_blob_on_disk", func(t *testing.T) {
		dir := t.TempDir()
		writeAuthoritativeActionMapFixture(t, dir)
		mustMkdir(t, filepath.Join(dir, "docs/operations"))
		mustWrite(t, filepath.Join(dir, "docs/operations/ACTION-MAP.yaml"), "apiVersion: v1\nkinds: []\n")
		writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", false, "docs/operations/ACTION-MAP.yaml"))
		initFixtureGit(t, dir)
		res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
		if err != nil {
			t.Fatal(err)
		}
		for _, code := range []string{CodeAuthoritativeActionMap, CodeActionMapStatusInvalid, CodeActionMapNotPublished} {
			if hasCode(res.Violations, code) {
				t.Fatalf("committed HEAD blob + on-disk file must pass, got %v", res.Violations)
			}
		}
		sha, ok := gitHeadBlobSHA(dir, "docs/operations/ACTION-MAP.yaml")
		if !ok || len(sha) != 40 {
			t.Fatalf("expected 40-char HEAD blob SHA, got %q ok=%v", sha, ok)
		}
		cmd := exec.Command("git", "-C", dir, "rev-parse", "--verify", "HEAD:docs/operations/ACTION-MAP.yaml")
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(out)) != sha {
			t.Fatalf("published SHA %s != git rev-parse %s", sha, strings.TrimSpace(string(out)))
		}
	})
}

func TestR6_F1_ActionMapAcceptedOtherFileDoesNotSubstitute(t *testing.T) {
	assertActionMapNotPublishedReason(t, func(dir string) {
		mustWrite(t, filepath.Join(dir, "docs/elsewhere.yaml"), "other\n")
		writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", false, "docs/operations/ACTION-MAP.yaml"))
	}, "FILE_MISSING")
}

func TestA97_3_ActionMapAcceptedTrackedInOtherBranchFails(t *testing.T) {
	assertActionMapNotPublishedReason(t, func(dir string) {
		mustMkdir(t, filepath.Join(dir, "docs/operations"))
		mustWrite(t, filepath.Join(dir, "docs/operations/ACTION-MAP.yaml"), "apiVersion: v1\n")
		writeDocRegistry(t, dir, actionMapRegistryYAMLLoc("accepted", false, "docs/operations/ACTION-MAP.yaml", "TRACKED_IN_OTHER_BRANCH"))
	}, "NON_LOCAL_LOCATION")
}

func TestA97_1_ProductionAuthStatusACCEPTEDFails(t *testing.T) {
	assertR3F1AuthFailure(t, "ACCEPTED", true, true, "product", CodeProdAuthWrongStatus)
}

func assertActionMapNotPublishedReason(t *testing.T, setup func(dir string), reason string) {
	t.Helper()
	dir := t.TempDir()
	writeAuthoritativeActionMapFixture(t, dir)
	setup(dir)
	initFixtureGit(t, dir)
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeActionMapNotPublished) || !detailHas(res.Violations, CodeActionMapNotPublished, reason) {
		t.Fatalf("expected %s with reason %s, got %v", CodeActionMapNotPublished, reason, res.Violations)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@test",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@test",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func detailHas(vs []Violation, code, marker string) bool {
	for _, v := range vs {
		if v.Code == code && strings.Contains(v.Detail, marker) {
			return true
		}
	}
	return false
}

func TestR6_F1_ActionMapAcceptedOptionalPresentPasses(t *testing.T) {
	assertR6Publication(t, true, "docs/operations/ACTION-MAP.yaml", true, true, "")
}

func TestR6_F1_ActionMapAcceptedRequiredPresentPasses(t *testing.T) {
	assertR6Publication(t, false, "docs/operations/ACTION-MAP.yaml", true, true, "")
}

func TestR6_F1_ActionMapLegacyWithFileFails(t *testing.T) {
	dir := t.TempDir()
	writeAuthoritativeActionMapFixture(t, dir)
	mustMkdir(t, filepath.Join(dir, "docs/operations"))
	mustWrite(t, filepath.Join(dir, "docs/operations/ACTION-MAP.yaml"), "apiVersion: v1\n")
	writeDocRegistry(t, dir, actionMapRegistryYAML("legacy", false, "docs/operations/ACTION-MAP.yaml"))
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, CodeAuthoritativeActionMap) {
		t.Fatalf("expected %s for legacy, got %v", CodeAuthoritativeActionMap, res.Violations)
	}
}

func TestR4_F1_ActionMapStatusAcceptedPasses(t *testing.T) {
	assertR6Publication(t, false, "docs/operations/ACTION-MAP.yaml", true, true, "")
}

func actionMapRegistryYAML(status string, optional bool, path string) string {
	return actionMapRegistryYAMLLoc(status, optional, path, "")
}

func actionMapRegistryYAMLLoc(status string, optional bool, path, locationClass string) string {
	loc := ""
	if locationClass != "" {
		loc = fmt.Sprintf("\n    location:\n      class: %s", locationClass)
	}
	return fmt.Sprintf(`apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: action-map-yaml
    path: %q
    status: %s
    optional: %t%s
`, path, status, optional, loc)
}

func initFixtureGit(t *testing.T, dir string, track ...string) {
	t.Helper()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "fixture@test")
	runGit(t, dir, "config", "user.name", "fixture")
	for _, p := range track {
		runGit(t, dir, "add", "-f", p)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "fixture", "--allow-empty")
}

func assertR6Publication(t *testing.T, optional bool, path string, writeFile, wantPass bool, wantCode string) {
	t.Helper()
	dir := t.TempDir()
	writeAuthoritativeActionMapFixture(t, dir)
	if writeFile {
		mustMkdir(t, filepath.Join(dir, filepath.Dir(path)))
		mustWrite(t, filepath.Join(dir, path), "apiVersion: v1\nkinds: []\n")
	}
	writeDocRegistry(t, dir, actionMapRegistryYAML("accepted", optional, path))
	initFixtureGit(t, dir)
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if wantPass {
		for _, code := range []string{CodeAuthoritativeActionMap, CodeActionMapStatusInvalid, CodeActionMapNotPublished} {
			if hasCode(res.Violations, code) {
				t.Fatalf("accepted+published must pass, got %v", res.Violations)
			}
		}
		return
	}
	if !hasCode(res.Violations, wantCode) {
		t.Fatalf("expected %s, got %v", wantCode, res.Violations)
	}
}

func assertR4F1ActionMapStatus(t *testing.T, status, wantCode string, omitStatus bool) {
	t.Helper()
	dir := t.TempDir()
	writeAuthoritativeActionMapFixture(t, dir)
	var yaml string
	if omitStatus {
		yaml = `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: action-map-yaml
    path: docs/operations/ACTION-MAP.yaml
`
	} else {
		yaml = fmt.Sprintf(`apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: action-map-yaml
    path: docs/operations/ACTION-MAP.yaml
    status: %s
`, status)
	}
	writeDocRegistry(t, dir, yaml)
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, wantCode) {
		t.Fatalf("expected %s for status=%q omit=%v, got %v", wantCode, status, omitStatus, res.Violations)
	}
}

func writeAuthoritativeActionMapFixture(t *testing.T, dir string) {
	t.Helper()
	writeMinimalFixture(t, dir, func(reg *Registry) {
		reg.ActionMapReferenceClassifications = []ActionMapReferenceClassification{{
			Path: "docs/ARCHITECTURE.md", Line: 1, Class: "AUTHORITATIVE_REFERENCE", Note: "test",
		}}
	})
	mustWrite(t, filepath.Join(dir, "docs/ARCHITECTURE.md"), "<!-- fixture-component -->\nAction Map is required.\n")
}

func TestMakefileAndWorkflowUseGoBlocking(t *testing.T) {
	root := repoRoot(t)
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	mk := string(makefile)
	if !strings.Contains(mk, "check-architecture-sync:") {
		t.Fatal("Makefile missing check-architecture-sync target")
	}
	// Extract the target body until the next target.
	body := targetBody(mk, "check-architecture-sync:")
	if strings.Contains(body, "python3 scripts/check-architecture-sync") && !strings.Contains(body, "coord-governance check-architecture-sync") {
		t.Fatal("Makefile still uses Python as the blocking architecture check without Go")
	}
	if !strings.Contains(body, "coord-governance check-architecture-sync") && !strings.Contains(body, "./internal/architecturecheck") {
		// Accept either CLI or go test of the package as the blocking path.
		if !strings.Contains(body, "go run ./cmd/coord-governance check-architecture-sync") {
			t.Fatalf("Makefile check-architecture-sync is not Go-blocking:\n%s", body)
		}
	}
	wf, err := os.ReadFile(filepath.Join(root, ".github/workflows/architecture-sync.yml"))
	if err != nil {
		t.Fatal(err)
	}
	wfs := string(wf)
	if !strings.Contains(wfs, "check-architecture-sync") && !strings.Contains(wfs, "architecturecheck") {
		t.Fatal("architecture-sync.yml must invoke the Go checker")
	}
	// Python may remain as evidence-only; it must not be the sole validate step.
	if strings.Contains(wfs, "python scripts/check-architecture-sync") && !strings.Contains(wfs, "coord-governance") && !strings.Contains(wfs, "go test") {
		t.Fatal("architecture-sync.yml still blocks only on Python")
	}
}

func TestASTIndexFindsTypeFuncMethodAndTest(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "pkg.go"), "package p\n type Foo struct{}\n func Bar(){}\n func (Foo) Baz(){}\n")
	mustWrite(t, filepath.Join(dir, "pkg_test.go"), "package p\n func TestFoo(t *testing.T){}\n")
	idx, err := BuildCodeIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !idx.HasSymbol("Foo") || !idx.HasSymbol("Bar") || !idx.HasSymbol("Baz") {
		t.Fatalf("symbols missing: %#v", idx.AllNames)
	}
	if !idx.HasTest("TestFoo") {
		t.Fatalf("test missing: %#v", idx.Tests)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// internal/architecturecheck -> repo root
	return filepath.Clean(filepath.Join(wd, "../.."))
}

func setAxes(c *Component, code, connection, qualification, production string) {
	c.Code = code
	c.Connection = connection
	c.Qualification = qualification
	c.Production = production
	c.Evidence.Symbols = []string{"FixtureSymbol"}
	c.Evidence.Tests = []string{"TestFixtureOK"}
}

func docRegistryYAML(id, status string) string {
	return fmt.Sprintf(`apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: %s
    path: docs/%s.md
    status: %s
`, id, id, status)
}

func docRegistryAuthorityYAML(id, status string, cpa, cna bool, normativeAuthority string) string {
	return fmt.Sprintf(`apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: %s
    path: docs/%s.md
    status: %s
    canonicalProductionAuthority: %t
    currentNormativeAuthority: %t
    normativeAuthority: %s
`, id, id, status, cpa, cna, normativeAuthority)
}

func writeDocRegistry(t *testing.T, dir, yaml string) {
	t.Helper()
	mustMkdir(t, filepath.Join(dir, "docs"))
	mustWrite(t, filepath.Join(dir, "docs/DOCUMENT-REGISTRY.yaml"), yaml)
}

func writeMinimalFixture(t *testing.T, dir string, mutate func(*Registry)) {
	t.Helper()
	falseVal := false
	reg := Registry{
		APIVersion: "coord.dev/v1alpha1",
		Kind:       "ArchitectureComponentRegistry",
		Metadata: Metadata{
			ProjectID:         "coord",
			RegistryVersion:   1,
			UpdatedFromCommit: "fixture",
			StatusVocabulary:  []string{"implemented", "partial", "missing", "proposed", "legacy"},
		},
		Documents: Documents{
			CurrentArchitecture: "docs/ARCHITECTURE.md",
			TargetArchitecture:  "docs/TARGET-ARCHITECTURE.md",
		},
		Components: []Component{{
			ID:                         "fixture-component",
			Title:                      "Fixture",
			Status:                     "partial",
			NormativeForCurrentProduct: &falseVal,
			NormativeSources:           []string{"docs/ADR-FIXTURE.md"},
			Implemented:                []string{"something"},
			Missing:                    []string{"else"},
			Evidence: Evidence{
				Symbols: []string{"FixtureSymbol"},
				Tests:   []string{"TestFixtureOK"},
			},
			Documents: ComponentDocs{CurrentAnchor: "fixture-component", TargetAnchor: "fixture-component"},
		}},
	}
	if mutate != nil {
		mutate(&reg)
	}
	mustMkdir(t, filepath.Join(dir, "docs/architecture"))
	writeYAML(t, filepath.Join(dir, "docs/architecture/COMPONENT-STATUS.yaml"), reg)
	mustWrite(t, filepath.Join(dir, "docs/ARCHITECTURE.md"), "<!-- fixture-component -->\n")
	mustWrite(t, filepath.Join(dir, "docs/TARGET-ARCHITECTURE.md"), "<!-- fixture-component -->\n")
	mustWrite(t, filepath.Join(dir, "docs/ADR-FIXTURE.md"), "fixture\n")
	mustWrite(t, filepath.Join(dir, "fixture.go"), "package fixture\n func FixtureSymbol(){}\n")
	mustWrite(t, filepath.Join(dir, "fixture_test.go"), "package fixture\n func TestFixtureOK(t interface{}){}\n")
	// Default registry so R3-F2 fail-closed does not trip every fixture.
	writeDocRegistry(t, dir, `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: fixture-doc
    path: docs/ADR-FIXTURE.md
    status: accepted
`)
}

func writeYAML(t *testing.T, path string, v any) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	enc := newYAMLEncoder(f)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
}

func loadGoldenFindings(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var findings []struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &findings); err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Code)
	}
	return out
}

func codes(vs []Violation) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.Code)
	}
	return out
}

func sameCodes(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	// Multiset compare.
	counts := map[string]int{}
	for _, c := range want {
		counts[c]++
	}
	for _, c := range got {
		counts[c]--
		if counts[c] < 0 {
			return false
		}
	}
	for _, n := range counts {
		if n != 0 {
			return false
		}
	}
	return true
}

func hasCode(vs []Violation, code string) bool {
	for _, v := range vs {
		if v.Code == code {
			return true
		}
	}
	return false
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
}

func targetBody(makefile, target string) string {
	idx := strings.Index(makefile, target)
	if idx < 0 {
		return ""
	}
	rest := makefile[idx+len(target):]
	lines := strings.Split(rest, "\n")
	var body []string
	for i, line := range lines {
		if i == 0 && strings.TrimSpace(line) == "" {
			continue
		}
		if i > 0 && len(line) > 0 && line[0] != '\t' && line[0] != ' ' && strings.Contains(line, ":") && !strings.HasPrefix(line, "#") {
			break
		}
		body = append(body, line)
	}
	return strings.Join(body, "\n")
}
