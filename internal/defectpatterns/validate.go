package defectpatterns

import (
	"fmt"
	"os"
	"regexp"

	"coord/internal/governanceio"
)

// Violation is a single failing check with a stable reason code and detail.
type Violation struct {
	Code   string
	Detail string
}

var patternIDRegexp = regexp.MustCompile(`^DP-[0-9]{3}$`)

// Validate parses patternsPath as DEFECT-PATTERNS.yaml and runs all
// structural governance checks relative to root.
func Validate(patternsPath, root string) ([]Violation, error) {
	f, err := os.Open(patternsPath)
	if err != nil {
		return nil, fmt.Errorf("open defect patterns: %w", err)
	}
	defer func() { _ = f.Close() }()

	dp, err := governanceio.DecodeStrictYAML[DefectPatterns](f)
	if err != nil {
		return nil, fmt.Errorf("parse defect patterns: %w", err)
	}
	return validate(dp, root), nil
}

func validate(dp DefectPatterns, root string) []Violation {
	var vs []Violation

	if dp.APIVersion != "coord.dev/v1alpha1" {
		vs = append(vs, Violation{Code: "INVALID_API_VERSION", Detail: fmt.Sprintf("got %q", dp.APIVersion)})
	}
	if dp.Kind != "DefectPatterns" {
		vs = append(vs, Violation{Code: "INVALID_KIND", Detail: fmt.Sprintf("got %q", dp.Kind)})
	}

	seenIDs := map[string]bool{}

	for i, p := range dp.Patterns {
		loc := fmt.Sprintf("pattern[%d] id=%q", i, p.PatternID)

		if p.PatternID == "" {
			vs = append(vs, Violation{Code: "MISSING_PATTERN_ID", Detail: fmt.Sprintf("pattern[%d] has no patternId", i)})
		} else {
			if !patternIDRegexp.MatchString(p.PatternID) {
				vs = append(vs, Violation{Code: "INVALID_PATTERN_ID", Detail: fmt.Sprintf("%s: patternId must match DP-NNN", loc)})
			}
			if seenIDs[p.PatternID] {
				vs = append(vs, Violation{Code: "DUPLICATE_PATTERN_ID", Detail: fmt.Sprintf("%s: patternId already seen", loc)})
			} else {
				seenIDs[p.PatternID] = true
			}
		}

		if !validAutomationStatuses[p.AutomationStatus] {
			vs = append(vs, Violation{Code: "INVALID_AUTOMATION_STATUS", Detail: fmt.Sprintf("%s: automationStatus must be FULL|PARTIAL|NONE, got %q", loc, p.AutomationStatus)})
		}

		if p.OwningNorm == "" {
			vs = append(vs, Violation{Code: "MISSING_OWNING_NORM", Detail: fmt.Sprintf("%s: owningNorm is required", loc)})
		}

		// FULL automation requires a selfTestRef to prove the check is automated.
		if p.AutomationStatus == "FULL" && p.SelfTestRef == "" {
			vs = append(vs, Violation{Code: "FULL_MISSING_SELF_TEST", Detail: fmt.Sprintf("%s: FULL automation requires selfTestRef", loc)})
		}
	}

	// owningNorm files must exist relative to root (if root is provided).
	if root != "" {
		seenNorm := map[string]bool{}
		for _, p := range dp.Patterns {
			if p.OwningNorm != "" && !seenNorm[p.OwningNorm] {
				seenNorm[p.OwningNorm] = true
				full := root + "/" + p.OwningNorm
				if _, err := os.Stat(full); err != nil {
					vs = append(vs, Violation{
						Code:   "OWNING_NORM_NOT_FOUND",
						Detail: fmt.Sprintf("owningNorm %q not found at %s", p.OwningNorm, full),
					})
				}
			}
		}
	}

	return vs
}
