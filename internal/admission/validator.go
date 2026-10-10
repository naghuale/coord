package admission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"coord/internal/actioncontext"
)

const (
	StatusAdmitted              = "ADMITTED"
	StatusInvalidAdmission      = "INVALID_ADMISSION"
	ErrBootstrapNotAdmitted     = "BOOTSTRAP_NOT_ADMITTED"
	ErrActionContextNotAdmitted = "ACTION_CONTEXT_NOT_ADMITTED"
	ErrWorkspaceBindingInvalid  = "WORKSPACE_BINDING_INVALID"
	ErrPathEscape               = "PATH_ESCAPE"
	ErrBaseSHAMismatch          = "BASE_SHA_MISMATCH"
	ErrExecutableNotAllowed     = "EXECUTABLE_NOT_ALLOWED"
	ErrExecutableInvalid        = "EXECUTABLE_INVALID"
	ErrSideEffectsBlocked       = "REAL_SIDE_EFFECTS_BLOCKED"
	ErrAssignmentInvalid        = "ASSIGNMENT_INVALID"
	ErrSessionBindingInvalid    = "SESSION_BINDING_INVALID"
)

var fullSHA = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)

type ValidateOptions struct {
	RequireBaseSHAMatch bool
	ExecutablePath      string
	ExecutableAllowlist []string
	RealSideEffects     bool
	DryRun              bool
}

type AdmissionResult struct {
	Result string   `json:"result"`
	Errors []string `json:"errors,omitempty"`
	DryRun bool     `json:"dryRun"`
}

type actionContextPayload struct {
	Admission struct {
		Status    string           `json:"status"`
		Result    string           `json:"result"`
		Documents []documentDigest `json:"documents"`
	} `json:"admission"`
	Registry  *documentDigest `json:"registry"`
	ActionMap *documentDigest `json:"actionMap"`
	Documents struct {
		Required []documentDigest `json:"required"`
	} `json:"documents"`
}

type documentDigest struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

func Validate(bootstrap AgentBootstrap, opts ValidateOptions) (AdmissionResult, error) {
	result := AdmissionResult{Result: StatusInvalidAdmission, DryRun: opts.DryRun || !opts.RealSideEffects}
	add := func(code string, detail string) {
		if detail == "" {
			result.Errors = append(result.Errors, code)
		} else {
			result.Errors = append(result.Errors, code+": "+detail)
		}
	}

	if bootstrap.BootstrapValidation.Result != StatusAdmitted {
		add(ErrBootstrapNotAdmitted, "bootstrapValidation.result must be ADMITTED")
	}
	if bootstrap.Kind != "AgentBootstrap" {
		add(ErrBootstrapNotAdmitted, "kind must be AgentBootstrap")
	}
	if bootstrap.Assignment.AssignmentID == "" {
		add(ErrAssignmentInvalid, "assignmentId is required")
	}
	if bootstrap.Session.Role == "" || bootstrap.Session.AgentID == "" {
		add(ErrSessionBindingInvalid, "role and agentId are required")
	}
	if len(bytes.TrimSpace(bootstrap.ActionContext)) == 0 || bytes.Equal(bytes.TrimSpace(bootstrap.ActionContext), []byte("null")) {
		add(ErrActionContextNotAdmitted, "actionContext is missing")
	} else {
		var context actionContextPayload
		if err := json.Unmarshal(bootstrap.ActionContext, &context); err != nil {
			add(ErrActionContextNotAdmitted, "actionContext is invalid JSON: "+err.Error())
		} else if !actionContextAdmitted(context.Admission.Status, context.Admission.Result) {
			add(ErrActionContextNotAdmitted, "actionContext admission is not ADMITTED")
		} else {
			verifyContextDigests(bootstrap.WorkspaceBinding.ProjectRoot, context, add)
		}
	}

	projectRoot, err := canonicalDirectory(bootstrap.WorkspaceBinding.ProjectRoot)
	if err != nil {
		add(ErrWorkspaceBindingInvalid, "projectRoot: "+err.Error())
	} else {
		repositoryRoot, repoErr := containedDirectory(projectRoot, bootstrap.WorkspaceBinding.RepositoryRoot, true)
		if repoErr != nil {
			add(ErrWorkspaceBindingInvalid, "repositoryRoot: "+repoErr.Error())
		}
		if _, worktreeErr := containedDirectory(projectRoot, bootstrap.WorkspaceBinding.AssignedWorktree, true); worktreeErr != nil {
			add(ErrWorkspaceBindingInvalid, "assignedWorktree: "+worktreeErr.Error())
		}
		if opts.RequireBaseSHAMatch {
			if repoErr != nil {
				add(ErrBaseSHAMismatch, "repositoryRoot is unavailable")
			} else {
				verifyBaseSHA(repositoryRoot, bootstrap.Assignment.BaseSHA, add)
			}
		}
	}

	if opts.ExecutablePath != "" {
		verifyExecutable(opts.ExecutablePath, opts.ExecutableAllowlist, add)
	}
	if opts.RealSideEffects {
		add(ErrSideEffectsBlocked, "launcher admission only permits dry-run")
	}

	if len(result.Errors) == 0 {
		result.Result = StatusAdmitted
	}
	return result, nil
}

func actionContextAdmitted(status, result string) bool {
	if status == "" && result == "" {
		return false
	}
	return (status == "" || status == StatusAdmitted) && (result == "" || result == StatusAdmitted)
}

func canonicalDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("path must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return filepath.Clean(resolved), nil
}

func containedDirectory(root, candidate string, requireStrictChild bool) (string, error) {
	if !filepath.IsAbs(candidate) {
		return "", errors.New("path must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || (requireStrictChild && rel == ".") {
		return "", fmt.Errorf("%s is outside projectRoot", candidate)
	}
	return filepath.Clean(resolved), nil
}

func verifyBaseSHA(repositoryRoot, expected string, add func(string, string)) {
	if !fullSHA.MatchString(expected) {
		add(ErrBaseSHAMismatch, "assignment.baseSha must be a full Git SHA")
		return
	}
	cmd := exec.Command("git", "-C", repositoryRoot, "rev-parse", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		add(ErrBaseSHAMismatch, "git rev-parse HEAD failed: "+err.Error())
		return
	}
	if strings.TrimSpace(string(output)) != expected {
		add(ErrBaseSHAMismatch, fmt.Sprintf("expected %s, observed %s", expected, strings.TrimSpace(string(output))))
	}
}

func verifyExecutable(path string, allowlist []string, add func(string, string)) {
	if !filepath.IsAbs(path) {
		add(ErrExecutableInvalid, "executable path must be absolute")
		return
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		add(ErrExecutableInvalid, err.Error())
		return
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		add(ErrExecutableInvalid, "path is not an existing regular file")
		return
	}
	if info.Mode().Perm()&0o111 == 0 {
		add(ErrExecutableInvalid, "file has no executable permission")
		return
	}
	allowed := false
	for _, candidate := range allowlist {
		if !filepath.IsAbs(candidate) {
			continue
		}
		approved, err := filepath.EvalSymlinks(candidate)
		if err == nil && filepath.Clean(approved) == filepath.Clean(resolved) {
			allowed = true
			break
		}
	}
	if !allowed {
		add(ErrExecutableNotAllowed, resolved)
	}
}

func verifyContextDigests(projectRoot string, context actionContextPayload, add func(string, string)) {
	root, err := canonicalDirectory(projectRoot)
	if err != nil {
		// The project root error is reported by the workspace binding check too.
		return
	}
	bindings := make([]documentDigest, 0, 2+len(context.Documents.Required))
	if context.Registry != nil {
		bindings = append(bindings, *context.Registry)
	}
	if context.ActionMap != nil {
		bindings = append(bindings, *context.ActionMap)
	}
	bindings = append(bindings, context.Documents.Required...)
	if len(context.Documents.Required) == 0 {
		bindings = append(bindings, context.Admission.Documents...)
	}
	for _, binding := range bindings {
		if binding.Path == "" || binding.Digest == "" {
			add(ErrActionContextNotAdmitted, "selected document is missing path or digest")
			continue
		}
		path := binding.Path
		if filepath.IsAbs(path) {
			add(actioncontext.ErrPathEscape, path)
			continue
		}
		joined := filepath.Clean(filepath.Join(root, path))
		rel, relErr := filepath.Rel(root, joined)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			add(actioncontext.ErrPathEscape, path)
			continue
		}
		resolved, err := filepath.EvalSymlinks(joined)
		if err != nil {
			add(ErrActionContextNotAdmitted, path+": "+err.Error())
			continue
		}
		rel, relErr = filepath.Rel(root, resolved)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			add(actioncontext.ErrPathEscape, path)
			continue
		}
		observed, err := actioncontext.SHA256File(resolved)
		if err != nil {
			add(ErrActionContextNotAdmitted, path+": "+err.Error())
			continue
		}
		if observed != binding.Digest {
			add(actioncontext.ErrDigestDrift, path)
		}
	}
}
