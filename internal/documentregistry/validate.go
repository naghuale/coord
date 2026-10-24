package documentregistry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"coord/internal/governanceio"
	"gopkg.in/yaml.v3"
)

// Violation is a single failing check with a stable reason code and detail.
type Violation struct {
	Code   string
	Detail string
}

// Mode selects how TRACKED_IN_OTHER_BRANCH refs are evaluated.
type Mode string

const (
	// ModeLocal is the developer default. An unavailable external ref is a
	// NOT_EVALUATED warning, never a PASS for that document.
	ModeLocal Mode = "LOCAL"
	// ModeCIStrict fails closed on fetch/ref/file/allowlist/blob errors and
	// writes an ephemeral coord.registry-evidence/v1 artifact outside the tree.
	ModeCIStrict Mode = "CI_STRICT"
)

// Options configures ValidateWithOptions. Root is required. Mode defaults to LOCAL.
type Options struct {
	Root           string
	Mode           Mode
	External       ExternalBlobReader
	EvidencePath   string
	RepositoryHead string
	Now            time.Time
}

// Result holds hard failures and LOCAL-mode warnings.
type Result struct {
	Violations []Violation
	Warnings   []Violation
}

// ExternalBlobReader reads allowlisted blobs without executing their content.
type ExternalBlobReader interface {
	// Fetch is called only in CI_STRICT before reading. LOCAL must not fetch.
	Fetch() error
	// Read returns the branch head and blob oid when the path exists on the ref.
	Read(repository, branch, path string) (branchHead, blobOID string, err error)
}

// EvidenceDocument is one document record inside coord.registry-evidence/v1.
type EvidenceDocument struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	BranchHead string `json:"branchHead,omitempty"`
	BlobOID    string `json:"blobOid,omitempty"`
	Result     string `json:"result"`
}

// EvidenceArtifact is the ephemeral CI artifact. It is never committed.
type EvidenceArtifact struct {
	APIVersion     string             `json:"apiVersion"`
	RepositoryHead string             `json:"repositoryHead"`
	GeneratedAt    string             `json:"generatedAt"`
	Documents      []EvidenceDocument `json:"documents"`
}

var (
	sha256HexRE  = regexp.MustCompile(`(?i)(?:sha256[:\s]+)?([0-9a-f]{64})`)
	reconTokenRE = regexp.MustCompile(`(?i)(?:recon-[a-z0-9._-]+|OWNER\s+20\d{2}-\d{2}-\d{2})`)
)

// Validate parses registryPath as DOCUMENT-REGISTRY.yaml and runs LOCAL checks.
func Validate(registryPath, root string) ([]Violation, error) {
	res, err := ValidateWithOptions(registryPath, Options{Root: root, Mode: ModeLocal})
	if err != nil {
		return nil, err
	}
	return res.Violations, nil
}

// ValidateWithOptions runs the registry checks under the given mode.
func ValidateWithOptions(registryPath string, opts Options) (Result, error) {
	var out Result
	if strings.TrimSpace(opts.Root) == "" {
		return out, fmt.Errorf("root is required")
	}
	if opts.Mode == "" {
		opts.Mode = ModeLocal
	}
	if opts.Mode != ModeLocal && opts.Mode != ModeCIStrict {
		return out, fmt.Errorf("unknown mode %q", opts.Mode)
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}

	f, err := os.Open(registryPath)
	if err != nil {
		return out, fmt.Errorf("open registry: %w", err)
	}
	defer func() { _ = f.Close() }()

	reg, err := governanceio.DecodeStrictYAML[Registry](f)
	if err != nil {
		return out, fmt.Errorf("parse registry: %w", err)
	}

	reader := opts.External
	if reader == nil {
		reader = &gitBlobReader{root: opts.Root, mode: opts.Mode}
	}
	if opts.Mode == ModeCIStrict {
		if strings.TrimSpace(opts.EvidencePath) == "" {
			return out, fmt.Errorf("CI_STRICT requires --evidence path outside the repository")
		}
		if err := refuseEvidenceInsideRoot(opts.Root, opts.EvidencePath); err != nil {
			return out, err
		}
		if err := reader.Fetch(); err != nil {
			out.Violations = append(out.Violations, Violation{
				Code:   "FETCH_FAILED",
				Detail: err.Error(),
			})
			return out, nil
		}
	}

	out = validate(reg, opts, reader)
	if opts.Mode == ModeCIStrict {
		if err := writeEvidence(opts, reader, reg); err != nil {
			return out, err
		}
	}
	return out, nil
}

func validate(reg Registry, opts Options, reader ExternalBlobReader) Result {
	var out Result
	root := opts.Root

	if reg.APIVersion != "coord.dev/v1alpha1" {
		out.Violations = append(out.Violations, Violation{
			Code:   "INVALID_API_VERSION",
			Detail: fmt.Sprintf("expected coord.dev/v1alpha1, got %q", reg.APIVersion),
		})
	}
	if reg.Kind != "DocumentRegistry" {
		out.Violations = append(out.Violations, Violation{
			Code:   "INVALID_KIND",
			Detail: fmt.Sprintf("expected DocumentRegistry, got %q", reg.Kind),
		})
	}

	seenIDs := map[string]bool{}
	seenPaths := map[string]bool{}
	ids := map[string]bool{}
	for _, doc := range reg.Documents {
		if doc.ID != "" {
			ids[doc.ID] = true
		}
	}

	for i, doc := range reg.Documents {
		loc := fmt.Sprintf("doc[%d] id=%q", i, doc.ID)

		if doc.ID == "" {
			out.Violations = append(out.Violations, Violation{Code: "MISSING_ID", Detail: fmt.Sprintf("doc[%d] has no id", i)})
		} else if seenIDs[doc.ID] {
			out.Violations = append(out.Violations, Violation{Code: "DUPLICATE_ID", Detail: fmt.Sprintf("%s: id already seen", loc)})
		} else {
			seenIDs[doc.ID] = true
		}

		if doc.Path == "" {
			out.Violations = append(out.Violations, Violation{Code: "MISSING_PATH", Detail: fmt.Sprintf("%s: no path", loc)})
		} else if seenPaths[doc.Path] {
			out.Violations = append(out.Violations, Violation{Code: "DUPLICATE_PATH", Detail: fmt.Sprintf("%s: path %q already registered", loc, doc.Path)})
		} else {
			seenPaths[doc.Path] = true
		}

		if !validClasses[doc.Class] {
			out.Violations = append(out.Violations, Violation{Code: "INVALID_CLASS", Detail: fmt.Sprintf("%s: unknown class %q", loc, doc.Class)})
		}
		if !validStatuses[doc.Status] {
			out.Violations = append(out.Violations, Violation{Code: "INVALID_STATUS", Detail: fmt.Sprintf("%s: unknown status %q", loc, doc.Status)})
		}

		if nodeEmpty(doc.UsedBy) {
			out.Violations = append(out.Violations, Violation{Code: "MISSING_USED_BY", Detail: fmt.Sprintf("%s: usedBy is empty or absent", loc)})
		}
		if nodeEmpty(doc.ReadWhen) {
			out.Violations = append(out.Violations, Violation{Code: "MISSING_READ_WHEN", Detail: fmt.Sprintf("%s: readWhen is empty or absent", loc)})
		}
		if nodeEmpty(doc.UpdateWhen) {
			out.Violations = append(out.Violations, Violation{Code: "MISSING_UPDATE_WHEN", Detail: fmt.Sprintf("%s: updateWhen is empty or absent", loc)})
		}

		if doc.Status == StatusHistorical && doc.CurrentOperationalAuthority {
			out.Violations = append(out.Violations, Violation{Code: "HISTORICAL_CURRENT_AUTHORITY", Detail: fmt.Sprintf("%s: historical doc cannot be current operational authority", loc)})
		}
		if doc.Status == StatusLegacy && doc.CanonicalProductionAuthority {
			out.Violations = append(out.Violations, Violation{Code: "LEGACY_CANONICAL_AUTHORITY", Detail: fmt.Sprintf("%s: legacy doc cannot be canonical production authority", loc)})
		}

		if doc.Status == StatusProposed &&
			normativeAuthorityAllowlist[doc.NormativeAuthority] &&
			doc.CurrentNormativeAuthority != nil && *doc.CurrentNormativeAuthority {
			out.Violations = append(out.Violations, Violation{
				Code:   "PROPOSED_MANDATORY_AUTHORITY",
				Detail: fmt.Sprintf("%s: proposed doc claims current normative authority %q", loc, doc.NormativeAuthority),
			})
		}

		if doc.Class != "normative" &&
			doc.Status == StatusAccepted &&
			normativeAuthorityAllowlist[doc.NormativeAuthority] &&
			doc.CurrentNormativeAuthority != nil && *doc.CurrentNormativeAuthority &&
			doc.DecisionRef == "" {
			out.Violations = append(out.Violations, Violation{
				Code:   "ACCEPTED_NORM_MISSING_DECISION_REF",
				Detail: fmt.Sprintf("%s: accepted non-normative doc with current authority requires decisionRef", loc),
			})
		}

		if doc.DecisionRef != "" {
			if strings.ContainsAny(doc.DecisionRef, "/\\") || strings.HasPrefix(doc.DecisionRef, ".") {
				out.Violations = append(out.Violations, Violation{
					Code:   "INVALID_DECISION_REF",
					Detail: fmt.Sprintf("%s: decisionRef must be a bare ID (no path separators), got %q", loc, doc.DecisionRef),
				})
			}
		}

		if doc.Status == StatusSuperseded {
			if strings.TrimSpace(doc.SupersededBy) == "" {
				out.Violations = append(out.Violations, Violation{
					Code:   "SUPERSEDED_BY_REQUIRED",
					Detail: fmt.Sprintf("%s: superseded status requires supersededBy", loc),
				})
			} else if strings.ContainsAny(doc.SupersededBy, "/\\") || strings.HasPrefix(doc.SupersededBy, ".") {
				out.Violations = append(out.Violations, Violation{
					Code:   "INVALID_SUPERSEDED_BY",
					Detail: fmt.Sprintf("%s: supersededBy must be a bare document id, got %q", loc, doc.SupersededBy),
				})
			} else if !ids[doc.SupersededBy] {
				out.Violations = append(out.Violations, Violation{
					Code:   "SUPERSEDED_BY_UNKNOWN",
					Detail: fmt.Sprintf("%s: supersededBy %q is not a registered document id", loc, doc.SupersededBy),
				})
			}
		} else if strings.TrimSpace(doc.SupersededBy) != "" {
			out.Violations = append(out.Violations, Violation{
				Code:   "SUPERSEDED_BY_UNEXPECTED",
				Detail: fmt.Sprintf("%s: supersededBy is only valid when status is superseded", loc),
			})
		}

		if doc.Status == StatusHistorical && !hasProvenance(doc) {
			out.Violations = append(out.Violations, Violation{
				Code:   "HISTORICAL_PROVENANCE_MISSING",
				Detail: fmt.Sprintf("%s: historical document requires provenance.sourceDigest and provenance.reconciliationRef, or purpose text with sha256 and a reconciliation reference", loc),
			})
		}

		locationClass, locationOK := resolveLocation(doc)
		if !locationOK {
			out.Violations = append(out.Violations, Violation{
				Code:   "UNKNOWN_LOCATION",
				Detail: fmt.Sprintf("%s: location class must be LOCAL or TRACKED_IN_OTHER_BRANCH", loc),
			})
			continue
		}

		if doc.Path == "" {
			continue
		}
		if pathErr := checkPathContainment(root, doc.Path); pathErr != nil {
			out.Violations = append(out.Violations, Violation{Code: "PATH_ESCAPE", Detail: fmt.Sprintf("%s: %v", loc, pathErr)})
			continue
		}

		switch locationClass {
		case LocationLocal:
			if doc.Location != nil && (doc.Location.Repository != "" || doc.Location.Branch != "") {
				out.Violations = append(out.Violations, Violation{
					Code:   "LOCAL_EXTERNAL_FIELDS",
					Detail: fmt.Sprintf("%s: LOCAL location cannot set repository or branch", loc),
				})
			}
			if requiresLocalFile(doc) {
				full := filepath.Join(root, filepath.Clean(doc.Path))
				if _, err := os.Stat(full); errors.Is(err, os.ErrNotExist) {
					out.Violations = append(out.Violations, Violation{
						Code:   "MISSING_FILE",
						Detail: fmt.Sprintf("%s: registered file not found: %s", loc, doc.Path),
					})
				}
			}
		case LocationTrackedInOtherBranch:
			checkTracked(doc, loc, opts, reader, &out)
		}
	}

	return out
}

func resolveLocation(doc Document) (string, bool) {
	if doc.Location == nil || strings.TrimSpace(doc.Location.Class) == "" {
		return LocationLocal, true
	}
	class := doc.Location.Class
	if !validLocations[class] {
		return class, false
	}
	return class, true
}

// requiresLocalFile: active/proposed LOCAL files are required even when optional.
// historical and superseded never require a local file. Other statuses keep the
// pre-#97 rule: non-optional means the file is required.
func requiresLocalFile(doc Document) bool {
	switch doc.Status {
	case StatusHistorical, StatusSuperseded:
		return false
	case StatusActive, StatusProposed:
		return true
	default:
		return !doc.Optional
	}
}

func checkTracked(doc Document, loc string, opts Options, reader ExternalBlobReader, out *Result) {
	l := doc.Location
	path := doc.Path
	if l != nil && l.Path != "" && l.Path != doc.Path {
		out.Violations = append(out.Violations, Violation{
			Code:   "LOCATION_PATH_MISMATCH",
			Detail: fmt.Sprintf("%s: location.path %q must equal document path %q", loc, l.Path, doc.Path),
		})
		return
	}
	if l == nil {
		out.Violations = append(out.Violations, Violation{
			Code:   "PATH_OUTSIDE_ALLOWLIST",
			Detail: fmt.Sprintf("%s: TRACKED_IN_OTHER_BRANCH requires location.repository and location.branch", loc),
		})
		return
	}
	if l.Repository != allowedExternalRepository || l.Branch != allowedExternalBranch {
		out.Violations = append(out.Violations, Violation{
			Code:   "PATH_OUTSIDE_ALLOWLIST",
			Detail: fmt.Sprintf("%s: TRACKED_IN_OTHER_BRANCH allowlist is %s + %s + %s*", loc, allowedExternalRepository, allowedExternalBranch, allowedExternalPathPrefix),
		})
		return
	}
	if strings.Contains(path, "..") || strings.HasPrefix(path, "/") || !strings.HasPrefix(path, allowedExternalPathPrefix) {
		out.Violations = append(out.Violations, Violation{
			Code:   "PATH_OUTSIDE_ALLOWLIST",
			Detail: fmt.Sprintf("%s: path %q is outside allowlisted prefix %s", loc, path, allowedExternalPathPrefix),
		})
		return
	}

	branchHead, blobOID, err := reader.Read(l.Repository, l.Branch, path)
	if err != nil {
		code := classifyExternalError(err)
		detail := fmt.Sprintf("%s: %v", loc, err)
		if opts.Mode == ModeLocal && (code == "EXTERNAL_REF_UNAVAILABLE" || code == "NOT_EVALUATED") {
			out.Warnings = append(out.Warnings, Violation{Code: "NOT_EVALUATED", Detail: detail})
			return
		}
		if opts.Mode == ModeLocal && code == "EXTERNAL_FILE_MISSING" {
			// Ref available but file missing is a hard failure even locally.
			out.Violations = append(out.Violations, Violation{Code: code, Detail: detail})
			return
		}
		out.Violations = append(out.Violations, Violation{Code: code, Detail: detail})
		return
	}
	_ = branchHead
	_ = blobOID
}

func classifyExternalError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "fetch"):
		return "FETCH_FAILED"
	case strings.Contains(msg, "outside allowlist"):
		return "PATH_OUTSIDE_ALLOWLIST"
	case strings.Contains(msg, "blob unreadable"), strings.Contains(msg, "cat-file"):
		return "BLOB_UNREADABLE"
	case strings.Contains(msg, "file missing"), strings.Contains(msg, "path not in tree"):
		return "EXTERNAL_FILE_MISSING"
	case strings.Contains(msg, "ref unavailable"), strings.Contains(msg, "unknown revision"), strings.Contains(msg, "needed a single revision"):
		return "EXTERNAL_REF_UNAVAILABLE"
	default:
		return "EXTERNAL_REF_UNAVAILABLE"
	}
}

func hasProvenance(doc Document) bool {
	if doc.Provenance != nil {
		digest := normalizeDigest(doc.Provenance.SourceDigest)
		ref := strings.TrimSpace(doc.Provenance.ReconciliationRef)
		if digest != "" && ref != "" && !strings.ContainsAny(ref, "/\\") && !strings.Contains(ref, "..") {
			return true
		}
	}
	text := purposeText(doc.Purpose)
	return sha256HexRE.MatchString(text) && reconTokenRE.MatchString(text)
}

func normalizeDigest(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.ToLower(v), "sha256:")
	v = strings.TrimSpace(v)
	if matched := sha256HexRE.FindStringSubmatch(v); matched != nil {
		return strings.ToLower(matched[1])
	}
	if len(v) == 64 && sha256HexRE.MatchString(v) {
		return strings.ToLower(v)
	}
	return ""
}

func purposeText(n yaml.Node) string {
	switch n.Kind {
	case 0:
		return ""
	case yaml.ScalarNode:
		return n.Value
	case yaml.SequenceNode:
		var parts []string
		for _, c := range n.Content {
			if c != nil && c.Kind == yaml.ScalarNode {
				parts = append(parts, c.Value)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func refuseEvidenceInsideRoot(root, evidencePath string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve root: %w", err)
	}
	absEvidence, err := filepath.Abs(evidencePath)
	if err != nil {
		return fmt.Errorf("resolve evidence path: %w", err)
	}
	rootResolved := absRoot
	if r, err := filepath.EvalSymlinks(absRoot); err == nil {
		rootResolved = r
	}
	evidenceResolved := absEvidence
	if r, err := filepath.EvalSymlinks(filepath.Dir(absEvidence)); err == nil {
		evidenceResolved = filepath.Join(r, filepath.Base(absEvidence))
	}
	sep := string(filepath.Separator)
	if evidenceResolved == rootResolved || strings.HasPrefix(evidenceResolved+sep, rootResolved+sep) {
		return fmt.Errorf("evidence path %q is inside the repository; refuse to write coord.registry-evidence/v1 inside the work tree", evidencePath)
	}
	return nil
}

func writeEvidence(opts Options, reader ExternalBlobReader, reg Registry) error {
	head := opts.RepositoryHead
	if head == "" {
		if h, err := gitOutput(opts.Root, "rev-parse", "HEAD"); err == nil {
			head = strings.TrimSpace(h)
		}
	}
	art := EvidenceArtifact{
		APIVersion:     "coord.registry-evidence/v1",
		RepositoryHead: head,
		GeneratedAt:    opts.Now.UTC().Format(time.RFC3339),
	}
	for _, doc := range reg.Documents {
		class, ok := resolveLocation(doc)
		if !ok || class != LocationTrackedInOtherBranch || doc.Location == nil {
			continue
		}
		entry := EvidenceDocument{ID: doc.ID, Path: doc.Path, Result: "FAIL"}
		branchHead, blobOID, err := reader.Read(doc.Location.Repository, doc.Location.Branch, doc.Path)
		if err == nil {
			entry.BranchHead = branchHead
			entry.BlobOID = blobOID
			entry.Result = "PASS"
		}
		art.Documents = append(art.Documents, entry)
	}
	raw, err := json.MarshalIndent(art, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(opts.EvidencePath), 0o750); err != nil {
		return err
	}
	return os.WriteFile(opts.EvidencePath, append(raw, '\n'), 0o600)
}

// gitBlobReader reads allowlisted refs via git. CI_STRICT fetches the fixed refspec.
type gitBlobReader struct {
	root string
	mode Mode
}

func (g *gitBlobReader) Fetch() error {
	if g.mode != ModeCIStrict {
		return nil
	}
	// Fixed refspec. Branch name from YAML is allowlist-checked before Read.
	cmd := exec.Command("git", "fetch", "--no-tags", "origin", "refs/heads/ops/coord/project:refs/remotes/origin/ops/coord/project")
	cmd.Dir = g.root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("fetch failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (g *gitBlobReader) Read(repository, branch, path string) (string, string, error) {
	if repository != allowedExternalRepository || branch != allowedExternalBranch {
		return "", "", fmt.Errorf("outside allowlist")
	}
	if strings.Contains(path, "..") || !strings.HasPrefix(path, allowedExternalPathPrefix) {
		return "", "", fmt.Errorf("outside allowlist")
	}
	ref := "refs/remotes/origin/" + branch
	branchHead, err := gitOutput(g.root, "rev-parse", "--verify", ref)
	if err != nil {
		// Fall back to the short remote name used by some checkouts.
		branchHead, err = gitOutput(g.root, "rev-parse", "--verify", "origin/"+branch)
		if err != nil {
			return "", "", fmt.Errorf("ref unavailable: %w", err)
		}
		ref = "origin/" + branch
	}
	branchHead = strings.TrimSpace(branchHead)
	blobOID, err := gitOutput(g.root, "rev-parse", "--verify", ref+":"+path)
	if err != nil {
		return "", "", fmt.Errorf("file missing or path not in tree: %w", err)
	}
	blobOID = strings.TrimSpace(blobOID)
	if _, err := gitOutput(g.root, "cat-file", "-e", blobOID); err != nil {
		return "", "", fmt.Errorf("blob unreadable: %w", err)
	}
	return branchHead, blobOID, nil
}

func gitOutput(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
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
