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
	Root            string
	RegistryPath    string
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

	docs, loadErr := loadDocumentRegistryIndex(root, opts.DocRegistryPath)
	actionMap := actionMapDocStatus{registryOK: loadErr == nil}
	if loadErr == nil {
		if d, ok := findActionMapRegistryDoc(docs); ok {
			actionMap = classifyActionMapDoc(d)
			actionMap.registryOK = true
			actionMap.found = true
			actionMap.evaluatePublication(root)
		}
	}

	var vs []Violation
	if loadErr != nil {
		vs = append(vs, Violation{Code: CodeDocumentRegistryUnavailable, Detail: loadErr.Error()})
	}
	vs = append(vs, validateDocuments(root, reg)...)
	vs = append(vs, validateComponents(root, reg, idx)...)
	vs = append(vs, validateAxes(reg)...)
	vs = append(vs, validateRequirements(reg, idx, docs)...)
	vs = append(vs, validateActionMapRefs(root, reg, actionMap)...)
	if !opts.SkipChangedDocs {
		vs = append(vs, validateChangedDocs(root, reg, regPath)...)
	}

	return &Result{Violations: vs, Components: len(reg.Components)}, nil
}

type actionMapDocStatus struct {
	proposed           bool
	found              bool
	registryOK         bool // false when DOCUMENT-REGISTRY could not be loaded
	status             string
	statusMissing      bool // row present but status field absent/empty
	statusWrongType    bool
	statusUnrecognized bool // present string outside the known vocabulary
	fieldInvalid       bool // any FieldInvalid on the row (incl. status wrong type)
	nonAuthoritative   bool // status recognized but not exactly "accepted" (R6-F1)
	path               string
	optional           bool
	locationClass      string // "" means LOCAL (omitted)
	publishedOK        bool
	publishedDetail    string
	publishedReason    string // EMPTY_PATH | PATH_OUTSIDE_REPOSITORY | ...
}

// registryDoc is a lightweight DOCUMENT-REGISTRY row used for authority checks.
// Pointer fields distinguish missing from false (R3-F1 / PRODUCTION-AUTHORITY-SOURCE-01).
type registryDoc struct {
	ID                           string
	Path                         string
	Status                       string
	StatusPresent                bool // true when YAML status key was present (even if wrong type)
	Optional                     bool
	LocationClass                string // empty => LOCAL
	CanonicalProductionAuthority *bool
	CurrentNormativeAuthority    *bool
	NormativeAuthority           *string
	// FieldInvalid is set when an authority field has a wrong YAML type.
	FieldInvalid string
}

// classifyActionMapDoc derives ACTION-MAP authority posture for R3-F2 / R4-F1 / R6-F1.
func classifyActionMapDoc(d registryDoc) actionMapDocStatus {
	out := actionMapDocStatus{
		status:        d.Status,
		fieldInvalid:  d.FieldInvalid != "",
		path:          d.Path,
		optional:      d.Optional,
		locationClass: d.LocationClass,
	}
	if strings.Contains(d.FieldInvalid, "status has wrong type") {
		out.statusWrongType = true
	}
	if !d.StatusPresent && !out.statusWrongType {
		out.statusMissing = true
	} else if d.StatusPresent && d.Status == "" && !out.statusWrongType {
		out.statusMissing = true
	}
	if d.Status != "" {
		if !recognizedDocStatus(d.Status) {
			out.statusUnrecognized = true
		}
		out.proposed = d.Status == "proposed"
		// R6-F1: AUTHORITATIVE_REFERENCE allow-list is exactly "accepted".
		out.nonAuthoritative = d.Status != "accepted"
	}
	return out
}

// findActionMapRegistryDoc locates the ACTION-MAP row by canonical path or by id,
// so empty/escaping path fields still surface as publication failures (R7-F2)
// rather than a silent missing-row miss.
func findActionMapRegistryDoc(docs documentRegistryIndex) (registryDoc, bool) {
	if d, ok := docs.byPath["docs/operations/ACTION-MAP.yaml"]; ok {
		return d, true
	}
	if d, ok := docs.byID["action-map"]; ok {
		return d, true
	}
	return registryDoc{}, false
}

// evaluatePublication applies the authoritative-ACTION-MAP publication rule
// (R6-F1 / R7-F1). Order: non-empty path → join to root → EvalSymlinks (and
// resolve root) → inside resolved root → regular file → git-tracked
// (`git ls-files --error-unmatch`). optional:true does not waive.
func (a *actionMapDocStatus) evaluatePublication(root string) {
	if a.status != "accepted" || a.statusMissing || a.statusWrongType || a.statusUnrecognized || a.fieldInvalid {
		return
	}
	fail := func(reason, detail string) {
		a.publishedReason = reason
		a.publishedDetail = reason + ": " + detail
	}
	loc := a.locationClass
	if loc == "" {
		loc = "LOCAL"
	}
	if loc != "LOCAL" {
		fail("NON_LOCAL_LOCATION", fmt.Sprintf("location class %q", loc))
		return
	}
	if strings.TrimSpace(a.path) == "" {
		fail("EMPTY_PATH", "registry path is empty")
		return
	}
	// Absolute registry paths are outside the join model.
	if filepath.IsAbs(a.path) {
		fail("PATH_OUTSIDE_REPOSITORY", fmt.Sprintf("absolute path %q", a.path))
		return
	}
	clean := filepath.Clean(a.path)
	full := filepath.Join(root, clean)

	rootResolved := root
	if rr, err := filepath.EvalSymlinks(root); err == nil {
		rootResolved = rr
	}

	// Resolve symlinks; if the path (or a parent) cannot be resolved, classify.
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		// Distinguish missing leaf vs other resolve failures after lexical join.
		if _, statErr := os.Lstat(full); statErr != nil {
			fail("FILE_MISSING", fmt.Sprintf("path %s", clean))
			return
		}
		// Symlink present but target unresolvable / outside — treat as symlink escape.
		fail("SYMLINK_TARGET_OUTSIDE_REPOSITORY", fmt.Sprintf("EvalSymlinks(%s): %v", clean, err))
		return
	}
	resRel, err := filepath.Rel(rootResolved, resolved)
	if err != nil || resRel == ".." || strings.HasPrefix(resRel, ".."+string(filepath.Separator)) {
		// Prefer symlink-specific reason when the joined path was a symlink.
		if li, lerr := os.Lstat(full); lerr == nil && li.Mode()&os.ModeSymlink != 0 {
			fail("SYMLINK_TARGET_OUTSIDE_REPOSITORY", fmt.Sprintf("%q -> %q", clean, resolved))
			return
		}
		fail("PATH_OUTSIDE_REPOSITORY", fmt.Sprintf("%q -> %q", clean, resolved))
		return
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		fail("FILE_MISSING", fmt.Sprintf("resolved %s", clean))
		return
	}
	if !fi.Mode().IsRegular() {
		fail("NOT_REGULAR_FILE", clean)
		return
	}
	if !gitTrackedPath(root, clean) {
		fail("FILE_NOT_TRACKED", clean)
		return
	}
	a.publishedOK = true
}

func gitTrackedPath(root, relPath string) bool {
	cmd := exec.Command("git", "-C", root, "ls-files", "--error-unmatch", "--", relPath)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	return cmd.Run() == nil
}

// recognizedDocStatus is the DOCUMENT-REGISTRY status vocabulary (exact match, no normalization).
func recognizedDocStatus(status string) bool {
	switch status {
	case "proposed", "accepted", "active", "historical", "superseded", "legacy":
		return true
	default:
		return false
	}
}

type documentRegistryIndex struct {
	byID   map[string]registryDoc
	byPath map[string]registryDoc
}

func loadDocumentRegistryIndex(root, docRegistryPath string) (documentRegistryIndex, error) {
	out := documentRegistryIndex{byID: map[string]registryDoc{}, byPath: map[string]registryDoc{}}
	path := docRegistryPath
	if path == "" {
		path = filepath.Join(root, "docs/DOCUMENT-REGISTRY.yaml")
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out, fmt.Errorf("document registry unavailable at %s: %w", path, err)
	}
	// Lightweight scan: avoid importing internal/documentregistry (cycle risk).
	// Authority fields use interface{} so wrong types stay detectable (R3-F1).
	var doc struct {
		Documents []struct {
			ID       string      `yaml:"id"`
			Path     string      `yaml:"path"`
			Status   interface{} `yaml:"status"`
			Optional bool        `yaml:"optional"`
			Location *struct {
				Class string `yaml:"class"`
			} `yaml:"location"`
			CanonicalProductionAuthority interface{} `yaml:"canonicalProductionAuthority"`
			CurrentNormativeAuthority    interface{} `yaml:"currentNormativeAuthority"`
			NormativeAuthority           interface{} `yaml:"normativeAuthority"`
		} `yaml:"documents"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return out, fmt.Errorf("document registry malformed at %s: %w", path, err)
	}
	for _, d := range doc.Documents {
		row := registryDoc{ID: d.ID, Path: d.Path, Optional: d.Optional}
		if d.Location != nil {
			row.LocationClass = d.Location.Class
		}
		if d.Status != nil {
			row.StatusPresent = true
			s, ok := d.Status.(string)
			if !ok {
				row.FieldInvalid = fmt.Sprintf("%s: status has wrong type %T", d.ID, d.Status)
			} else {
				row.Status = s
			}
		}
		if d.CanonicalProductionAuthority != nil {
			b, ok := d.CanonicalProductionAuthority.(bool)
			if !ok {
				if row.FieldInvalid == "" {
					row.FieldInvalid = fmt.Sprintf("%s: canonicalProductionAuthority has wrong type %T", d.ID, d.CanonicalProductionAuthority)
				}
			} else {
				row.CanonicalProductionAuthority = &b
			}
		}
		if d.CurrentNormativeAuthority != nil {
			b, ok := d.CurrentNormativeAuthority.(bool)
			if !ok {
				if row.FieldInvalid == "" {
					row.FieldInvalid = fmt.Sprintf("%s: currentNormativeAuthority has wrong type %T", d.ID, d.CurrentNormativeAuthority)
				}
			} else {
				row.CurrentNormativeAuthority = &b
			}
		}
		if d.NormativeAuthority != nil {
			s, ok := d.NormativeAuthority.(string)
			if !ok {
				if row.FieldInvalid == "" {
					row.FieldInvalid = fmt.Sprintf("%s: normativeAuthority has wrong type %T", d.ID, d.NormativeAuthority)
				}
			} else {
				row.NormativeAuthority = &s
			}
		}
		if d.ID != "" {
			out.byID[d.ID] = row
		}
		if d.Path != "" {
			out.byPath[d.Path] = row
		}
	}
	if len(out.byID) == 0 && len(out.byPath) == 0 {
		return out, fmt.Errorf("document registry empty at %s", path)
	}
	return out, nil
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

// componentHasForbiddenProductionClaim reports axis values that mean production
// use, production connection, or qualification under the #97 four-axis vocabulary.
// Mapping (RESULT semanticMapping / PRODUCTION-AUTHORITY-SOURCE-01):
//
//	productionUse        -> Production ACTIVE | ACTIVE_WITH_LIMITATIONS
//	productionConnection -> Connection PRODUCTION_CONNECTED
//	qualified            -> Qualification PASS | ACCEPTED_WITH_LIMITATIONS
//	disabledOrBlocked    -> Production DISABLED | BLOCKED (allowed with non-authoritative source)
func componentHasForbiddenProductionClaim(c Component) (bool, string) {
	switch c.Production {
	case "ACTIVE", "ACTIVE_WITH_LIMITATIONS":
		return true, fmt.Sprintf("production=%s", c.Production)
	}
	if c.Connection == "PRODUCTION_CONNECTED" {
		return true, fmt.Sprintf("connection=%s", c.Connection)
	}
	switch c.Qualification {
	case "PASS", "ACCEPTED_WITH_LIMITATIONS":
		return true, fmt.Sprintf("qualification=%s", c.Qualification)
	}
	return false, ""
}

// authorizeProductionSource applies PRODUCTION-AUTHORITY-SOURCE-01:
// one source must carry status=accepted AND canonicalProductionAuthority=true
// AND currentNormativeAuthority=true AND normativeAuthority=product.
// Missing is distinct from false; active is not accepted; no composition across sources.
func authorizeProductionSource(src registryDoc) (ok bool, code, detail string) {
	if src.FieldInvalid != "" {
		return false, CodeProdAuthInvalidField, src.FieldInvalid
	}
	if src.Status != "accepted" {
		return false, CodeProdAuthWrongStatus, fmt.Sprintf("status=%q (need accepted; active is not accepted)", src.Status)
	}
	if src.NormativeAuthority == nil {
		return false, CodeProdAuthInvalidField, "missing normativeAuthority"
	}
	if *src.NormativeAuthority != "product" {
		return false, CodeProdAuthWrongAuthorityClass, fmt.Sprintf("normativeAuthority=%q (need product)", *src.NormativeAuthority)
	}
	if src.CanonicalProductionAuthority == nil {
		return false, CodeProdAuthMissingCanonical, "missing canonicalProductionAuthority"
	}
	if !*src.CanonicalProductionAuthority {
		return false, CodeProdAuthMissingCanonical, "canonicalProductionAuthority=false"
	}
	if src.CurrentNormativeAuthority == nil {
		return false, CodeProdAuthMissingCurrentNormative, "missing currentNormativeAuthority"
	}
	if !*src.CurrentNormativeAuthority {
		return false, CodeProdAuthMissingCurrentNormative, "currentNormativeAuthority=false"
	}
	return true, "", ""
}

func validateRequirements(reg Registry, idx *CodeIndex, docs documentRegistryIndex) []Violation {
	var vs []Violation
	components := map[string]Component{}
	for _, c := range reg.Components {
		components[c.ID] = c
	}

	// Components covered by at least one requirement whose SINGLE source
	// satisfies PRODUCTION-AUTHORITY-SOURCE-01 (all four on one row).
	authorizedComponents := map[string]bool{}

	for id, req := range reg.Requirements {
		if id == "" {
			vs = append(vs, Violation{Code: CodeUnknownRequirementID, Detail: "empty requirement id"})
			continue
		}
		compID := strings.TrimSpace(req.Component)
		if compID == "" {
			vs = append(vs, Violation{Code: CodeUnknownRequirementID, Detail: fmt.Sprintf("%s: empty component", id)})
			continue
		}
		c, ok := components[compID]
		if !ok {
			vs = append(vs, Violation{Code: CodeUnknownRequirementID, Detail: fmt.Sprintf("%s: unknown component %q", id, compID)})
			continue
		}
		// Resolution chain: requirement -> sourceDocumentId -> DOCUMENT-REGISTRY
		// -> PRODUCTION-AUTHORITY-SOURCE-01 -> axes. Row NormativeStatus never
		// raises authority (F97-1 / R3-F1).
		srcID := strings.TrimSpace(req.SourceDocumentID)
		if srcID == "" {
			vs = append(vs, Violation{Code: CodeUnknownRequirementID, Detail: fmt.Sprintf("%s: missing sourceDocumentId", id)})
			if bad, claim := componentHasForbiddenProductionClaim(c); bad {
				vs = append(vs, Violation{Code: CodeProposedProductionAuth, Detail: fmt.Sprintf("%s: missing sourceDocumentId cannot authorize %s for %s", id, claim, c.ID)})
			}
			continue
		}
		src, ok := docs.byID[srcID]
		if !ok {
			vs = append(vs, Violation{Code: CodeUnknownRequirementID, Detail: fmt.Sprintf("%s: unresolved sourceDocumentId %q", id, srcID)})
			if bad, claim := componentHasForbiddenProductionClaim(c); bad {
				vs = append(vs, Violation{Code: CodeProposedProductionAuth, Detail: fmt.Sprintf("%s: unresolved source %q cannot authorize %s for %s", id, srcID, claim, c.ID)})
			}
			continue
		}
		authOK, authCode, authDetail := authorizeProductionSource(src)
		if authOK {
			authorizedComponents[compID] = true
		} else if bad, claim := componentHasForbiddenProductionClaim(c); bad {
			vs = append(vs, Violation{Code: authCode, Detail: fmt.Sprintf("%s: source %q cannot authorize %s for %s: %s", id, srcID, claim, c.ID, authDetail)})
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
	}

	// Every component with a production claim must be linked to at least one
	// requirement whose DOCUMENT-REGISTRY source satisfies
	// PRODUCTION-AUTHORITY-SOURCE-01. Absent/empty inventory fails closed.
	for _, c := range reg.Components {
		bad, claim := componentHasForbiddenProductionClaim(c)
		if !bad {
			continue
		}
		if !authorizedComponents[c.ID] {
			if len(reg.Requirements) == 0 {
				vs = append(vs, Violation{Code: CodeProposedProductionAuth, Detail: fmt.Sprintf("%s: production claim %s requires a non-empty requirements inventory with a PRODUCTION-AUTHORITY-SOURCE-01 source", c.ID, claim)})
			} else {
				vs = append(vs, Violation{Code: CodeProposedProductionAuth, Detail: fmt.Sprintf("%s: production claim %s has no requirement linked to a single source with accepted+canonicalProductionAuthority+currentNormativeAuthority+normativeAuthority=product", c.ID, claim)})
			}
		}
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
