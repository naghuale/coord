package defectpatterns

import "gopkg.in/yaml.v3"

// DefectPatterns is the strict YAML model for docs/operations/DEFECT-PATTERNS.yaml.
type DefectPatterns struct {
	APIVersion string    `yaml:"apiVersion"`
	Kind       string    `yaml:"kind"`
	Metadata   yaml.Node `yaml:"metadata"`
	Patterns   []Pattern `yaml:"patterns"`
}

// Pattern is a single defect pattern entry.
type Pattern struct {
	PatternID        string `yaml:"patternId"`
	Title            string `yaml:"title"`
	AutomationStatus string `yaml:"automationStatus"`
	OwningNorm       string `yaml:"owningNorm"`
	SelfTestRef      string `yaml:"selfTestRef"`
}

var validAutomationStatuses = map[string]bool{
	"FULL": true, "PARTIAL": true, "NONE": true,
}
