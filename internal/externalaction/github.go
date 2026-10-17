package externalaction

import (
	"bytes"
	"coord/task"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type GitHubOperation string

type EvidenceClass string

const EvidenceFixtureNotLive EvidenceClass = "FIXTURE_NOT_LIVE"

const (
	CreateIssue        GitHubOperation = "CREATE_ISSUE"
	UpdateIssue        GitHubOperation = "UPDATE_ISSUE"
	CommentIssue       GitHubOperation = "COMMENT_ISSUE"
	CloseIssue         GitHubOperation = "CLOSE_ISSUE"
	ReopenIssue        GitHubOperation = "REOPEN_ISSUE"
	CreatePullRequest  GitHubOperation = "CREATE_PULL_REQUEST"
	UpdatePullRequest  GitHubOperation = "UPDATE_PULL_REQUEST"
	CommentPullRequest GitHubOperation = "COMMENT_PULL_REQUEST"
	MergePullRequest   GitHubOperation = "MERGE_PULL_REQUEST"
	ObserveIssue       GitHubOperation = "OBSERVE_ISSUE"
	ObservePullRequest GitHubOperation = "OBSERVE_PULL_REQUEST"
	ObservePipeline    GitHubOperation = "OBSERVE_PIPELINE"
)

type GitHubTarget struct {
	Repository        string `json:"repository"`
	IssueNumber       uint64 `json:"issue_number,omitempty"`
	PullRequestNumber uint64 `json:"pull_request_number,omitempty"`
	BaseBranch        string `json:"base_branch,omitempty"`
	HeadBranch        string `json:"head_branch,omitempty"`
	ExpectedHead      string `json:"expected_head,omitempty"`
	ExpectedState     string `json:"expected_state"`
}
type GitHubRequest struct {
	Title       string   `json:"title,omitempty"`
	Body        string   `json:"body,omitempty"`
	Comment     string   `json:"comment,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	MergeMethod string   `json:"merge_method,omitempty"`
}
type GitHubMarker struct {
	OperationInstanceID string `json:"operation_instance_id"`
	IdempotencyKey      string `json:"idempotency_key"`
	PayloadDigest       string `json:"payload_digest"`
}
type GitHubActionPayload struct {
	Operation          GitHubOperation            `json:"operation"`
	Origin             *task.ExternalActionOrigin `json:"origin,omitempty"`
	ProjectID          string                     `json:"project_id"`
	OriginAssignmentID string                     `json:"origin_assignment_id"`
	ScopeDigest        string                     `json:"scope_digest"`
	Target             GitHubTarget               `json:"target"`
	Request            GitHubRequest              `json:"request"`
	MergeGateRef       string                     `json:"merge_gate_ref,omitempty"`
	Pipeline           *PipelineSelector          `json:"pipeline,omitempty"`
	// Marker is derived after hashing the versioned immutable request, so the
	// digest has no self-reference. It is never an authority-bearing input.
	Marker GitHubMarker `json:"marker"`
}
type GitHubObservation struct {
	Marker               GitHubMarker         `json:"marker"`
	MarkerMatched        bool                 `json:"marker_matched"`
	PayloadDigestMatched bool                 `json:"payload_digest_matched"`
	Target               GitHubTarget         `json:"target"`
	ExternalID           string               `json:"external_id"`
	State                string               `json:"state"`
	ResponseDigest       string               `json:"response_digest"`
	ResultStatus         string               `json:"result_status"`
	Pipeline             *PipelineObservation `json:"pipeline,omitempty"`
}

// GitHubFixture is a closed, credential-free in-memory record used only by
// deterministic tests and explicitly configured fixture mode.
type GitHubFixture struct {
	Operation     GitHubOperation      `json:"operation"`
	Target        GitHubTarget         `json:"target"`
	ExternalID    string               `json:"external_id"`
	State         string               `json:"state"`
	Marker        *GitHubMarker        `json:"marker,omitempty"`
	PayloadDigest string               `json:"payload_digest,omitempty"`
	Pipeline      *PipelineObservation `json:"pipeline,omitempty"`
}

func (f GitHubFixture) Validate() error {
	if f.ExternalID == "" || !repositoryPattern.MatchString(f.Target.Repository) {
		return fmt.Errorf("invalid fixture identity")
	}
	switch f.Operation {
	case ObserveIssue:
		if f.Target.IssueNumber == 0 || f.Target.PullRequestNumber != 0 || (f.State != "OPEN" && f.State != "CLOSED") {
			return fmt.Errorf("invalid fixture Issue identity")
		}
	case ObservePullRequest:
		if f.Target.PullRequestNumber == 0 || f.Target.IssueNumber != 0 || !headPattern.MatchString(f.Target.ExpectedHead) || (f.State != "OPEN" && f.State != "CLOSED" && f.State != "MERGED") {
			return fmt.Errorf("invalid fixture PR identity")
		}
	case ObservePipeline:
		if f.Pipeline == nil || f.Pipeline.Selector.Validate() != nil {
			return fmt.Errorf("invalid fixture pipeline selector")
		}
		if f.Pipeline.Selector.Repository != f.Target.Repository {
			return fmt.Errorf("fixture pipeline selector repository mismatch")
		}
	default:
		return fmt.Errorf("fixture writes and unknown operations are disabled")
	}
	if (f.Marker == nil) != (f.PayloadDigest == "") {
		return fmt.Errorf("fixture marker and payload digest must be present together")
	}
	if f.Marker != nil && (f.Marker.OperationInstanceID == "" || f.Marker.IdempotencyKey == "" || !digestPattern.MatchString(f.Marker.PayloadDigest) || f.PayloadDigest != f.Marker.PayloadDigest) {
		return fmt.Errorf("invalid fixture marker")
	}
	return nil
}

// MergeGate is resolved by the canonical owner, never supplied by a model in
// the request. Absence of a canonical source is a denied capability.
type MergeGate struct {
	Repository        string
	PullRequestNumber uint64
	ExpectedHead      string
	State             string
	Superseded        bool
}
type GateLookup func(string) (MergeGate, bool)

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var headPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var gatePattern = regexp.MustCompile(`^gate:[A-Za-z0-9._:/-]+$`)

func decodeRequest(data json.RawMessage, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing payload")
	}
	return nil
}

// GitHubPayloadDigest uses a versioned typed preimage without the generated
// marker. Envelope/lifecycle metadata and caller-supplied whitespace do not
// alter the immutable logical request.
func GitHubPayloadDigest(p GitHubActionPayload) string {
	p.Marker = GitHubMarker{}
	return Digest(struct {
		Version uint32
		Payload GitHubActionPayload
	}{ContractVersion, p})
}
func (p GitHubActionPayload) Validate() error {
	if p.ProjectID == "" || p.OriginAssignmentID == "" || !digestPattern.MatchString(p.ScopeDigest) || !repositoryPattern.MatchString(p.Target.Repository) {
		return fmt.Errorf("invalid GitHub origin/target")
	}
	if p.Origin != nil {
		if err := p.Origin.Validate(); err != nil {
			return fmt.Errorf("invalid typed GitHub origin: %w", err)
		}
		if p.Origin.ProjectID != p.ProjectID || (p.Origin.AssignmentID != "" && p.Origin.AssignmentID != p.OriginAssignmentID) {
			return fmt.Errorf("typed GitHub origin differs from payload identity")
		}
	}
	switch p.Target.ExpectedState {
	case "OPEN", "CLOSED", "MERGED", "ANY":
	default:
		return fmt.Errorf("invalid expected external state")
	}
	if len(p.Request.Title) > 256 || len(p.Request.Body) > 65536 || len(p.Request.Comment) > 65536 {
		return fmt.Errorf("GitHub request too large")
	}
	if !utf8.ValidString(p.Request.Title) || !utf8.ValidString(p.Request.Body) || !utf8.ValidString(p.Request.Comment) {
		return fmt.Errorf("invalid UTF-8")
	}
	labels := map[string]bool{}
	for _, label := range p.Request.Labels {
		if label == "" || len(label) > 100 || !utf8.ValidString(label) || labels[label] {
			return fmt.Errorf("invalid label")
		}
		labels[label] = true
	}
	switch p.Operation {
	case CreateIssue:
		if p.Request.Title == "" || p.Target.IssueNumber != 0 || p.Target.PullRequestNumber != 0 {
			return fmt.Errorf("invalid issue creation")
		}
	case UpdateIssue, CommentIssue, CloseIssue, ReopenIssue, ObserveIssue:
		if p.Target.IssueNumber == 0 || p.Target.PullRequestNumber != 0 {
			return fmt.Errorf("issue identity missing")
		}
	case ObservePipeline:
		if p.Pipeline == nil || p.Target.IssueNumber != 0 || p.Target.PullRequestNumber != 0 || p.Target.Repository != p.Pipeline.Repository {
			return fmt.Errorf("invalid pipeline selector")
		}
		if err := p.Pipeline.Validate(); err != nil {
			return err
		}
	case CreatePullRequest:
		if p.Request.Title == "" || p.Target.BaseBranch == "" || p.Target.HeadBranch == "" || p.Target.PullRequestNumber != 0 {
			return fmt.Errorf("invalid PR creation")
		}
	case UpdatePullRequest, CommentPullRequest, MergePullRequest, ObservePullRequest:
		if p.Target.PullRequestNumber == 0 || p.Target.IssueNumber != 0 {
			return fmt.Errorf("PR identity missing")
		}
	default:
		return fmt.Errorf("unknown GitHub operation")
	}
	if p.Operation != ObservePipeline && p.Pipeline != nil {
		return fmt.Errorf("pipeline selector on non-pipeline operation")
	}
	switch p.Operation {
	case CreatePullRequest, UpdatePullRequest, CommentPullRequest, MergePullRequest, ObservePullRequest:
		if !headPattern.MatchString(p.Target.ExpectedHead) {
			return fmt.Errorf("exact PR head missing")
		}
	}
	if (p.Operation == CommentIssue || p.Operation == CommentPullRequest) && strings.TrimSpace(p.Request.Comment) == "" {
		return fmt.Errorf("comment missing")
	}
	if p.Operation == MergePullRequest {
		if len(p.MergeGateRef) > 256 || !gatePattern.MatchString(p.MergeGateRef) {
			return fmt.Errorf("MERGE_GATE_MISSING")
		}
		switch p.Request.MergeMethod {
		case "MERGE", "SQUASH", "REBASE":
		default:
			return fmt.Errorf("merge method missing")
		}
	} else if p.MergeGateRef != "" || p.Request.MergeMethod != "" {
		return fmt.Errorf("merge fields on non-merge operation")
	}
	return nil
}
func (a Action) GitHubPayload() (GitHubActionPayload, error) {
	var p GitHubActionPayload
	if a.Family != GitHub {
		return p, fmt.Errorf("GitHub payload requires GITHUB family")
	}
	if err := decodeRequest(a.Request, &p); err != nil {
		return p, err
	}
	return p, p.Validate()
}

func (a Action) CanonicalOrigin() (task.ExternalActionOrigin, bool, error) {
	p, err := a.GitHubPayload()
	if err != nil {
		return task.ExternalActionOrigin{}, false, err
	}
	if p.Origin == nil {
		return task.ExternalActionOrigin{}, false, nil
	}
	return *p.Origin, true, nil
}
func (a Action) validateGitHub() error {
	p, err := a.GitHubPayload()
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
	if p.Marker.PayloadDigest != GitHubPayloadDigest(p) {
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
		if a.Result == nil || a.Result.GitHub == nil {
			return fmt.Errorf("positive observation missing")
		}
		if a.Result.Evidence == "" || a.Result.Outcome != "OBSERVED" || a.Result.EvidenceClass != string(EvidenceFixtureNotLive) || a.Result.StopMethod != "" || a.Result.Stopped {
			return fmt.Errorf("invalid GitHub result envelope")
		}
		o := a.Result.GitHub
		if a.Status == Observed && o.ResultStatus == "NOT_EVALUATED" {
			if (p.Operation != ObserveIssue && p.Operation != ObservePullRequest) || a.Result.Evidence != "LEGACY_EXTERNAL_PROVENANCE_INCOMPLETE" || o.MarkerMatched || o.PayloadDigestMatched || o.Marker != (GitHubMarker{}) || o.ExternalID == "" || !digestPattern.MatchString(o.ResponseDigest) || o.Target != p.Target {
				return fmt.Errorf("invalid legacy provenance observation")
			}
			return nil
		}
		if o.Pipeline != nil {
			if p.Operation != ObservePipeline || p.Pipeline == nil || o.Pipeline.Selector.Digest() != p.Pipeline.Digest() {
				return fmt.Errorf("pipeline observation selector mismatch")
			}
			state, evaluation := MapPipelineState(string(o.Pipeline.State))
			if state != o.Pipeline.State || evaluation != o.Pipeline.Evaluation {
				return fmt.Errorf("pipeline state/evaluation mismatch")
			}
			if !o.MarkerMatched || !o.PayloadDigestMatched || o.Marker != p.Marker || o.Target != p.Target || o.ResultStatus != "PASS" || o.ExternalID == "" || !digestPattern.MatchString(o.ResponseDigest) {
				return fmt.Errorf("pipeline observation not positively bound")
			}
			return nil
		}
		if p.Operation == ObservePipeline {
			return fmt.Errorf("pipeline action requires a typed pipeline observation")
		}
		if !o.MarkerMatched {
			return fmt.Errorf("marker not matched")
		}
		if !o.PayloadDigestMatched {
			return fmt.Errorf("payload digest not matched")
		}
		if o.Marker != p.Marker || o.Target != p.Target {
			return fmt.Errorf("observation binding mismatch")
		}
		if o.ResultStatus != "PASS" || o.ExternalID == "" || !digestPattern.MatchString(o.ResponseDigest) {
			return fmt.Errorf("positive observation invalid")
		}
		if o.State != "OPEN" && o.State != "CLOSED" && o.State != "MERGED" {
			return fmt.Errorf("external state unknown")
		}
		if p.Target.ExpectedState != "ANY" && o.State != p.Target.ExpectedState {
			return fmt.Errorf("expected state mismatch")
		}
	}
	return nil
}
func ValidateMergeGate(p GitHubActionPayload, lookup GateLookup) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Operation != MergePullRequest {
		return nil
	}
	if lookup == nil {
		return fmt.Errorf("MERGE_GATE_NOT_FOUND")
	}
	g, ok := lookup(p.MergeGateRef)
	if !ok {
		return fmt.Errorf("MERGE_GATE_NOT_FOUND")
	}
	if g.Superseded {
		return fmt.Errorf("MERGE_GATE_SUPERSEDED")
	}
	if g.State != "ACCEPTED" {
		return fmt.Errorf("MERGE_GATE_NOT_ACCEPTED")
	}
	if g.Repository != p.Target.Repository {
		return fmt.Errorf("MERGE_GATE_REPOSITORY_MISMATCH")
	}
	if g.PullRequestNumber != p.Target.PullRequestNumber {
		return fmt.Errorf("MERGE_GATE_PULL_REQUEST_MISMATCH")
	}
	if g.ExpectedHead != p.Target.ExpectedHead {
		return fmt.Errorf("MERGE_GATE_SUBJECT_MISMATCH")
	}
	return nil
}

func NewGitHub(id string, p GitHubActionPayload, at time.Time) (Action, error) {
	p.Marker = GitHubMarker{}
	request, err := json.Marshal(p)
	if err != nil {
		return Action{}, err
	}
	a := Action{ID: id, OperationInstanceID: id, Family: GitHub, SchemaVersion: ContractVersion,
		Kind: Kind(p.Operation), Request: request, Status: Pending, MaxAttempts: 3, CreatedAt: at, UpdatedAt: at, NextAttemptAt: at}
	a.Key = Key(a)
	p.Marker = GitHubMarker{id, a.Key, GitHubPayloadDigest(p)}
	a.Request, err = json.Marshal(p)
	if err != nil {
		return Action{}, err
	}
	a.RequestDigest = Digest(a.Request)
	return a, a.Validate()
}
