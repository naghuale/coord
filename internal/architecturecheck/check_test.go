package architecturecheck

import (
	"encoding/json"
	"fmt"
	"os"
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
	if !hasCode(res.Violations, CodeProposedProductionAuth) {
		t.Fatalf("expected %s, got %v", CodeProposedProductionAuth, res.Violations)
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
	if !hasCode(res.Violations, CodeProposedProductionAuth) {
		t.Fatalf("expected %s, got %v", CodeProposedProductionAuth, res.Violations)
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
documents: []
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
			if !hasCode(res.Violations, CodeProposedProductionAuth) {
				t.Fatalf("expected %s for status=%s, got %v", CodeProposedProductionAuth, status, res.Violations)
			}
		})
	}
}

// F97-1 positive control: accepted source + production claim -> PASS (no CodeProposedProductionAuth).
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
				NormativeStatus:  "PROPOSED", // row cannot lower either for this check; registry accepted wins
			},
		}
	})
	writeDocRegistry(t, dir, docRegistryYAML("src-accepted", "accepted"))
	res, err := Check(Options{Root: dir, SkipChangedDocs: true, DocRegistryPath: "docs/DOCUMENT-REGISTRY.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if hasCode(res.Violations, CodeProposedProductionAuth) {
		t.Fatalf("accepted source must allow production claim, got %v", res.Violations)
	}
	if hasCode(res.Violations, CodeUnknownRequirementID) {
		t.Fatalf("accepted source must resolve, got %v", res.Violations)
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
