package actioncontext

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type actionMapFile struct {
	Kind   string `yaml:"kind"`
	Events map[string]struct {
		RequiredDocuments []string `yaml:"requiredDocuments"`
		AllowedActions    []string `yaml:"allowedActions"`
		ForbiddenActions  []string `yaml:"forbiddenActions"`
		ExpectedResults   []string `yaml:"expectedResults"`
	} `yaml:"events"`
}

type documentRegistryFile struct {
	Kind      string             `yaml:"kind"`
	Documents []registryDocument `yaml:"documents"`
}

type contextBudgetsFile struct {
	Budgets map[Role]ContextBudget `yaml:"budgets"`
}

var subjectSHA = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

func Resolve(req ResolveRequest) (ResolveResult, error) {
	root, err := canonicalDir(req.ProjectRoot)
	if err != nil {
		return ResolveResult{}, fmt.Errorf("project root: %w", err)
	}
	registryPath := req.RegistryPath
	if registryPath == "" {
		registryPath = "docs/DOCUMENT-REGISTRY.yaml"
	}
	actionMapPath := req.ActionMapPath
	if actionMapPath == "" {
		actionMapPath = "docs/operations/ACTION-MAP.yaml"
	}
	budgetsPath := req.BudgetsPath
	if budgetsPath == "" {
		budgetsPath = "config/CONTEXT-BUDGETS.yaml"
	}
	registryFile, err := containedExistingFile(root, registryPath)
	if err != nil {
		return ResolveResult{}, fmt.Errorf("document registry: %w", err)
	}
	actionFile, err := containedExistingFile(root, actionMapPath)
	if err != nil {
		return ResolveResult{}, fmt.Errorf("action map: %w", err)
	}
	budgetFile, err := containedExistingFile(root, budgetsPath)
	if err != nil {
		return ResolveResult{}, fmt.Errorf("context budgets: %w", err)
	}

	var actionMap actionMapFile
	if err := readYAML(actionFile, &actionMap); err != nil {
		return ResolveResult{}, fmt.Errorf("decode action map: %w", err)
	}
	var registry documentRegistryFile
	if err := readYAML(registryFile, &registry); err != nil {
		return ResolveResult{}, fmt.Errorf("decode document registry: %w", err)
	}
	var budgets contextBudgetsFile
	if err := readYAML(budgetFile, &budgets); err != nil {
		return ResolveResult{}, fmt.Errorf("decode context budgets: %w", err)
	}
	if actionMap.Kind != "ActionMap" || registry.Kind != "DocumentRegistry" {
		return ResolveResult{}, errors.New("governance document kind is invalid")
	}

	result := ResolveResult{Admission: Admission{Status: StatusInvalidAdmission}}
	if req.AssignmentID == "" {
		result.Admission.Rejections = append(result.Admission.Rejections,
			AuthorityRejection{Reason: ErrSubjectBindingMissing, Detail: "assignmentId"})
	}
	if !subjectSHA.MatchString(req.BaseSHA) {
		result.Admission.Rejections = append(result.Admission.Rejections,
			AuthorityRejection{Reason: ErrSubjectBindingMissing, Detail: "baseSha must be a full lowercase Git SHA"})
	}
	spec, ok := actionMap.Events[string(req.Event)]
	if !ok {
		result.Admission.Rejections = append(result.Admission.Rejections,
			AuthorityRejection{Reason: ErrUnknownEvent, Detail: string(req.Event)})
		return result, nil
	}
	budget, ok := budgets.Budgets[req.Role]
	if !ok {
		return ResolveResult{}, fmt.Errorf("no context budget for role %q", req.Role)
	}

	documentsByID := make(map[string]registryDocument, len(registry.Documents))
	for _, doc := range registry.Documents {
		if doc.ID == "" || doc.Path == "" {
			return ResolveResult{}, errors.New("document registry contains an entry without id or path")
		}
		if _, exists := documentsByID[doc.ID]; exists {
			return ResolveResult{}, fmt.Errorf("document registry has duplicate id %q", doc.ID)
		}
		documentsByID[doc.ID] = doc
	}

	ids := make([]string, 0, len(spec.RequiredDocuments)+len(req.OwningDocumentIDs)+len(req.AdditionalDocumentIDs))
	required := make(map[string]bool, len(spec.RequiredDocuments))
	seen := make(map[string]bool)
	add := func(id string, isRequired bool) {
		if id == "" {
			return
		}
		if isRequired {
			required[id] = true
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, id := range spec.RequiredDocuments {
		add(id, true)
	}
	for _, id := range req.OwningDocumentIDs {
		add(id, false)
	}
	for _, id := range req.AdditionalDocumentIDs {
		add(id, false)
	}

	historicalCount := 0
	for _, id := range ids {
		doc, found := documentsByID[id]
		if !found {
			result.Admission.Missing = append(result.Admission.Missing, id)
			result.Admission.Rejections = append(result.Admission.Rejections,
				AuthorityRejection{DocumentID: id, Reason: ErrDocumentUnregistered})
			continue
		}
		reason, detail := authorityReason(doc, req.Event, req)
		if reason == ErrAuthorityViolation && detail == "event_not_allowed" && !required[id] {
			// An optional document outside this event is not selected and does not
			// invalidate otherwise complete admission.
			continue
		}
		if reason != "" {
			result.Admission.Rejections = append(result.Admission.Rejections,
				AuthorityRejection{DocumentID: id, Reason: reason, Detail: detail})
			continue
		}
		path, err := containedExistingFile(root, doc.Path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				result.Admission.Missing = append(result.Admission.Missing, id)
				result.Admission.Rejections = append(result.Admission.Rejections,
					AuthorityRejection{DocumentID: id, Reason: ErrRequiredDocumentMissing, Detail: doc.Path})
				continue
			}
			code := ErrPathEscape
			if !strings.Contains(err.Error(), "outside project root") {
				code = ErrRequiredDocumentMissing
			}
			result.Admission.Rejections = append(result.Admission.Rejections,
				AuthorityRejection{DocumentID: id, Reason: code, Detail: err.Error()})
			continue
		}
		digest, err := SHA256File(path)
		if err != nil {
			result.Admission.Rejections = append(result.Admission.Rejections,
				AuthorityRejection{DocumentID: id, Reason: ErrRequiredDocumentMissing, Detail: err.Error()})
			continue
		}
		result.Admission.Documents = append(result.Admission.Documents, DocumentBinding{
			ID: id, Path: doc.Path, Digest: digest, Status: doc.Status,
		})
		if doc.Status == DocStatusHistorical {
			historicalCount++
		}
	}

	if len(result.Admission.Documents) > budget.MaxDocuments {
		result.Admission.Rejections = append(result.Admission.Rejections,
			AuthorityRejection{Reason: ErrContextBudgetExceeded,
				Detail: fmt.Sprintf("documents=%d max=%d", len(result.Admission.Documents), budget.MaxDocuments)})
	}
	if historicalCount > budget.MaxHistoricalDocuments {
		result.Admission.Rejections = append(result.Admission.Rejections,
			AuthorityRejection{Reason: ErrHistoricalBudgetExceeded,
				Detail: fmt.Sprintf("historical=%d max=%d", historicalCount, budget.MaxHistoricalDocuments)})
	}
	if len(result.Admission.Rejections) == 0 && len(result.Admission.Missing) == 0 {
		result.Admission.Status = StatusAdmitted
	}
	return result, nil
}

func canonicalDir(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("must be an absolute path")
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
		return "", errors.New("must name a directory")
	}
	return filepath.Clean(resolved), nil
}

func containedExistingFile(root, path string) (string, error) {
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("path %q is absolute and outside project root", path)
	}
	joined := filepath.Clean(filepath.Join(root, path))
	if !withinRoot(joined, root) {
		return "", fmt.Errorf("path %q is outside project root", path)
	}
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", err
	}
	if !withinRoot(resolved, root) {
		return "", fmt.Errorf("path %q resolves outside project root", path)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("path %q is not a regular file", path)
	}
	return resolved, nil
}

func withinRoot(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func readYAML(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, target)
}
