package architecturecheck

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"coord/internal/governanceio"
	"gopkg.in/yaml.v3"
)

// Options configures Check.
type Options struct {
	Root           string
	RegistryPath   string
	DocRegistryPath string
	// SkipChangedDocs skips the architecture-significant path / impact check.
	// Used by golden fixture trees that are not full git repositories.
	SkipChangedDocs bool
	// Index may be supplied by tests; otherwise it is built from Root.
	Index *CodeIndex
}

// Result holds violations from one check run.
type Result struct {
	Violations []Violation
	Components int
}

// Check loads COMPONENT-STATUS.yaml and validates it against the tree at opts.Root.
func Check(opts Options) (*Result, error) {
	if opts.Root == "" {
		return nil, fmt.Errorf("root is required")
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, err
	}
	regPath := opts.RegistryPath
	if regPath == "" {
		regPath = filepath.Join(root, "docs/architecture/COMPONENT-STATUS.yaml")
	}
	if !filepath.IsAbs(regPath) {
		regPath = filepath.Join(root, regPath)
	}

	f, err := os.Open(regPath)
	if err != nil {
		return nil, fmt.Errorf("open architecture registry: %w", err)
	}
	defer func() { _ = f.Close() }()

	reg, err := governanceio.DecodeStrictYAML[Registry](f)
	if err != nil {
		return &Result{Violations: []Violation{{Code: CodeStatusInvalid, Detail: err.Error()}}}, nil
	}

	idx := opts.Index
	if idx == nil {
		idx, err = BuildCodeIndex(root)
		if err != nil {
			return nil, fmt.Errorf("build code index: %w", err)
		}
	}

	docRegStatus := loadActionMapProposed(root, opts.DocRegistryPath)

	var vs []Violation
	vs = append(vs, validateDocuments(root, reg)...)
	vs = append(vs, validateComponents(root, reg, idx)...)
	vs = append(vs, validateAxes(reg)...)
	vs = append(vs, validateRequirements(reg, idx, docRegStatus)...)
	vs = append(vs, validateActionMapRefs(root, reg, docRegStatus)...)
	if !opts.SkipChangedDocs {
		vs = append(vs, validateChangedDocs(root, reg, regPath)...)
	}

	return &Result{Violations: vs, Components: len(reg.Components)}, nil
}

type actionMapDocStatus struct {
	proposed bool
	found    bool
}

func loadActionMapProposed(root, docRegistryPath string) actionMapDocStatus {
	path := docRegistryPath
	if path == "" {
		path = filepath.Join(root, "docs/DOCUMENT-REGISTRY.yaml")
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return actionMapDocStatus{}
	}
	// Lightweight scan: avoid pulling the full documentregistry package cycle.
	var doc struct {
		Documents []struct {
			Path   string `yaml:"path"`
			Status string `yaml:"status"`
		} `yaml:"documents"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return actionMapDocStatus{}
	}
	for _, d := range doc.Documents {
		if d.Path == "docs/operations/ACTION-MAP.yaml" {
			return actionMapDocStatus{proposed: d.Status == "proposed", found: true}
		}
	}
	return actionMapDocStatus{found: false}
}

func validateDocuments(root string, reg Registry) []Violation {
	var vs []Violation
	currentRel := reg.Documents.CurrentArchitecture
	if currentRel == "" {
		currentRel = "docs/ARCHITECTURE.md"
	}
	targetRel := reg.Documents.TargetArchitecture
	if targetRel == "" {
		targetRel = "docs/TARGET-ARCHITECTURE.md"
	}
	currentPath := filepath.Join(root, currentRel)
	targetPath := filepath.Join(root, targetRel)
	if _, err := os.Stat(currentPath); err != nil {
		vs = append(vs, Violation{Code: CodeCurrentMissing, Detail: currentPath})
	}
	if _, err := os.Stat(targetPath); err != nil {
		vs = append(vs, Violation{Code: CodeTargetMissing, Detail: targetPath})
	}
	return vs
}

func validateComponents(root string, reg Registry, idx *CodeIndex) []Violation {
	var vs []Violation
	currentRel := reg.Documents.CurrentArchitecture
	if currentRel == "" {
		currentRel = "docs/ARCHITECTURE.md"
	}
	targetRel := reg.Documents.TargetArchitecture
	if targetRel == "" {
		targetRel = "docs/TARGET-ARCHITECTURE.md"
	}
	currentText := readFile(filepath.Join(root, currentRel))
	targetText := readFile(filepath.Join(root, targetRel))

	seen := map[string]bool{}
	for _, c := range reg.Components {
		cid := c.ID
		if cid == "" || seen[cid] {
			vs = append(vs, Violation{Code: CodeStatusConflict, Detail: fmt.Sprintf("duplicate or empty component id: %q", cid)})
		}
		seen[cid] = true

		if !legacyStatuses[c.Status] {
			vs = append(vs, Violation{Code: CodeStatusInvalid, Detail: fmt.Sprintf("%s: %q", cid, c.Status)})
		}
		if c.Status == "implemented" && (len(c.Missing) > 0 || len(c.Implemented) == 0) {
			vs = append(vs, Violation{Code: CodeMaturityInflated, Detail: fmt.Sprintf("%s: implemented requires evidence and no missing items", cid)})
		}
		if c.Status == "partial" && (len(c.Implemented) == 0 || len(c.Missing) == 0) {
			vs = append(vs, Violation{Code: CodeStatusInvalid, Detail: fmt.Sprintf("%s: partial requires non-empty implemented and missing", cid)})
		}
		if c.Status == "missing" && len(c.Implemented) > 0 {
			vs = append(vs, Violation{Code: CodeStatusConflict, Detail: fmt.Sprintf("%s: missing cannot list implemented items", cid)})
		}
		if c.Status == "proposed" && (c.NormativeForCurrentProduct == nil || *c.NormativeForCurrentProduct) {
			vs = append(vs, Violation{Code: CodeProposedAsImplemented, Detail: fmt.Sprintf("%s: proposed must set normativeForCurrentProduct: false", cid)})
		}
		for _, source := range c.NormativeSources {
			if _, err := os.Stat(filepath.Join(root, source)); err != nil {
				vs = append(vs, Violation{Code: CodeEvidenceMissing, Detail: fmt.Sprintf("%s: missing normative source %s", cid, source)})
			}
		}
		if (c.Status == "implemented" || c.Status == "partial") && len(c.Evidence.Tests) == 0 {
			vs = append(vs, Violation{Code: CodeTestMissing, Detail: fmt.Sprintf("%s: status %s requires tests", cid, c.Status)})
		}
		for _, test := range c.Evidence.Tests {
			if !idx.HasTest(test) {
				vs = append(vs, Violation{Code: CodeTestMissing, Detail: fmt.Sprintf("%s: test not found: %s", cid, test)})
			}
		}
		for _, symbol := range c.Evidence.Symbols {
			if !idx.HasSymbol(symbol) {
				vs = append(vs, Violation{Code: CodeSymbolMissing, Detail: fmt.Sprintf("%s: symbol not found: %s", cid, symbol)})
			}
		}
		if c.Documents.CurrentAnchor != "" && currentText != "" && !anchorExists(currentText, c.Documents.CurrentAnchor) {
			vs = append(vs, Violation{Code: CodeAnchorMissing, Detail: fmt.Sprintf("%s: current anchor %s", cid, c.Documents.CurrentAnchor)})
		}
		if c.Documents.TargetAnchor != "" && targetText != "" && !anchorExists(targetText, c.Documents.TargetAnchor) {
			vs = append(vs, Violation{Code: CodeAnchorMissing, Detail: fmt.Sprintf("%s: target anchor %s", cid, c.Documents.TargetAnchor)})
		}

		// Axis cross-rules when axes are populated.
		if strings.EqualFold(c.Code, "IMPLEMENTED") && len(c.Evidence.Symbols) == 0 {
			vs = append(vs, Violation{Code: CodeImplementedNoSymbol, Detail: fmt.Sprintf("%s: code IMPLEMENTED requires resolved symbols", cid)})
		}
		if c.Connection == "PRODUCTION_CONNECTED" && len(c.ProductionEntrypoints) == 0 {
			vs = append(vs, Violation{Code: CodeConnectedNoEntrypoint, Detail: fmt.Sprintf("%s: PRODUCTION_CONNECTED requires productionEntrypoints", cid)})
		}
		if c.Connection == "PRODUCTION_CONNECTED" {
			for _, ep := range c.ProductionEntrypoints {
				if !idx.HasSymbol(ep) {
					vs = append(vs, Violation{Code: CodeConnectedNoEntrypoint, Detail: fmt.Sprintf("%s: production entrypoint not found: %s", cid, ep)})
				}
			}
		}
		if (c.Production == "ACTIVE" || c.Production == "ACTIVE_WITH_LIMITATIONS") && len(c.Evidence.Symbols) == 0 && len(c.ProductionEntrypoints) == 0 {
			vs = append(vs, Violation{Code: CodeActiveNoRuntime, Detail: fmt.Sprintf("%s: active production requires runtime evidence", cid)})
		}
		qualNeedsTest := c.Qualification == "PASS" || c.Qualification == "ACCEPTED_WITH_LIMITATIONS" ||
			(c.Code == "IMPLEMENTED" && c.Production == "ACTIVE")
		if qualNeedsTest && len(c.Evidence.Tests) == 0 {
			vs = append(vs, Violation{Code: CodeQualifiedNoTest, Detail: fmt.Sprintf("%s: qualified/active implemented component requires tests", cid)})
		}
	}
	return vs
}

func validateAxes(reg Registry) []Violation {
	var vs []Violation
	anyAxis := false
	for _, c := range reg.Components {
		if c.Code != "" || c.Connection != "" || c.Qualification != "" || c.Production != "" {
			anyAxis = true
			break
		}
	}
	if !anyAxis {
		return nil
	}
	for _, c := range reg.Components {
		cid := c.ID
		if c.Code != "" && !codeAxis[c.Code] {
			vs = append(vs, Violation{Code: CodeAxisInvalid, Detail: fmt.Sprintf("%s: code %q", cid, c.Code)})
		}
		if c.Connection != "" && !connectionAxis[c.Connection] {
			vs = append(vs, Violation{Code: CodeAxisInvalid, Detail: fmt.Sprintf("%s: connection %q", cid, c.Connection)})
		}
		if c.Qualification != "" && !qualificationAxis[c.Qualification] {
			vs = append(vs, Violation{Code: CodeAxisInvalid, Detail: fmt.Sprintf("%s: qualification %q", cid, c.Qualification)})
		}
		if c.Production != "" && !productionAxis[c.Production] {
			vs = append(vs, Violation{Code: CodeAxisInvalid, Detail: fmt.Sprintf("%s: production %q", cid, c.Production)})
		}
		// Once any axis is used on the registry, require all four on every component.
		if c.Code == "" || c.Connection == "" || c.Qualification == "" || c.Production == "" {
			vs = append(vs, Violation{Code: CodeAxisInvalid, Detail: fmt.Sprintf("%s: all four axes required when axis migration is active", cid)})
		}
	}
	return vs
}

func validateRequirements(reg Registry, idx *CodeIndex, actionMap actionMapDocStatus) []Violation {
	if len(reg.Requirements) == 0 {
		return nil
	}
	var vs []Violation
	components := map[string]bool{}
	for _, c := range reg.Components {
		components[c.ID] = true
	}
	for id, req := range reg.Requirements {
		if id == "" {
			vs = append(vs, Violation{Code: CodeUnknownRequirementID, Detail: "empty requirement id"})
			continue
		}
		if req.Component != "" && !components[req.Component] {
			vs = append(vs, Violation{Code: CodeUnknownRequirementID, Detail: fmt.Sprintf("%s: unknown component %q", id, req.Component)})
		}
		if req.NormativeStatus == "PROPOSED" {
			// Proposed source cannot authorize production enablement claims.
			for _, c := range reg.Components {
				if c.ID != req.Component {
					continue
				}
				if c.Production == "ACTIVE" || c.Production == "ACTIVE_WITH_LIMITATIONS" {
					vs = append(vs, Violation{Code: CodeProposedProductionAuth, Detail: fmt.Sprintf("%s: PROPOSED requirement cannot authorize production for %s", id, c.ID)})
				}
			}
		}
		for _, sym := range req.ImplementationSymbols {
			if !idx.HasSymbol(sym) {
				vs = append(vs, Violation{Code: CodeSymbolMissing, Detail: fmt.Sprintf("requirement %s: symbol not found: %s", id, sym)})
			}
		}
		for _, test := range req.Tests {
			if !idx.HasTest(test) {
				vs = append(vs, Violation{Code: CodeTestMissing, Detail: fmt.Sprintf("requirement %s: test not found: %s", id, test)})
			}
		}
		for _, ep := range req.ProductionEntrypoints {
			if !idx.HasSymbol(ep) {
				vs = append(vs, Violation{Code: CodeConnectedNoEntrypoint, Detail: fmt.Sprintf("requirement %s: entrypoint not found: %s", id, ep)})
			}
		}
		_ = actionMap
	}
	return vs
}

func validateChangedDocs(root string, reg Registry, regPath string) []Violation {
	changed := changedFiles(root)
	if len(changed) == 0 {
		return nil
	}
	significant := false
	for _, path := range changed {
		for _, pattern := range reg.ArchitectureSignificantPaths {
			if matchGlob(pattern, path) {
				significant = true
				break
			}
		}
		if significant {
			break
		}
	}
	if !significant {
		return nil
	}
	currentRel := reg.Documents.CurrentArchitecture
	if currentRel == "" {
		currentRel = "docs/ARCHITECTURE.md"
	}
	targetRel := reg.Documents.TargetArchitecture
	if targetRel == "" {
		targetRel = "docs/TARGET-ARCHITECTURE.md"
	}
	regRel, err := filepath.Rel(root, regPath)
	if err != nil {
		regRel = "docs/architecture/COMPONENT-STATUS.yaml"
	}
	docsChanged := false
	for _, path := range changed {
		if path == regRel || path == currentRel || path == targetRel {
			docsChanged = true
			break
		}
	}
	if docsChanged {
		return nil
	}
	impact := filepath.Join(root, ".coord/architecture-impact.yaml")
	raw, err := os.ReadFile(impact)
	if err != nil {
		return []Violation{{Code: CodeDocUpdateRequired, Detail: "architecture-significant code changed without docs/registry or impact manifest"}}
	}
	var manifest struct {
		ArchitectureDocumentation struct {
			Status string `yaml:"status"`
			Reason string `yaml:"reason"`
		} `yaml:"architectureDocumentation"`
	}
	if err := yaml.Unmarshal(raw, &manifest); err != nil {
		return []Violation{{Code: CodeDocUpdateRequired, Detail: "impact manifest must contain status: N/A and a non-empty reason"}}
	}
	if manifest.ArchitectureDocumentation.Status != "N/A" || strings.TrimSpace(manifest.ArchitectureDocumentation.Reason) == "" {
		return []Violation{{Code: CodeDocUpdateRequired, Detail: "impact manifest must contain status: N/A and a non-empty reason"}}
	}
	return nil
}

func changedFiles(root string) []string {
	base := ""
	for _, candidate := range []string{"origin/p0-2-task", "origin/main", "HEAD^"} {
		cmd := exec.Command("git", "rev-parse", "--verify", candidate)
		cmd.Dir = root
		if err := cmd.Run(); err == nil {
			base = candidate
			break
		}
	}
	if base == "" {
		return nil
	}
	cmd := exec.Command("git", "diff", "--name-only", base+"...HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files
}

func matchGlob(pattern, path string) bool {
	// filepath.Match does not treat ** as recursive; approximate Python fnmatch
	// for the patterns used in COMPONENT-STATUS (prefix/**).
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	}
	ok, err := filepath.Match(pattern, path)
	return err == nil && ok
}

func anchorExists(text, anchor string) bool {
	return strings.Contains(text, `id="`+anchor+`"`) ||
		strings.Contains(text, `id='`+anchor+`'`) ||
		strings.Contains(text, "#"+anchor) ||
		strings.Contains(text, "<!-- "+anchor+" -->")
}

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}
