package documentregistry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"coord/internal/governanceio"
	"gopkg.in/yaml.v3"
)

// Violation is a single failing check with a stable reason code and detail.
type Violation struct {
	Code   string
	Detail string
}

// Validate parses registryPath as DOCUMENT-REGISTRY.yaml and runs all
// structural governance checks relative to root.
func Validate(registryPath, root string) ([]Violation, error) {
	f, err := os.Open(registryPath)
	if err != nil {
		return nil, fmt.Errorf("open registry: %w", err)
	}
	defer func() { _ = f.Close() }()

	reg, err := governanceio.DecodeStrictYAML[Registry](f)
	if err != nil {
		return nil, fmt.Errorf("parse registry: %w", err)
	}
	return validate(reg, root), nil
}

func validate(reg Registry, root string) []Violation {
	var vs []Violation

	if reg.APIVersion != "coord.dev/v1alpha1" {
		vs = append(vs, Violation{
			Code:   "INVALID_API_VERSION",
			Detail: fmt.Sprintf("expected coord.dev/v1alpha1, got %q", reg.APIVersion),
		})
	}
	if reg.Kind != "DocumentRegistry" {
		vs = append(vs, Violation{
			Code:   "INVALID_KIND",
			Detail: fmt.Sprintf("expected DocumentRegistry, got %q", reg.Kind),
		})
	}

	seenIDs := map[string]bool{}
	seenPaths := map[string]bool{}

	for i, doc := range reg.Documents {
		loc := fmt.Sprintf("doc[%d] id=%q", i, doc.ID)

		if doc.ID == "" {
			vs = append(vs, Violation{Code: "MISSING_ID", Detail: fmt.Sprintf("doc[%d] has no id", i)})
		} else if seenIDs[doc.ID] {
			vs = append(vs, Violation{Code: "DUPLICATE_ID", Detail: fmt.Sprintf("%s: id already seen", loc)})
		} else {
			seenIDs[doc.ID] = true
		}

		if doc.Path == "" {
			vs = append(vs, Violation{Code: "MISSING_PATH", Detail: fmt.Sprintf("%s: no path", loc)})
		} else {
			if seenPaths[doc.Path] {
				vs = append(vs, Violation{Code: "DUPLICATE_PATH", Detail: fmt.Sprintf("%s: path %q already registered", loc, doc.Path)})
			} else {
				seenPaths[doc.Path] = true
			}
			if pathErr := checkPathContainment(root, doc.Path); pathErr != nil {
				vs = append(vs, Violation{Code: "PATH_ESCAPE", Detail: fmt.Sprintf("%s: %v", loc, pathErr)})
			} else if !doc.Optional {
				full := filepath.Join(root, filepath.Clean(doc.Path))
				if _, err := os.Stat(full); errors.Is(err, os.ErrNotExist) {
					vs = append(vs, Violation{Code: "MISSING_FILE", Detail: fmt.Sprintf("%s: registered file not found: %s", loc, doc.Path)})
				}
			}
		}

		if !validClasses[doc.Class] {
			vs = append(vs, Violation{Code: "INVALID_CLASS", Detail: fmt.Sprintf("%s: unknown class %q", loc, doc.Class)})
		}
		if !validStatuses[doc.Status] {
			vs = append(vs, Violation{Code: "INVALID_STATUS", Detail: fmt.Sprintf("%s: unknown status %q", loc, doc.Status)})
		}

		if nodeEmpty(doc.UsedBy) {
			vs = append(vs, Violation{Code: "MISSING_USED_BY", Detail: fmt.Sprintf("%s: usedBy is empty or absent", loc)})
		}
		if nodeEmpty(doc.ReadWhen) {
			vs = append(vs, Violation{Code: "MISSING_READ_WHEN", Detail: fmt.Sprintf("%s: readWhen is empty or absent", loc)})
		}
		if nodeEmpty(doc.UpdateWhen) {
			vs = append(vs, Violation{Code: "MISSING_UPDATE_WHEN", Detail: fmt.Sprintf("%s: updateWhen is empty or absent", loc)})
		}

		if doc.Status == "historical" && doc.CurrentOperationalAuthority {
			vs = append(vs, Violation{Code: "HISTORICAL_CURRENT_AUTHORITY", Detail: fmt.Sprintf("%s: historical doc cannot be current operational authority", loc)})
		}
		if doc.Status == "legacy" && doc.CanonicalProductionAuthority {
			vs = append(vs, Violation{Code: "LEGACY_CANONICAL_AUTHORITY", Detail: fmt.Sprintf("%s: legacy doc cannot be canonical production authority", loc)})
		}

		// Proposed documents cannot claim current normative authority.
		if doc.Status == "proposed" &&
			normativeAuthorityAllowlist[doc.NormativeAuthority] &&
			doc.CurrentNormativeAuthority != nil && *doc.CurrentNormativeAuthority {
			vs = append(vs, Violation{
				Code:   "PROPOSED_MANDATORY_AUTHORITY",
				Detail: fmt.Sprintf("%s: proposed doc claims current normative authority %q", loc, doc.NormativeAuthority),
			})
		}

		// Non-normative-class docs that are accepted with current normative authority
		// must have a durable decision provenance reference.
		if doc.Class != "normative" &&
			doc.Status == "accepted" &&
			normativeAuthorityAllowlist[doc.NormativeAuthority] &&
			doc.CurrentNormativeAuthority != nil && *doc.CurrentNormativeAuthority &&
			doc.DecisionRef == "" {
			vs = append(vs, Violation{
				Code:   "ACCEPTED_NORM_MISSING_DECISION_REF",
				Detail: fmt.Sprintf("%s: accepted non-normative doc with current authority requires decisionRef", loc),
			})
		}

		// Validate decisionRef when present.
		if doc.DecisionRef != "" {
			if strings.ContainsAny(doc.DecisionRef, "/\\") || strings.HasPrefix(doc.DecisionRef, ".") {
				vs = append(vs, Violation{
					Code:   "INVALID_DECISION_REF",
					Detail: fmt.Sprintf("%s: decisionRef must be a bare ID (no path separators), got %q", loc, doc.DecisionRef),
				})
			}
		}
	}

	return vs
}

// nodeEmpty reports whether a yaml.Node is absent, null, or an empty sequence.
func nodeEmpty(n yaml.Node) bool {
	if n.Kind == 0 {
		return true // zero value: field absent
	}
	if n.Kind == yaml.ScalarNode && (n.Tag == "!!null" || n.Value == "") {
		return true
	}
	if n.Kind == yaml.SequenceNode && len(n.Content) == 0 {
		return true
	}
	return false
}

// checkPathContainment returns an error if relPath escapes root via .. or symlinks.
func checkPathContainment(root, relPath string) error {
	if filepath.IsAbs(relPath) {
		return errors.New("absolute path rejected")
	}
	cleaned := filepath.Clean(relPath)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return errors.New("path escapes root via parent reference")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("cannot resolve root: %w", err)
	}
	full := filepath.Join(absRoot, cleaned)
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return nil // file does not exist; checked separately
	}
	rootResolved, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		rootResolved = absRoot
	}
	sep := string(filepath.Separator)
	if !strings.HasPrefix(resolved+sep, rootResolved+sep) {
		return fmt.Errorf("symlink escapes root: %s resolves outside %s", relPath, root)
	}
	return nil
}
