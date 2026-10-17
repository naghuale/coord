package externalaction

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// LocalGit is the third canonical ExternalAction family per
// P0-3-LOCAL-GIT-AND-RUN-RECOVERY-R1. It covers repository, branch, index,
// tree and commit operations that require no credentials and no network.
const LocalGit Family = "LOCAL_GIT"

// LocalGitOperation enumerates the Phase-1 P0-3 operations allowed by
// P0-3-LOCAL-GIT-AND-RUN-RECOVERY-R1. Push and any form of remote write are
// explicitly excluded.
type LocalGitOperation string

const (
	LocalGitObserveRepository     LocalGitOperation = "OBSERVE_REPOSITORY"
	LocalGitCreateIsolatedBranch  LocalGitOperation = "CREATE_ISOLATED_BRANCH"
	LocalGitWriteAllowlistedFiles LocalGitOperation = "WRITE_ALLOWLISTED_FILES"
	LocalGitObserveTree           LocalGitOperation = "OBSERVE_TREE"
	LocalGitCreateCommit          LocalGitOperation = "CREATE_COMMIT"
	LocalGitObserveCommit         LocalGitOperation = "OBSERVE_COMMIT"
)

// LocalGitMarker is embedded in LocalGitActionPayload and carries the
// idempotency identity of the action. Like GitHubMarker it is derived after
// hashing the versioned immutable request and is never an authority-bearing
// input.
type LocalGitMarker struct {
	OperationInstanceID string `json:"operation_instance_id"`
	IdempotencyKey      string `json:"idempotency_key"`
	PayloadDigest       string `json:"payload_digest"`
}

// LocalGitActionPayload is the typed canonical representation of a LOCAL_GIT
// action. One action = one observable side effect. CREATE_ISOLATED_BRANCH,
// WRITE_ALLOWLISTED_FILES and CREATE_COMMIT must be separate actions, each
// with its own observation. This narrows ambiguous state after crash.
//
// Idempotency identity for CREATE_COMMIT is:
//
//	OperationInstanceID + RepositoryID + BaseSHA + Branch + ExpectedOutputTree + ChangeSetDigest
//
// Commit timestamp, process ID and temporary paths are excluded.
type LocalGitActionPayload struct {
	Operation          LocalGitOperation `json:"operation"`
	RepositoryID       string            `json:"repository_id"`
	RepositoryRoot     string            `json:"repository_root"`
	BaseSHA            string            `json:"base_sha,omitempty"`
	Branch             string            `json:"branch,omitempty"`
	AllowedPaths       []string          `json:"allowed_paths,omitempty"`
	ExpectedInputTree  string            `json:"expected_input_tree,omitempty"`
	ExpectedOutputTree string            `json:"expected_output_tree,omitempty"`
	ChangeSetDigest    string            `json:"change_set_digest,omitempty"`
	CommitMessage      string            `json:"commit_message,omitempty"`
	Marker             LocalGitMarker    `json:"marker"`
}

// LocalGitObservation is the crash-safe observation record produced by a
// LOCAL_GIT adapter after the side effect is confirmed. It carries the
// correlation evidence needed to bind a pre-existing commit after restart.
type LocalGitObservation struct {
	Marker               LocalGitMarker `json:"marker"`
	MarkerMatched        bool           `json:"marker_matched"`
	PayloadDigestMatched bool           `json:"payload_digest_matched"`
	RepositoryID         string         `json:"repository_id"`
	ResultBranch         string         `json:"result_branch,omitempty"`
	ResultTree           string         `json:"result_tree,omitempty"`
	ResultCommitSHA      string         `json:"result_commit_sha,omitempty"`
	ResultStatus         string         `json:"result_status"`
	ResponseDigest       string         `json:"response_digest"`
}

// LocalGitPayloadDigest uses a versioned typed preimage without the generated
// marker, matching the GitHub pattern so that envelope metadata and
// caller-supplied whitespace do not alter the immutable logical request.
func LocalGitPayloadDigest(p LocalGitActionPayload) string {
	p.Marker = LocalGitMarker{}
	return Digest(struct {
		Version uint32
		Payload LocalGitActionPayload
	}{ContractVersion, p})
}

// Validate checks that the payload is structurally and semantically correct
// for its declared operation. It enforces the admission guards from
// P0-3-LOCAL-GIT-AND-RUN-RECOVERY-R1.
func (p LocalGitActionPayload) Validate() error {
	if p.RepositoryID == "" {
		return fmt.Errorf("local git: repository_id required")
	}
	if p.RepositoryRoot == "" {
		return fmt.Errorf("local git: repository_root required")
	}
	if !filepath.IsAbs(p.RepositoryRoot) {
		return fmt.Errorf("local git: repository_root must be absolute")
	}
	for _, seg := range []string{"/tmp/", "/private/tmp/", "/var/tmp/"} {
		if strings.HasPrefix(filepath.Clean(p.RepositoryRoot)+"/", seg) {
			return fmt.Errorf("local git: system tmp prohibited")
		}
	}
	if p.BaseSHA != "" && len(p.BaseSHA) != 40 {
		return fmt.Errorf("local git: base_sha must be a 40-character hex SHA")
	}
	switch p.Operation {
	case LocalGitObserveRepository:
		// No additional required fields beyond identity.
	case LocalGitCreateIsolatedBranch:
		if err := requireLocalGitBranch(p); err != nil {
			return err
		}
		if p.BaseSHA == "" {
			return fmt.Errorf("local git: base_sha required for CREATE_ISOLATED_BRANCH")
		}
	case LocalGitWriteAllowlistedFiles:
		if err := requireLocalGitBranch(p); err != nil {
			return err
		}
		if len(p.AllowedPaths) == 0 {
			return fmt.Errorf("local git: allowed_paths required for WRITE_ALLOWLISTED_FILES")
		}
		if err := validateAllowedPaths(p.AllowedPaths); err != nil {
			return err
		}
		if p.ExpectedInputTree == "" {
			return fmt.Errorf("local git: expected_input_tree required for WRITE_ALLOWLISTED_FILES")
		}
		if p.ExpectedOutputTree == "" {
			return fmt.Errorf("local git: expected_output_tree required for WRITE_ALLOWLISTED_FILES")
		}
		if !digestPattern.MatchString(p.ChangeSetDigest) {
			return fmt.Errorf("local git: change_set_digest required for WRITE_ALLOWLISTED_FILES")
		}
	case LocalGitObserveTree:
		if err := requireLocalGitBranch(p); err != nil {
			return err
		}
	case LocalGitCreateCommit:
		if err := requireLocalGitBranch(p); err != nil {
			return err
		}
		if p.BaseSHA == "" {
			return fmt.Errorf("local git: base_sha required for CREATE_COMMIT")
		}
		if p.ExpectedOutputTree == "" {
			return fmt.Errorf("local git: expected_output_tree required for CREATE_COMMIT")
		}
		if !digestPattern.MatchString(p.ChangeSetDigest) {
			return fmt.Errorf("local git: change_set_digest required for CREATE_COMMIT")
		}
		if strings.TrimSpace(p.CommitMessage) == "" {
			return fmt.Errorf("local git: commit_message required for CREATE_COMMIT")
		}
	case LocalGitObserveCommit:
		if err := requireLocalGitBranch(p); err != nil {
			return err
		}
	default:
		return fmt.Errorf("local git: unknown operation %q", p.Operation)
	}
	return nil
}

func requireLocalGitBranch(p LocalGitActionPayload) error {
	if strings.TrimSpace(p.Branch) == "" {
		return fmt.Errorf("local git: branch required for %s", p.Operation)
	}
	lower := strings.ToLower(p.Branch)
	for _, protected := range []string{"main", "master", "refs/heads/main", "refs/heads/master"} {
		if lower == protected {
			return fmt.Errorf("local git: protected branch rejected")
		}
	}
	return nil
}

func validateAllowedPaths(paths []string) error {
	seen := map[string]bool{}
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("local git: empty path in allowed_paths")
		}
		if filepath.IsAbs(p) {
			return fmt.Errorf("local git: absolute path in allowed_paths")
		}
		if strings.Contains(p, "..") {
			return fmt.Errorf("local git: path traversal in allowed_paths")
		}
		if seen[p] {
			return fmt.Errorf("local git: duplicate path in allowed_paths")
		}
		seen[p] = true
	}
	return nil
}

// NewLocalGit constructs a validated LOCAL_GIT ExternalAction. It follows the
// same two-pass pattern as NewGitHub: the marker is derived from the marker-
// free payload so the digest has no self-reference.
func NewLocalGit(id string, p LocalGitActionPayload, at time.Time) (Action, error) {
	p.Marker = LocalGitMarker{}
	request, err := json.Marshal(p)
	if err != nil {
		return Action{}, err
	}
	a := Action{
		ID:                  id,
		OperationInstanceID: id,
		Family:              LocalGit,
		SchemaVersion:       ContractVersion,
		Kind:                Kind(p.Operation),
		Request:             request,
		Status:              Pending,
		MaxAttempts:         3,
		CreatedAt:           at,
		UpdatedAt:           at,
		NextAttemptAt:       at,
	}
	a.Key = Key(a)
	p.Marker = LocalGitMarker{id, a.Key, LocalGitPayloadDigest(p)}
	a.Request, err = json.Marshal(p)
	if err != nil {
		return Action{}, err
	}
	a.RequestDigest = Digest(a.Request)
	return a, a.Validate()
}

// LocalGitPayload decodes and validates the LocalGitActionPayload from this
// action. Fails if the family is not LOCAL_GIT.
func (a Action) LocalGitPayload() (LocalGitActionPayload, error) {
	var p LocalGitActionPayload
	if a.Family != LocalGit {
		return p, fmt.Errorf("local git payload requires LOCAL_GIT family")
	}
	if err := decodeRequest(a.Request, &p); err != nil {
		return p, err
	}
	return p, p.Validate()
}

// validateLocalGit is called from Action.Validate when Family == LocalGit.
func (a Action) validateLocalGit() error {
	p, err := a.LocalGitPayload()
	if err != nil {
		return err
	}
	if a.Kind != Kind(p.Operation) || a.Binding != (Binding{}) || a.ParentID != "" {
		return fmt.Errorf("mixed action family binding")
	}
	if p.Marker.OperationInstanceID != a.OperationInstanceID {
		return fmt.Errorf("MARKER_OPERATION_INSTANCE_MISMATCH")
	}
	if p.Marker.IdempotencyKey != a.Key {
		return fmt.Errorf("MARKER_IDEMPOTENCY_KEY_MISMATCH")
	}
	if p.Marker.PayloadDigest != LocalGitPayloadDigest(p) {
		return fmt.Errorf("MARKER_PAYLOAD_DIGEST_MISMATCH")
	}
	switch a.Status {
	case Pending, Claimed, Dispatching, Dispatched, Ambiguous, Observed, Completed, Failed, Blocked, Fenced:
	default:
		return fmt.Errorf("unknown action status")
	}
	if a.Status == Blocked && a.Blocker == "" {
		return fmt.Errorf("blocker missing")
	}
	if a.Status == Completed || a.Status == Observed {
		if a.Result == nil || a.Result.LocalGit == nil {
			return fmt.Errorf("positive local git observation missing")
		}
		if a.Result.Evidence == "" || a.Result.Outcome != "OBSERVED" || a.Result.StopMethod != "" || a.Result.Stopped {
			return fmt.Errorf("invalid local git result envelope")
		}
		o := a.Result.LocalGit
		if !o.MarkerMatched {
			return fmt.Errorf("marker not matched")
		}
		if !o.PayloadDigestMatched {
			return fmt.Errorf("payload digest not matched")
		}
		if o.Marker != p.Marker {
			return fmt.Errorf("observation marker binding mismatch")
		}
		if o.RepositoryID != p.RepositoryID {
			return fmt.Errorf("observation repository binding mismatch")
		}
		if o.ResultStatus != "PASS" || !digestPattern.MatchString(o.ResponseDigest) {
			return fmt.Errorf("positive observation invalid")
		}
	}
	return nil
}
