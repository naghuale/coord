package main

import (
	"context"
	"coord/internal/shadowpilot"
	"coord/telepathy"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type startupProbe struct {
	mu                  sync.Mutex
	EntryPointCalls     int    `json:"entry_point_calls"`
	StoreOpens          int    `json:"store_opens"`
	DaemonLocks         int    `json:"daemon_locks"`
	Dispatches          int    `json:"dispatches"`
	AdapterKind         string `json:"adapter_kind"`
	ProductionWrites    int    `json:"production_writes"`
	GitHubWrites        int    `json:"github_writes"`
	CredentialMutations int    `json:"credential_mutations"`
}

type probeKey struct{}

func (p *startupProbe) EntryPointEntered() { p.mu.Lock(); defer p.mu.Unlock(); p.EntryPointCalls++ }

func (p *startupProbe) StoreOpened()        { p.mu.Lock(); defer p.mu.Unlock(); p.StoreOpens++ }
func (p *startupProbe) DaemonLockAcquired() { p.mu.Lock(); defer p.mu.Unlock(); p.DaemonLocks++ }
func (p *startupProbe) AdapterSelected(kind string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.AdapterKind = kind
}
func (p *startupProbe) DispatchCompleted(string) { p.mu.Lock(); defer p.mu.Unlock(); p.Dispatches++ }

func observeDaemonStartup(ctx context.Context, storeOpen bool) {
	if p, ok := ctx.Value(probeKey{}).(*startupProbe); ok {
		if storeOpen {
			p.StoreOpened()
		} else {
			p.DaemonLockAcquired()
		}
	}
}

type shadowRuntimeEvidence struct {
	shadowpilot.RuntimeEvidence
	CanonicalStoreOpened   bool   `json:"canonical_store_opened"`
	DaemonLockAcquired     bool   `json:"daemon_lock_acquired"`
	NonNilAgentAdapterUsed bool   `json:"non_nil_agent_adapter_used"`
	AgentAdapterKind       string `json:"agent_adapter_kind"`
	CredentialMutations    int    `json:"credential_mutations"`
	StoreOpenCount         int    `json:"store_open_count"`
	DaemonLockCount        int    `json:"daemon_lock_count"`
	DispatchCount          int    `json:"dispatch_count"`
}

func (p *startupProbe) evidence() shadowRuntimeEvidence {
	p.mu.Lock()
	defer p.mu.Unlock()
	return shadowRuntimeEvidence{
		RuntimeEvidence:      shadowpilot.RuntimeEvidence{RealEntryPointUsed: p.EntryPointCalls > 0, AdapterKind: p.AdapterKind, FakeOrLive: "live", ProductionWrites: p.ProductionWrites, GitHubWrites: p.GitHubWrites, CredentialUse: p.CredentialMutations},
		CanonicalStoreOpened: p.StoreOpens > 0, DaemonLockAcquired: p.DaemonLocks > 0,
		NonNilAgentAdapterUsed: p.AdapterKind == "telepathy" && p.Dispatches > 0,
		AgentAdapterKind:       p.AdapterKind, CredentialMutations: p.CredentialMutations,
		StoreOpenCount: p.StoreOpens, DaemonLockCount: p.DaemonLocks, DispatchCount: p.Dispatches,
	}
}

func (r shadowRuntimeEvidence) Validate() error {
	if err := r.RuntimeEvidence.Validate(); err != nil {
		return err
	}
	if r.CanonicalStoreOpened || r.StoreOpenCount != 0 {
		return refuse("canonicalStore", "opening canonical store prohibited")
	}
	if r.DaemonLockAcquired || r.DaemonLockCount != 0 {
		return refuse("daemonLocks", "acquiring daemon locks prohibited")
	}
	if !r.NonNilAgentAdapterUsed || r.AgentAdapterKind != "telepathy" || r.DispatchCount != 1 {
		return refuse("adapter", "one confirmed concrete Telepathy dispatch required")
	}
	if r.CredentialMutations != 0 {
		return refuse("credentials", "credential mutation prohibited")
	}
	return nil
}

type shadowInvariant struct {
	ID          string `yaml:"id" json:"id"`
	Description string `yaml:"description" json:"description"`
}
type shadowFixture struct {
	Scenario string `yaml:"scenario"`
	Decision string `yaml:"decision"`
	Subject  struct {
		SHA        string `yaml:"sha"`
		Repository string `yaml:"repository"`
	} `yaml:"subject"`
	Task struct {
		Mode      string `yaml:"mode"`
		Operation string `yaml:"operation"`
	} `yaml:"task"`
	Invariants   []shadowInvariant `yaml:"expectedInvariants"`
	OutputSchema struct {
		Kind           string   `yaml:"kind"`
		RequiredFields []string `yaml:"requiredFields"`
	} `yaml:"outputSchema"`
	Writes map[string]string `yaml:"writes"`
}

type shadowOptions struct {
	Input, Adapter, Agent, Session, Output, Server, TelepathyHome, ReplyTo, ReviewerAgent string
	Timeout                                                                               time.Duration
}

type shadowDeps struct {
	Root        string
	Now         func() time.Time
	Probe       *startupProbe
	OpenAdapter func(context.Context, shadowOptions) (shadowAgentAdapter, error)
}

func parseShadowOptions(args []string, stderr io.Writer) (shadowOptions, error) {
	var o shadowOptions
	if len(args) < 2 || args[0] != "shadow-pilot" || args[1] != "run" {
		return o, refuse("command", "usage: coordd shadow-pilot run --input <fixture> --adapter telepathy --executor-agent <id> --executor-session <session> --output <scratch-dir>")
	}
	f := flag.NewFlagSet("coordd shadow-pilot run", flag.ContinueOnError)
	f.SetOutput(stderr)
	f.StringVar(&o.Input, "input", "", "immutable input fixture")
	f.StringVar(&o.Adapter, "adapter", "", "telepathy (live only)")
	f.StringVar(&o.Agent, "executor-agent", "", "canonical executor agent ID")
	f.StringVar(&o.Session, "executor-session", "", "exact executor session ID")
	f.StringVar(&o.ReviewerAgent, "reviewer-agent", "", "assigned reviewer identity for executor admission; no review acceptance implied")
	f.StringVar(&o.Output, "output", ".scratch/tmp/wp-sv-07-live-run", "new project-local scratch directory")
	f.StringVar(&o.Server, "telepathy-server", os.Getenv("COORD_SHADOW_TELEPATHY_SERVER"), "verified Telepathy 0.8.0 or 0.9.0 MCP server")
	f.StringVar(&o.TelepathyHome, "telepathy-home", os.Getenv("TELEPATHY_HOME"), "deliberately selected existing Telepathy home")
	f.StringVar(&o.ReplyTo, "reply-to", "", "sender Telepathy address for correlated replies")
	f.DurationVar(&o.Timeout, "timeout", 5*time.Minute, "dispatch/result deadline")
	if err := f.Parse(args[2:]); err != nil {
		return o, err
	}
	if f.NArg() != 0 || o.Input == "" || o.Agent == "" || o.Session == "" || o.ReviewerAgent == "" || o.Timeout <= 0 {
		return o, refuse("command", "input, canonical agent, exact session and positive timeout required; no positional or daemon arguments")
	}
	if o.Adapter != "telepathy" {
		return o, refuse("adapter", "only live Telepathy adapter supported")
	}
	return o, nil
}

func openShadowTelepathy(ctx context.Context, o shadowOptions) (shadowAgentAdapter, error) {
	if o.Server == "" || o.TelepathyHome == "" {
		return nil, refuse("adapter", "telepathy-server and telepathy-home required for live MCP")
	}
	env := make([]string, 0, len(os.Environ())+2)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "TELEPATHY_HOME=") && !strings.HasPrefix(value, "TELEPATHY_AGENT_PID=") {
			env = append(env, value)
		}
	}
	env = append(env, "TELEPATHY_HOME="+o.TelepathyHome, fmt.Sprintf("TELEPATHY_AGENT_PID=%d", os.Getpid()))
	p, err := telepathy.Start(ctx, []string{"node", o.Server, "--agent", "opencode"}, "", env)
	if err != nil {
		return nil, err
	}
	listed, err := p.Call(ctx, "list_peers", map[string]any{})
	if err != nil {
		return nil, errors.Join(err, p.Close())
	}
	replyTo, err := shadowReplyAddress(listed)
	if err != nil {
		return nil, errors.Join(err, p.Close())
	}
	if o.ReplyTo != "" && o.ReplyTo != replyTo {
		return nil, errors.Join(refuse("replyTo", "does not match MCP sender session"), p.Close())
	}
	return &telepathyShadowAgentAdapter{tools: p, process: p, to: o.Agent + ":" + o.Session, replyTo: replyTo}, nil
}

func shadowPilotCLI(args []string, stderr io.Writer, deps shadowDeps) int {
	o, err := parseShadowOptions(args, stderr)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), o.Timeout)
		defer cancel()
		ctx = context.WithValue(ctx, probeKey{}, deps.Probe)
		err = runShadowPilot(ctx, o, deps)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	return 0
}

func digestBytes(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func immutableSHA(sha string) bool {
	_, err := hex.DecodeString(sha)
	return err == nil && (len(sha) == 40 || len(sha) == 64) && sha == strings.ToLower(sha)
}

func loadShadowFixture(path string) (shadowFixture, []byte, error) {
	var f shadowFixture
	raw, err := os.ReadFile(path)
	if err != nil {
		return f, nil, err
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&f); err != nil {
		return f, nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return f, nil, refuse("input", "one fixture document required")
	}
	if !immutableSHA(f.Subject.SHA) {
		return f, nil, refuse("subjectSHA", "immutable lowercase hex commit SHA required")
	}
	if f.Task.Mode != "READ_ONLY" || len(f.Invariants) == 0 {
		return f, nil, refuse("input", "read-only task and invariant list required")
	}
	if f.OutputSchema.Kind != shadowpilot.AgentResultKind {
		return f, nil, refuse("input", "typed AgentResult output schema required")
	}
	for _, key := range []string{"repository", "github", "main", "issueState", "canonicalStore", "credentials"} {
		if f.Writes[key] != "prohibited" {
			return f, nil, refuse("input", "all external writes must be prohibited")
		}
	}
	seen := map[string]bool{}
	for _, inv := range f.Invariants {
		if inv.ID == "" || inv.Description == "" || seen[inv.ID] {
			return f, nil, refuse("input", "unique named invariants required")
		}
		seen[inv.ID] = true
	}
	return f, raw, nil
}

func runShadowPilot(ctx context.Context, o shadowOptions, deps shadowDeps) (err error) {
	if deps.Probe == nil || deps.Now == nil || deps.OpenAdapter == nil {
		return refuse("entrypoint", "runtime dependencies required")
	}
	fixture, input, err := loadShadowFixture(o.Input)
	if err != nil {
		return err
	}
	object, objectErr := exec.CommandContext(ctx, "git", "-C", deps.Root, "cat-file", "-t", fixture.Subject.SHA).Output()
	if objectErr != nil || strings.TrimSpace(string(object)) != "commit" {
		return refuse("subjectSHA", "immutable subject must resolve to a local commit object")
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	id := hex.EncodeToString(nonce[:])
	started := deps.Now().UTC()
	a := shadowpilot.ShadowAssignment{ShadowRunID: shadowpilot.ShadowRunID("shadow-" + id), AssignmentID: "assignment-" + id, AgentID: o.Agent, SessionID: o.Session, Generation: 1, SubjectSHA: fixture.Subject.SHA, InputDigest: digestBytes(input), Role: "executor", CreatedAt: started}
	if err = a.Validate(); err != nil {
		return refuse("assignment", err.Error())
	}
	if a.SessionID != strings.TrimSpace(a.SessionID) {
		return refuse("sessionID", "exact unpadded session required")
	}
	// Production transport startup is deliberately not reachable from this path.
	adapter, err := deps.OpenAdapter(ctx, o)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeShadowAdapter(adapter)) }()
	if err = requireLiveShadowAdapter(adapter); err != nil {
		return err
	}
	deps.Probe.AdapterSelected("telepathy")
	output, err := newShadowOutput(deps.Root, o.Output)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = output.writeJSON("REFUSAL.json", map[string]any{"error": err.Error(), "executionCompleted": false, "evidenceAccepted": false, "recommendation": "NOT_EVALUATED"})
		}
	}()
	replyTo := adapter.(*telepathyShadowAgentAdapter).replyTo
	if replyTo == "" {
		return refuse("replyTo", "MCP sender identity required")
	}
	d := shadowDispatch{CommandDomain: shadowDomain, DispatchID: "dispatch-" + id, ShadowRunID: a.ShadowRunID, AssignmentID: a.AssignmentID, SubjectSHA: a.SubjectSHA, BaselineSHA: a.SubjectSHA, Generation: a.Generation, InputDigest: a.InputDigest, ReadOnly: true, Assignment: a, Invariants: fixture.Invariants, ReplyTo: replyTo, InputFixture: string(input)}
	if err = output.writeJSON("RUN.yaml", map[string]any{"shadowRunID": a.ShadowRunID, "startedAt": started, "productionWrites": "prohibited", "p0_3AdmissionAuthority": false}); err != nil {
		return err
	}
	if err = output.writeJSON("ASSIGNMENT.yaml", a); err != nil {
		return err
	}
	// Admission checks the caller-supplied reviewer identity, but performs no
	// review and grants no acceptance from that identity.
	execution, err := shadowpilot.NewShadowExecution(a, a.SubjectSHA, []string{o.ReviewerAgent})
	if err != nil {
		return err
	}
	if err = execution.Start(started); err != nil {
		return err
	}
	if err = adapter.Dispatch(ctx, d); err != nil {
		return err
	}
	deps.Probe.DispatchCompleted(d.DispatchID)
	if err = output.writeJSON("DISPATCH.yaml", d); err != nil {
		return err
	}
	r, err := adapter.Receive(ctx, d.DispatchID)
	if err != nil {
		return err
	}
	if err = validateShadowResult(d, r); err != nil {
		return err
	}
	runtime := deps.Probe.evidence()
	if err = runtime.Validate(); err != nil {
		return err
	}
	if err = execution.Complete(r.Result, deps.Now().UTC()); err != nil {
		return err
	}
	if err = output.writeJSON("RESULT.yaml", r); err != nil {
		return err
	}
	if err = output.writeJSON("RUNTIME-EVIDENCE.yaml", runtime); err != nil {
		return err
	}
	recovery, err := replayShadowCheckpoint(a, execution)
	if err != nil {
		return err
	}
	dims := compareShadowRun(d, r, fixture, recovery)
	comparison, err := shadowpilot.NewComparisonResult(a.ShadowRunID, dims)
	if err != nil {
		return err
	}
	if err = output.writeJSON("COMPARISON.json", comparison); err != nil {
		return err
	}
	command := map[string]any{"argv": os.Args, "startedAt": started, "endedAt": deps.Now().UTC()}
	if err = output.writeJSON("COMMANDS.json", command); err != nil {
		return err
	}
	provenance, err := shadowProvenance(deps.Root, d, r, started, deps.Now().UTC(), runtime, output.digests)
	if err != nil {
		return err
	}
	bundle, err := shadowpilot.NewEvidenceBundle(a.ShadowRunID)
	if err != nil {
		return err
	}
	for _, name := range sortedKeys(output.digests) {
		if err = bundle.AddRef(shadowpilot.EvidenceRef{Kind: "artifact_sha256", ID: name + ":" + output.digests[name]}); err != nil {
			return err
		}
	}
	if err = bundle.Seal(provenance); err != nil {
		return err
	}
	if err = verifyShadowEvidence(bundle, a.ShadowRunID); err != nil {
		return err
	}
	if err = output.writeJSON("EVIDENCE-MANIFEST.yaml", bundle); err != nil {
		return err
	}
	return writeShadowReport(output, a, runtime, comparison, recovery, bundle, deps.Now().UTC())
}

func replayShadowCheckpoint(a shadowpilot.ShadowAssignment, e shadowpilot.ShadowExecution) (shadowpilot.RecoverySummary, error) {
	raw, err := json.Marshal(shadowpilot.ShadowCheckpoint{Assignment: &a, Execution: &e})
	if err != nil {
		return shadowpilot.RecoverySummary{}, err
	}
	var cp shadowpilot.ShadowCheckpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return shadowpilot.RecoverySummary{}, err
	}
	plan := shadowpilot.PlanRecovery(cp, nil)
	if !plan.Is(shadowpilot.RecoveryResumeGraph) {
		return shadowpilot.RecoverySummary{}, refuse("restart", "completed checkpoint must not redispatch")
	}
	return shadowpilot.RecoverySummary{CrashPointsTested: 1}, nil
}

func compareShadowRun(d shadowDispatch, r shadowResult, f shadowFixture, recovery shadowpilot.RecoverySummary) []shadowpilot.DimensionResult {
	match := shadowpilot.OutcomeMatch
	findings := shadowpilot.OutcomeMatch
	expected, actual := []string{}, []string{}
	for _, inv := range f.Invariants {
		expected = append(expected, inv.ID)
	}
	for _, finding := range r.Result.Findings {
		actual = append(actual, finding.ID)
	}
	slices.Sort(expected)
	slices.Sort(actual)
	if !reflect.DeepEqual(expected, actual) {
		findings = shadowpilot.OutcomeUnsafeDivergence
	}
	return []shadowpilot.DimensionResult{
		{Dimension: shadowpilot.DimAssignmentIdentity, Outcome: match, Note: "all ten envelope/result bindings checked"},
		{Dimension: shadowpilot.DimExactSubject, Outcome: match, Note: d.SubjectSHA + " baseline=" + d.BaselineSHA},
		{Dimension: shadowpilot.DimRoleIndependence, Outcome: shadowpilot.OutcomeNotEvaluated, Note: "live run does not perform independent implementation review or audit"},
		{Dimension: shadowpilot.DimResultSchema, Outcome: match, Note: "strict JSON decoder and AgentResult.Validate"},
		{Dimension: shadowpilot.DimFindingIdentity, Outcome: findings, Note: "compare returned finding IDs to immutable fixture invariant IDs"},
		{Dimension: shadowpilot.DimRationalePreserved, Outcome: shadowpilot.OutcomeNotEvaluated, Note: "no review/audit Discussion Graph executed"},
		{Dimension: shadowpilot.DimEvidenceBinding, Outcome: match, Note: "artifact digests are sealed and verified before report publication"},
		{Dimension: shadowpilot.DimReadinessState, Outcome: shadowpilot.OutcomeNotEvaluated, Note: "independent Review and Audit acceptance not supplied by this run"},
		{Dimension: shadowpilot.DimRestartBehavior, Outcome: shadowpilot.OutcomeNotEvaluated, Note: fmt.Sprintf("%d completed-checkpoint model replay observed; live process restart not exercised", recovery.CrashPointsTested)},
		{Dimension: shadowpilot.DimDuplicateSideEffect, Outcome: shadowpilot.OutcomeNotEvaluated, Note: "single live dispatch; model replay does not prove end-to-end live crash deduplication"},
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func shadowProvenance(root string, d shadowDispatch, r shadowResult, start, end time.Time, runtime shadowRuntimeEvidence, digests map[string]string) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		return "", err
	}
	commit, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	argv, err := json.Marshal(os.Args)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(map[string]any{"coorddCommit": strings.TrimSpace(string(commit)), "binaryDigest": digestBytes(binary), "commandDigest": digestBytes(argv), "candidateSHA": d.SubjectSHA, "baselineSHA": d.BaselineSHA, "inputDigest": d.InputDigest, "adapterKind": "telepathy", "mcpClientVersion": "telepathy>=0.8.0/protocol2024-11-05", "dispatchID": d.DispatchID, "agentID": r.Result.AgentID, "sessionID": r.Result.SessionID, "resultDigest": r.ResultDigest, "startedAt": start, "endedAt": end, "runtime": runtime, "artifactDigests": digests})
	return string(raw), err
}
