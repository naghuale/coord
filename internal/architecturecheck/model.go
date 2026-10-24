package architecturecheck

import "gopkg.in/yaml.v3"

// Registry is the strict YAML model for docs/architecture/COMPONENT-STATUS.yaml.
type Registry struct {
	APIVersion                        string                             `yaml:"apiVersion"`
	Kind                              string                             `yaml:"kind"`
	Metadata                          Metadata                           `yaml:"metadata"`
	Documents                         Documents                          `yaml:"documents"`
	ArchitectureSignificantPaths      []string                           `yaml:"architectureSignificantPaths"`
	Components                        []Component                        `yaml:"components"`
	Requirements                      map[string]Requirement             `yaml:"requirements,omitempty"`
	ActionMapReferenceClassifications []ActionMapReferenceClassification `yaml:"actionMapReferenceClassifications,omitempty"`
}

// Metadata holds registry identity and vocabulary.
type Metadata struct {
	ProjectID         string          `yaml:"projectId"`
	RegistryVersion   int             `yaml:"registryVersion"`
	UpdatedFromCommit string          `yaml:"updatedFromCommit"`
	StatusVocabulary  []string        `yaml:"statusVocabulary"`
	AxisVocabulary    *AxisVocabulary `yaml:"axisVocabulary,omitempty"`
}

// AxisVocabulary lists allowed values for the four independent status axes.
type AxisVocabulary struct {
	Code          []string `yaml:"code"`
	Connection    []string `yaml:"connection"`
	Qualification []string `yaml:"qualification"`
	Production    []string `yaml:"production"`
}

// Documents points at current and target architecture docs.
type Documents struct {
	CurrentArchitecture string `yaml:"currentArchitecture"`
	TargetArchitecture  string `yaml:"targetArchitecture"`
}

// Component is one architecture component row.
// Legacy status stays during migration; axes are the OWNER vocabulary.
type Component struct {
	ID                         string        `yaml:"id"`
	Title                      string        `yaml:"title"`
	Status                     string        `yaml:"status"`
	NormativeForCurrentProduct *bool         `yaml:"normativeForCurrentProduct,omitempty"`
	NormativeSources           []string      `yaml:"normativeSources"`
	Implemented                []string      `yaml:"implemented"`
	Missing                    []string      `yaml:"missing"`
	Evidence                   Evidence      `yaml:"evidence"`
	Documents                  ComponentDocs `yaml:"documents"`
	Code                       string        `yaml:"code,omitempty"`
	Connection                 string        `yaml:"connection,omitempty"`
	Qualification              string        `yaml:"qualification,omitempty"`
	Production                 string        `yaml:"production,omitempty"`
	ProductionEntrypoints      []string      `yaml:"productionEntrypoints,omitempty"`
	Extra                      yaml.Node     `yaml:"-"`
}

// Evidence lists symbols, tests and issue numbers.
type Evidence struct {
	Symbols []string `yaml:"symbols"`
	Tests   []string `yaml:"tests"`
	Issues  []any    `yaml:"issues"`
}

// ComponentDocs holds document anchors.
type ComponentDocs struct {
	CurrentAnchor string `yaml:"currentAnchor"`
	TargetAnchor  string `yaml:"targetAnchor"`
}

// Requirement is a critical-path requirement row keyed by stable ID.
type Requirement struct {
	SourceDocumentID      string   `yaml:"sourceDocumentId"`
	Section               string   `yaml:"section"`
	Component             string   `yaml:"component"`
	NormativeStatus       string   `yaml:"normativeStatus"`
	ImplementationSymbols []string `yaml:"implementationSymbols"`
	ProductionEntrypoints []string `yaml:"productionEntrypoints"`
	Tests                 []string `yaml:"tests"`
	Gaps                  []string `yaml:"gaps"`
	Issues                []any    `yaml:"issues"`
}

// ActionMapReferenceClassification records one NV-4 hit classification.
type ActionMapReferenceClassification struct {
	Path   string `yaml:"path"`
	Line   int    `yaml:"line"`
	Class  string `yaml:"class"`
	Note   string `yaml:"note,omitempty"`
	Phrase string `yaml:"phrase,omitempty"`
}

// Violation is a single failing check with a stable reason code.
type Violation struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// Finding codes preserved from the Python checker.
const (
	CodeStatusInvalid          = "ARCHITECTURE_STATUS_INVALID"
	CodeStatusConflict         = "ARCHITECTURE_STATUS_CONFLICT"
	CodeMaturityInflated       = "ARCHITECTURE_MATURITY_INFLATED"
	CodeProposedAsImplemented  = "ARCHITECTURE_PROPOSED_AS_IMPLEMENTED"
	CodeEvidenceMissing        = "ARCHITECTURE_EVIDENCE_MISSING"
	CodeTestMissing            = "ARCHITECTURE_TEST_MISSING"
	CodeSymbolMissing          = "ARCHITECTURE_SYMBOL_MISSING"
	CodeAnchorMissing          = "ARCHITECTURE_ANCHOR_MISSING"
	CodeDocUpdateRequired      = "ARCHITECTURE_DOC_UPDATE_REQUIRED"
	CodeCurrentMissing         = "ARCHITECTURE_CURRENT_MISSING"
	CodeTargetMissing          = "ARCHITECTURE_TARGET_MISSING"
	CodeProposedProductionAuth = "ARCHITECTURE_PROPOSED_PRODUCTION_AUTHORITY"
	CodeConnectedNoEntrypoint  = "ARCHITECTURE_CONNECTED_WITHOUT_ENTRYPOINT"
	CodeActiveNoRuntime        = "ARCHITECTURE_ACTIVE_WITHOUT_RUNTIME_EVIDENCE"
	CodeUnknownRequirementID   = "ARCHITECTURE_UNKNOWN_REQUIREMENT_ID"
	CodeUnclassifiedActionMap  = "ARCHITECTURE_ACTION_MAP_UNCLASSIFIED"
	CodeAuthoritativeActionMap = "ARCHITECTURE_ACTION_MAP_AUTHORITATIVE_WHILE_PROPOSED"
	CodeAxisInvalid            = "ARCHITECTURE_AXIS_INVALID"
	CodeImplementedNoSymbol    = "ARCHITECTURE_IMPLEMENTED_WITHOUT_SYMBOL"
	CodeQualifiedNoTest        = "ARCHITECTURE_QUALIFIED_WITHOUT_TEST"
)

var legacyStatuses = map[string]bool{
	"implemented": true,
	"partial":     true,
	"missing":     true,
	"proposed":    true,
	"legacy":      true,
}

var codeAxis = map[string]bool{
	"MISSING": true, "PARTIAL": true, "IMPLEMENTED": true, "not_applicable": true,
}
var connectionAxis = map[string]bool{
	"NOT_CONNECTED": true, "TEST_ONLY": true, "PRODUCTION_CONNECTED": true, "not_applicable": true,
}
var qualificationAxis = map[string]bool{
	"NOT_EVALUATED": true, "PARTIAL": true, "PASS": true, "FAIL": true,
	"ACCEPTED_WITH_LIMITATIONS": true, "not_applicable": true,
}
var productionAxis = map[string]bool{
	"DISABLED": true, "BLOCKED": true, "ACTIVE": true, "ACTIVE_WITH_LIMITATIONS": true, "not_applicable": true,
}

var actionMapClasses = map[string]bool{
	"AUTHORITATIVE_REFERENCE":      true,
	"NON_NORMATIVE_CHECKLIST":      true,
	"HISTORICAL_REFERENCE":         true,
	"STALE_REFERENCE":              true,
	"CONDITIONAL_FUTURE_REFERENCE": true,
}
