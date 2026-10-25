package documentregistry

import "gopkg.in/yaml.v3"

// Registry is the strict YAML model for docs/DOCUMENT-REGISTRY.yaml.
// All 25 document field names from the committed registry are covered;
// unknown fields at either level are rejected by KnownFields(true).
type Registry struct {
	APIVersion                 string     `yaml:"apiVersion"`
	Kind                       string     `yaml:"kind"`
	Metadata                   yaml.Node  `yaml:"metadata"`
	Vocabulary                 yaml.Node  `yaml:"vocabulary"`
	Precedence                 yaml.Node  `yaml:"precedence"`
	InvariantOwners            yaml.Node  `yaml:"invariantOwners"`
	Rules                      yaml.Node  `yaml:"rules"`
	StartupPaths               yaml.Node  `yaml:"startupPaths"`
	Validation                 yaml.Node  `yaml:"validation"`
	UnregisteredDocumentPolicy yaml.Node  `yaml:"unregisteredDocumentPolicy"`
	ChangeMatrix               yaml.Node  `yaml:"changeMatrix"`
	Documents                  []Document `yaml:"documents"`
}

// Document represents one entry in the registry documents list.
// Purpose, UsedBy, ReadWhen, UpdateWhen use yaml.Node to accept YAML list
// items that contain colons (otherwise parsed as maps by yaml.v3).
type Document struct {
	ID                           string      `yaml:"id"`
	Path                         string      `yaml:"path"`
	Class                        string      `yaml:"class"`
	Status                       string      `yaml:"status"`
	Purpose                      yaml.Node   `yaml:"purpose"`
	UsedBy                       yaml.Node   `yaml:"usedBy"`
	ReadWhen                     yaml.Node   `yaml:"readWhen"`
	UpdateWhen                   yaml.Node   `yaml:"updateWhen"`
	CurrentOperationalAuthority  bool        `yaml:"currentOperationalAuthority"`
	CanonicalProductionAuthority bool        `yaml:"canonicalProductionAuthority"`
	NormativeAuthority           string      `yaml:"normativeAuthority"`
	CurrentNormativeAuthority    *bool       `yaml:"currentNormativeAuthority"`
	Ownership                    yaml.Node   `yaml:"ownership"`
	Acceptance                   yaml.Node   `yaml:"acceptance"`
	Related                      yaml.Node   `yaml:"related"`
	DecisionRef                  string      `yaml:"decisionRef"`
	Optional                     bool        `yaml:"optional"`
	AppliesTo                    yaml.Node   `yaml:"appliesTo"`
	NotApplicableTo              yaml.Node   `yaml:"notApplicableTo"`
	ImmutableHistoricalClaims    yaml.Node   `yaml:"immutableHistoricalClaims"`
	Projections                  yaml.Node   `yaml:"projections"`
	SourceOfStatus               yaml.Node   `yaml:"sourceOfStatus"`
	Location                     *Location   `yaml:"location,omitempty"`
	SupersededBy                 string      `yaml:"supersededBy,omitempty"`
	Provenance                   *Provenance `yaml:"provenance,omitempty"`
}

// Location names where the document bytes live. Omitted location means LOCAL.
// TRACKED_IN_OTHER_BRANCH is allowlisted; the registry never stores a floating SHA.
type Location struct {
	Class      string `yaml:"class"`
	Repository string `yaml:"repository,omitempty"`
	Branch     string `yaml:"branch,omitempty"`
	Path       string `yaml:"path,omitempty"`
}

// Provenance records the source digest and reconciliation reference for a
// historical document whose file may be absent from the current tree.
type Provenance struct {
	SourceDigest      string `yaml:"sourceDigest"`
	ReconciliationRef string `yaml:"reconciliationRef"`
}

const (
	LocationLocal                = "LOCAL"
	LocationTrackedInOtherBranch = "TRACKED_IN_OTHER_BRANCH"

	StatusActive     = "active"
	StatusAccepted   = "accepted"
	StatusProposed   = "proposed"
	StatusLegacy     = "legacy"
	StatusHistorical = "historical"
	StatusGenerated  = "generated"
	StatusSuperseded = "superseded"
)

var validClasses = map[string]bool{
	"normative": true, "architecture_status": true, "process": true,
	"operations": true, "active_context": true, "historical": true, "design": true,
}

var validStatuses = map[string]bool{
	StatusActive: true, StatusAccepted: true, StatusProposed: true,
	StatusLegacy: true, StatusHistorical: true, StatusGenerated: true,
	StatusSuperseded: true,
}

var validLocations = map[string]bool{
	LocationLocal: true, LocationTrackedInOtherBranch: true,
}

// normativeAuthorityAllowlist holds the values that grant a document
// current normative authority over product behaviour or process.
var normativeAuthorityAllowlist = map[string]bool{
	"process": true, "product": true, "canonical": true,
}

// Allowlisted external location. The validator never executes external content;
// it only reads blobs for this exact repository/branch/path-prefix set.
const (
	allowedExternalRepository = "naghuale/coord"
	allowedExternalBranch     = "ops/coord/project"
	allowedExternalPathPrefix = "operations/project/"
)
