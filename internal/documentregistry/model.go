package documentregistry

import "gopkg.in/yaml.v3"

// Registry is the strict YAML model for docs/DOCUMENT-REGISTRY.yaml.
// All 22 document field names from the committed registry are covered;
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
// Purpose, UsedBy, ReadWhen, UpdateWhen use []yaml.Node to accept YAML list
// items that contain colons (otherwise parsed as maps by yaml.v3).
type Document struct {
	ID                           string    `yaml:"id"`
	Path                         string    `yaml:"path"`
	Class                        string    `yaml:"class"`
	Status                       string    `yaml:"status"`
	Purpose                      yaml.Node `yaml:"purpose"`
	UsedBy                       yaml.Node `yaml:"usedBy"`
	ReadWhen                     yaml.Node `yaml:"readWhen"`
	UpdateWhen                   yaml.Node `yaml:"updateWhen"`
	CurrentOperationalAuthority  bool      `yaml:"currentOperationalAuthority"`
	CanonicalProductionAuthority bool      `yaml:"canonicalProductionAuthority"`
	NormativeAuthority           string    `yaml:"normativeAuthority"`
	CurrentNormativeAuthority    *bool     `yaml:"currentNormativeAuthority"`
	Ownership                    yaml.Node `yaml:"ownership"`
	Acceptance                   yaml.Node `yaml:"acceptance"`
	Related                      yaml.Node `yaml:"related"`
	DecisionRef                  string    `yaml:"decisionRef"`
	Optional                     bool      `yaml:"optional"`
	AppliesTo                    yaml.Node `yaml:"appliesTo"`
	NotApplicableTo              yaml.Node `yaml:"notApplicableTo"`
	ImmutableHistoricalClaims    yaml.Node `yaml:"immutableHistoricalClaims"`
	Projections                  yaml.Node `yaml:"projections"`
	SourceOfStatus               yaml.Node `yaml:"sourceOfStatus"`
}

var validClasses = map[string]bool{
	"normative": true, "architecture_status": true, "process": true,
	"operations": true, "active_context": true, "historical": true, "design": true,
}

var validStatuses = map[string]bool{
	"active": true, "accepted": true, "proposed": true,
	"legacy": true, "historical": true, "generated": true,
}

// normativeAuthorityAllowlist holds the values that grant a document
// current normative authority over product behaviour or process.
var normativeAuthorityAllowlist = map[string]bool{
	"process": true, "product": true, "canonical": true,
}
