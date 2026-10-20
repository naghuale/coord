package main

import (
	"coord/internal/shadowpilot"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type shadowOutput struct {
	path    string
	digests map[string]string
}

func verifyShadowEvidence(bundle shadowpilot.EvidenceBundle, runID shadowpilot.ShadowRunID) error {
	return bundle.Verify(runID)
}

func newShadowOutput(root, target string) (*shadowOutput, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	target = filepath.Clean(target)
	scratch := filepath.Join(root, ".scratch", "tmp")
	if !strings.HasPrefix(target, scratch+string(os.PathSeparator)) {
		return nil, refuse("output", "new directory beneath project .scratch/tmp required")
	}
	// Refuse every symlink component before creating directories. The final
	// directory is exclusive: replay cannot overwrite an earlier run's artifacts.
	for path := target; path != root; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
			return nil, refuse("output", "symlink or non-directory ancestor prohibited")
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(target, 0700); err != nil {
		return nil, err
	}
	return &shadowOutput{path: target, digests: map[string]string{}}, nil
}

// JSON is a strict YAML subset; the *.yaml records use the same canonical JSON
// encoding as the digested artifacts, without a second serialization preimage.
func (o *shadowOutput) writeJSON(name string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	file, err := os.OpenFile(filepath.Join(o.path, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(raw)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	o.digests[name] = digestBytes(raw)
	return nil
}

type shadowAxis struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}
type shadowPilotReport struct {
	Recommendation        string                             `json:"recommendation"`
	P03AdmissionAuthority bool                               `json:"p0_3AdmissionAuthority"`
	RuntimeDimensions     map[string]shadowAxis              `json:"runtime_dimensions"`
	Model                 shadowpilot.ShadowValidationReport `json:"model_report"`
}

func (r shadowPilotReport) Validate() error {
	if r.P03AdmissionAuthority || r.Recommendation != "NOT_EVALUATED" {
		return refuse("admission", "pilot cannot self-issue P0-3 admission")
	}
	if err := r.Model.Validate(); err != nil {
		return err
	}
	if r.Model.Recommendation == shadowpilot.AdmitP03 {
		return refuse("admission", "model must not claim independent acceptance")
	}
	for _, name := range []string{"runtime_entrypoint", "non_nil_agent_adapter", "production_writes", "github_writes", "credential_mutations", "canonical_store", "daemon_locks", "candidate_comparison", "independent_review", "independent_audit"} {
		axis, ok := r.RuntimeDimensions[name]
		if !ok || (axis.Status != "PASS" && axis.Status != "NOT_EVALUATED" && axis.Status != "UNSAFE_DIVERGENCE") {
			return refuse("dimensions", "missing or invalid explicit axis: "+name)
		}
	}
	for _, name := range []string{"independent_review", "independent_audit"} {
		if r.RuntimeDimensions[name].Status != "NOT_EVALUATED" || r.RuntimeDimensions[name].Reason == "" {
			return refuse("admission", "independent acceptance not performed by pilot")
		}
	}
	return nil
}

func writeShadowReport(output *shadowOutput, a shadowpilot.ShadowAssignment, runtime shadowRuntimeEvidence, comparison shadowpilot.ComparisonResult, recovery shadowpilot.RecoverySummary, bundle shadowpilot.EvidenceBundle, now time.Time) error {
	if err := runtime.Validate(); err != nil {
		return err
	}
	if err := verifyShadowEvidence(bundle, a.ShadowRunID); err != nil {
		return err
	}
	// NewReport independently checks complete dimension coverage and verifies the
	// sealed bundle. No Review/Audit acceptance is fabricated from this run.
	model, err := shadowpilot.NewReport("WP-SV-07", a.SubjectSHA, "coordd-shadow-pilot-v1", 1, now,
		[]shadowpilot.WorkPackageEntry{{Name: "WP-SV-07", Status: shadowpilot.WPPending}}, runtime.RuntimeEvidence, comparison, recovery, false, false, bundle)
	if err != nil {
		return err
	}
	report := shadowPilotReport{Recommendation: "NOT_EVALUATED", RuntimeDimensions: map[string]shadowAxis{}, Model: model}
	for _, axis := range []string{"runtime_entrypoint", "non_nil_agent_adapter", "production_writes", "github_writes", "credential_mutations", "canonical_store", "daemon_locks"} {
		report.RuntimeDimensions[axis] = shadowAxis{Status: "PASS"}
	}
	for _, axis := range []string{"independent_review", "independent_audit"} {
		report.RuntimeDimensions[axis] = shadowAxis{Status: "NOT_EVALUATED", Reason: "live run does not perform independent implementation acceptance"}
	}
	candidateStatus := "PASS"
	for _, d := range comparison.Dimensions {
		if d.Dimension == shadowpilot.DimFindingIdentity && d.Outcome != shadowpilot.OutcomeMatch {
			candidateStatus = string(d.Outcome)
		}
	}
	report.RuntimeDimensions["candidate_comparison"] = shadowAxis{Status: candidateStatus, Reason: "fixture invariant IDs compared to correlated AgentResult"}
	if err := report.Validate(); err != nil {
		return err
	}
	if err := output.writeJSON("REPORT.json", report); err != nil {
		return err
	}
	var integrity strings.Builder
	for _, name := range sortedKeys(output.digests) {
		_, _ = fmt.Fprintf(&integrity, "%s  %s\n", output.digests[name], name)
	}
	return os.WriteFile(filepath.Join(output.path, "INTEGRITY.sha256"), []byte(integrity.String()), 0600)
}
