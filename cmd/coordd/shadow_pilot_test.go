//go:build unix

package main

import (
	"bufio"
	"bytes"
	"context"
	"coord/internal/coorddconfig"
	"coord/internal/shadowpilot"
	"coord/telepathy"
	"coord/transport"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type shadowTestTools struct {
	dispatch       shadowDispatch
	probe          *startupProbe
	mutate         func(*shadowResult)
	failDispatch   bool
	malformed      bool
	beforeDispatch func()
}

func shadowReply(d shadowDispatch) shadowResult {
	a := d.Assignment
	findings := []shadowpilot.Finding{}
	for _, inv := range d.Invariants {
		findings = append(findings, shadowpilot.Finding{ID: inv.ID, Summary: inv.Description})
	}
	r := shadowpilot.AgentResult{Kind: shadowpilot.AgentResultKind, AssignmentID: a.AssignmentID, AgentID: a.AgentID, SessionID: a.SessionID, Generation: a.Generation, SubjectSHA: a.SubjectSHA, InputDigest: a.InputDigest, Findings: findings, CompletedAt: a.CreatedAt.Add(time.Second)}
	digest, _ := shadowpilot.AgentResultDigest(r)
	return shadowResult{CommandDomain: shadowDomain, DispatchID: d.DispatchID, ShadowRunID: a.ShadowRunID, BaselineSHA: d.BaselineSHA, ResultDigest: digest, Result: r}
}

func (s *shadowTestTools) Call(_ context.Context, name string, args map[string]any) (string, error) {
	switch name {
	case "send_message":
		if s.beforeDispatch != nil {
			s.beforeDispatch()
		}
		if s.probe.evidence().NonNilAgentAdapterUsed {
			return "", errors.New("adapter use claimed before confirmed dispatch")
		}
		_, tail, ok := strings.Cut(args["message"].(string), "BEGIN_COORD_SHADOW_ASSIGNMENT\n")
		if !ok {
			return "", errors.New("missing dispatch envelope")
		}
		raw, _, ok := strings.Cut(tail, "\nEND_COORD_SHADOW_ASSIGNMENT")
		if !ok {
			return "", errors.New("unclosed dispatch envelope")
		}
		if err := json.Unmarshal([]byte(raw), &s.dispatch); err != nil {
			return "", err
		}
		if args["to"] != s.dispatch.Assignment.AgentID+":"+s.dispatch.Assignment.SessionID || !s.dispatch.ReadOnly || s.dispatch.CommandDomain != shadowDomain || digestBytes([]byte(s.dispatch.InputFixture)) != s.dispatch.InputDigest {
			return "", errors.New("wrong assignment recipient, domain, read-only flag or immutable input")
		}
		if s.failDispatch {
			return "", errors.New("dispatch refused")
		}
		return "delivered", nil
	case "read_messages":
		if s.malformed {
			return "BEGIN_COORD_SHADOW_RESULT\nall good\nEND_COORD_SHADOW_RESULT", nil
		}
		r := shadowReply(s.dispatch)
		if s.mutate != nil {
			s.mutate(&r)
			r.ResultDigest, _ = shadowpilot.AgentResultDigest(r.Result)
		}
		raw, _ := json.Marshal(r)
		return "BEGIN_COORD_SHADOW_RESULT\n" + string(raw) + "\nEND_COORD_SHADOW_RESULT", nil
	default:
		return "", fmt.Errorf("forbidden MCP tool %s", name)
	}
}

func shadowTestDeps(t *testing.T) (shadowDeps, *shadowTestTools, []string, string) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(root, ".scratch", "tmp")
	if err := os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(scratch, "shadow-cli-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	output := filepath.Join(dir, "run")
	probe := &startupProbe{}
	tools := &shadowTestTools{probe: probe}
	deps := shadowDeps{Root: root, Now: func() time.Time { return time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC) }, Probe: probe}
	deps.OpenAdapter = func(context.Context, shadowOptions) (shadowAgentAdapter, error) {
		return &telepathyShadowAgentAdapter{tools: tools, to: "codex:test-session", replyTo: "opencode:shadow-host"}, nil
	}
	args := []string{"shadow-pilot", "run", "--input", filepath.Join(root, "testdata/shadow-validation/input.yaml"), "--adapter", "telepathy", "--executor-agent", "codex", "--executor-session", "test-session", "--reviewer-agent", "opencode", "--reply-to", "opencode:shadow-host", "--output", output, "--timeout", "5s"}
	return deps, tools, args, output
}

func shadowNeverDaemon(t *testing.T) transportFactory {
	t.Helper()
	return func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
		t.Error("shadow command entered daemon transport startup")
		return nil, nil, errors.New("daemon forbidden")
	}
}

func TestShadowCLICompletesTypedRoundTripWithoutProductionStartup(t *testing.T) {
	deps, tools, args, output := shadowTestDeps(t)
	var stderr bytes.Buffer
	if code := mainRunWithShadowDeps(args, shadowNeverDaemon(t), &stderr, &deps); code != 0 {
		t.Fatalf("exit=%d: %s", code, stderr.String())
	}
	if got := deps.Probe.evidence(); got.CanonicalStoreOpened || got.DaemonLockAcquired || !got.RealEntryPointUsed || !got.NonNilAgentAdapterUsed {
		t.Fatalf("runtime=%+v", got)
	}
	for _, name := range []string{"RUN.yaml", "ASSIGNMENT.yaml", "DISPATCH.yaml", "RESULT.yaml", "RUNTIME-EVIDENCE.yaml", "EVIDENCE-MANIFEST.yaml", "COMPARISON.json", "REPORT.json", "COMMANDS.json", "INTEGRITY.sha256"} {
		if _, err := os.Stat(filepath.Join(output, name)); err != nil {
			t.Fatal(err)
		}
	}
	var bundle shadowpilot.EvidenceBundle
	decodeShadowArtifact(t, output, "EVIDENCE-MANIFEST.yaml", &bundle)
	if err := bundle.Verify(tools.dispatch.ShadowRunID); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Verify("foreign-run"); err == nil {
		t.Fatal("cross-run bundle accepted")
	}
	if err := verifyShadowEvidence(bundle, "foreign-run"); err == nil {
		t.Fatal("pilot verifier accepted foreign run")
	}
	unsealed := bundle
	unsealed.Provenance = ""
	unsealed.BundleDigest = ""
	if err := verifyShadowEvidence(unsealed, bundle.ShadowRunID); err == nil {
		t.Fatal("pilot verifier accepted unsealed evidence")
	}
	for _, ref := range bundle.Manifest {
		parts := strings.SplitN(ref.ID, ":", 2)
		raw, err := os.ReadFile(filepath.Join(output, parts[0]))
		if err != nil || len(parts) != 2 || digestBytes(raw) != parts[1] {
			t.Fatalf("bad evidence artifact %v: %v", ref, err)
		}
	}
	var report shadowPilotReport
	decodeShadowArtifact(t, output, "REPORT.json", &report)
	if len(report.RuntimeDimensions) != 10 {
		t.Fatalf("runtime dimension count=%d, want 10", len(report.RuntimeDimensions))
	}
	var comparison shadowpilot.ComparisonResult
	decodeShadowArtifact(t, output, "COMPARISON.json", &comparison)
	for _, dim := range comparison.Dimensions {
		if dim.Dimension == shadowpilot.DimRestartBehavior && dim.Outcome != shadowpilot.OutcomeNotEvaluated {
			t.Fatal("model replay claimed live restart proof")
		}
	}
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
	if report.Recommendation != "NOT_EVALUATED" || report.P03AdmissionAuthority || report.Model.Recommendation == shadowpilot.AdmitP03 || report.Model.Comparison.DimensionsTotal != 10 {
		t.Fatalf("self-issued admission or missing dimension: %+v", report)
	}
	integrity, err := os.ReadFile(filepath.Join(output, "INTEGRITY.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(integrity)), "\n") {
		digest, name, ok := strings.Cut(line, "  ")
		raw, err := os.ReadFile(filepath.Join(output, name))
		if !ok || err != nil || digestBytes(raw) != digest {
			t.Fatalf("integrity mismatch: %s (%v)", line, err)
		}
	}
}

func decodeShadowArtifact(t *testing.T, root, name string, value any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, value); err != nil {
		t.Fatal(err)
	}
}

func TestShadowExactCommandRejectsUnknownAndMixedModes(t *testing.T) {
	for _, args := range [][]string{{"shadow-pilot"}, {"shadow-pilot", "unknown"}, {"shadow-pilot", "run", "-config", "daemon.yaml"}, {"-config", "daemon.yaml", "--adapter", "telepathy"}, {"--input", "fixture"}} {
		var stderr bytes.Buffer
		if code := mainRunWithWriter(args, shadowNeverDaemon(t), &stderr); code != 2 {
			t.Fatalf("args=%v exit=%d", args, code)
		}
	}
}

func TestShadowRuntimeProbePinsEveryAuthorityBoundary(t *testing.T) {
	for _, name := range []string{"entrypoint", "store", "lock", "adapter", "dispatch", "production", "github", "credentials"} {
		t.Run(name, func(t *testing.T) {
			p := &startupProbe{}
			p.EntryPointEntered()
			p.AdapterSelected("telepathy")
			p.DispatchCompleted("dispatch")
			if err := p.evidence().Validate(); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "entrypoint":
				p.EntryPointCalls = 0
			case "store":
				p.StoreOpened()
			case "lock":
				p.DaemonLockAcquired()
			case "adapter":
				p.AdapterKind = "fake"
			case "dispatch":
				p.Dispatches = 0
			case "production":
				p.ProductionWrites++
			case "github":
				p.GitHubWrites++
			case "credentials":
				p.CredentialMutations++
			}
			if err := p.evidence().Validate(); err == nil {
				t.Fatalf("%s breach accepted", name)
			}
		})
	}
}

func TestShadowFailedDispatchCannotClaimAdapterUseOrSeal(t *testing.T) {
	deps, tools, args, output := shadowTestDeps(t)
	tools.failDispatch = true
	var stderr bytes.Buffer
	if code := mainRunWithShadowDeps(args, shadowNeverDaemon(t), &stderr, &deps); code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if deps.Probe.evidence().NonNilAgentAdapterUsed {
		t.Fatal("failed dispatch claimed adapter use")
	}
	if _, err := os.Stat(filepath.Join(output, "EVIDENCE-MANIFEST.yaml")); !os.IsNotExist(err) {
		t.Fatal("failed dispatch sealed evidence")
	}
}

type fakeShadowAdapter struct{}

func (*fakeShadowAdapter) Dispatch(context.Context, shadowDispatch) error { return nil }
func (*fakeShadowAdapter) Receive(context.Context, string) (shadowResult, error) {
	return shadowResult{}, nil
}

func TestShadowLiveModeRejectsNilFakeAndNilMCP(t *testing.T) {
	var nilLive *telepathyShadowAgentAdapter
	var nilTools *shadowTestTools
	for _, adapter := range []shadowAgentAdapter{nil, nilLive, &fakeShadowAdapter{}, &telepathyShadowAgentAdapter{}, &telepathyShadowAgentAdapter{tools: nilTools}} {
		if err := requireLiveShadowAdapter(adapter); err == nil {
			t.Fatalf("invalid adapter %T accepted", adapter)
		}
	}
	deps, _, args, _ := shadowTestDeps(t)
	deps.OpenAdapter = func(context.Context, shadowOptions) (shadowAgentAdapter, error) { return nil, nil }
	if code := mainRunWithShadowDeps(args, shadowNeverDaemon(t), io.Discard, &deps); code != 2 {
		t.Fatalf("nil adapter exit=%d", code)
	}
}

func TestShadowDecoderHandlesShadowRefusal(t *testing.T) {
	// An agent refusal must surface the agent's own field/reason, not "unknown field 'kind'".
	for _, tc := range []struct {
		raw        string
		wantField  string
		wantReason string
	}{
		{`{"kind":"ShadowRefusal","field":"sessionID","reason":"session_id mismatch"}`, "sessionID", "session_id mismatch"},
		{`{"kind":"ShadowRefusal","field":"assignment","reason":"bad input"}`, "assignment", "bad input"},
	} {
		tc := tc
		_, err := decodeShadowResult(tc.raw)
		if err == nil {
			t.Fatalf("ShadowRefusal %q accepted", tc.raw)
		}
		var ref *ShadowRefusal
		if !errors.As(err, &ref) {
			t.Fatalf("ShadowRefusal %q: want *ShadowRefusal, got %T: %v", tc.raw, err, err)
		}
		if ref.Field != tc.wantField {
			t.Fatalf("ShadowRefusal %q: field=%q want %q", tc.raw, ref.Field, tc.wantField)
		}
		if !strings.Contains(ref.Reason, tc.wantReason) {
			t.Fatalf("ShadowRefusal %q: reason=%q want %q", tc.raw, ref.Reason, tc.wantReason)
		}
	}
	// Refusal without field must fail clearly, not panic.
	if _, err := decodeShadowResult(`{"kind":"ShadowRefusal"}`); err == nil {
		t.Fatal("ShadowRefusal without field accepted")
	}
	// Unknown kind is not a ShadowRefusal; strict decoder must reject unknown fields.
	if _, err := decodeShadowResult(`{"kind":"UnknownKind","field":"x"}`); err == nil {
		t.Fatal("unknown kind with extra field accepted")
	}
}

func TestShadowStrictDecoderRejectsFreeTextAndExtraFields(t *testing.T) {
	for _, raw := range []string{"all good", `{"unknown":"ignored"}`, `{} {}`} {
		if _, err := decodeShadowResult(raw); err == nil {
			t.Fatalf("malformed result %q accepted", raw)
		}
	}
	deps, tools, args, output := shadowTestDeps(t)
	tools.malformed = true
	if code := mainRunWithShadowDeps(args, shadowNeverDaemon(t), io.Discard, &deps); code != 2 {
		t.Fatalf("free-text exit=%d", code)
	}
	if _, err := os.Stat(filepath.Join(output, "EVIDENCE-MANIFEST.yaml")); !os.IsNotExist(err) {
		t.Fatal("free-text result sealed evidence")
	}
}

func shadowBindingMutations() map[string]func(*shadowResult) {
	return map[string]func(*shadowResult){
		"assignmentID":  func(r *shadowResult) { r.Result.AssignmentID += "-wrong" },
		"agentID":       func(r *shadowResult) { r.Result.AgentID = "CODEX" },
		"sessionID":     func(r *shadowResult) { r.Result.SessionID += "-wrong" },
		"generation":    func(r *shadowResult) { r.Result.Generation++ },
		"subjectSHA":    func(r *shadowResult) { r.Result.SubjectSHA = strings.Repeat("b", 40) },
		"inputDigest":   func(r *shadowResult) { r.Result.InputDigest += "-wrong" },
		"shadowRunID":   func(r *shadowResult) { r.ShadowRunID += "-wrong" },
		"baselineSHA":   func(r *shadowResult) { r.BaselineSHA = strings.Repeat("c", 40) },
		"dispatchID":    func(r *shadowResult) { r.DispatchID += "-wrong" },
		"commandDomain": func(r *shadowResult) { r.CommandDomain = "production" },
	}
}

func TestShadowEveryIdentityBindingHasTypedRefusal(t *testing.T) {
	deps, _, args, _ := shadowTestDeps(t)
	o, err := parseShadowOptions(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	f, input, err := loadShadowFixture(o.Input)
	if err != nil {
		t.Fatal(err)
	}
	a := shadowpilot.ShadowAssignment{ShadowRunID: "test-run", AssignmentID: "test-assignment", AgentID: "codex", SessionID: "test-session", Generation: 1, SubjectSHA: f.Subject.SHA, InputDigest: digestBytes(input), CreatedAt: deps.Now(), Role: "executor"}
	d := shadowDispatch{CommandDomain: shadowDomain, DispatchID: "test-dispatch", ShadowRunID: a.ShadowRunID, SubjectSHA: a.SubjectSHA, BaselineSHA: a.SubjectSHA, Assignment: a, Invariants: f.Invariants}
	for field, mutate := range shadowBindingMutations() {
		t.Run(field, func(t *testing.T) {
			r := shadowReply(d)
			mutate(&r)
			r.ResultDigest, _ = shadowpilot.AgentResultDigest(r.Result)
			err := validateShadowResult(d, r)
			var refusal *ShadowRefusal
			if !errors.As(err, &refusal) || refusal.Field != field {
				t.Fatalf("error=%v want typed %s refusal", err, field)
			}
		})
	}
	r := shadowReply(d)
	r.ResultDigest = "tampered"
	if err := validateShadowResult(d, r); err == nil {
		t.Fatal("tampered digest accepted")
	}
}

func TestShadowMismatchedResultCannotCompleteOrSeal(t *testing.T) {
	for field, mutate := range shadowBindingMutations() {
		t.Run(field, func(t *testing.T) {
			deps, tools, args, output := shadowTestDeps(t)
			tools.mutate = mutate
			var stderr bytes.Buffer
			if code := mainRunWithShadowDeps(args, shadowNeverDaemon(t), &stderr, &deps); code != 2 {
				t.Fatalf("exit=%d: %s", code, stderr.String())
			}
			for _, name := range []string{"RESULT.yaml", "EVIDENCE-MANIFEST.yaml", "REPORT.json"} {
				if _, err := os.Stat(filepath.Join(output, name)); !os.IsNotExist(err) {
					t.Fatalf("%s created after binding mismatch", name)
				}
			}
		})
	}
}

func TestShadowMutableSubjectAndUnsafeOutputRejected(t *testing.T) {
	deps, _, args, _ := shadowTestDeps(t)
	input, err := os.ReadFile(args[3])
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "input.yaml")
	input = bytes.ReplaceAll(input, []byte("b4334cd2438837140bc112b97cba49eb1a478f08"), []byte("main"))
	if err = os.WriteFile(path, input, 0600); err != nil {
		t.Fatal(err)
	}
	args[3] = path
	deps.OpenAdapter = func(context.Context, shadowOptions) (shadowAgentAdapter, error) {
		t.Fatal("adapter opened for mutable subject")
		return nil, nil
	}
	if code := mainRunWithShadowDeps(args, shadowNeverDaemon(t), io.Discard, &deps); code != 2 {
		t.Fatalf("mutable ref exit=%d", code)
	}
	if _, err := newShadowOutput(deps.Root, "../../outside"); err == nil {
		t.Fatal("output escape allowed")
	}
}

func TestShadowPanicDoesNotFallThroughIntoDaemon(t *testing.T) {
	deps, _, args, _ := shadowTestDeps(t)
	deps.OpenAdapter = func(context.Context, shadowOptions) (shadowAgentAdapter, error) { panic("adapter panic") }
	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected panic")
			}
		}()
		mainRunWithShadowDeps(args, shadowNeverDaemon(t), io.Discard, &deps)
	}()
	if got := deps.Probe.evidence(); got.CanonicalStoreOpened || got.DaemonLockAcquired {
		t.Fatalf("panic opened production: %+v", got)
	}
}

func TestShadowDaemonStartupStillObservesStoresAndLocks(t *testing.T) {
	cfg := testConfig(t)
	p := &startupProbe{}
	ctx := context.WithValue(context.Background(), probeKey{}, p)
	err := runWithWriter(ctx, cfg, func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
		return nil, nil, errors.New("intentional stop after store and lock acquisition")
	}, io.Discard)
	if err == nil || p.StoreOpens != 1 || p.DaemonLocks != 2 {
		t.Fatalf("normal startup observation: err=%v store=%d locks=%d", err, p.StoreOpens, p.DaemonLocks)
	}
}

// A real stdio MCP subprocess exercises the existing client and typed adapter.
// This is explicitly a protocol fixture, not evidence of a live agent run.
func TestShadowMCPProtocolHelper(t *testing.T) {
	if os.Getenv("COORD_SHADOW_MCP_HELPER") != "1" {
		return
	}
	var dispatch shadowDispatch
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		var req struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(scan.Bytes(), &req) != nil {
			os.Exit(3)
		}
		if req.ID == 0 {
			continue
		}
		var result any
		if req.Method == "initialize" {
			result = map[string]any{"protocolVersion": "2024-11-05", "serverInfo": map[string]any{"name": "telepathy", "version": "0.8.0"}}
		} else {
			text := "delivered"
			switch req.Params.Name {
			case "send_message":
				_, tail, _ := strings.Cut(req.Params.Arguments["message"].(string), "BEGIN_COORD_SHADOW_ASSIGNMENT\n")
				raw, _, _ := strings.Cut(tail, "\nEND_COORD_SHADOW_ASSIGNMENT")
				if json.Unmarshal([]byte(raw), &dispatch) != nil {
					os.Exit(4)
				}
			case "read_messages":
				raw, _ := json.Marshal(shadowReply(dispatch))
				text = "BEGIN_COORD_SHADOW_RESULT\n" + string(raw) + "\nEND_COORD_SHADOW_RESULT"
			}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
		}
		if json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}) != nil {
			os.Exit(5)
		}
	}
	os.Exit(0)
}

func TestShadowCLIUsesExistingMCPClientEndToEnd(t *testing.T) {
	deps, _, args, output := shadowTestDeps(t)
	deps.OpenAdapter = func(ctx context.Context, o shadowOptions) (shadowAgentAdapter, error) {
		p, err := telepathy.Start(ctx, []string{os.Args[0], "-test.run=^TestShadowMCPProtocolHelper$"}, deps.Root, append(os.Environ(), "COORD_SHADOW_MCP_HELPER=1"))
		if err != nil {
			return nil, err
		}
		return &telepathyShadowAgentAdapter{tools: p, process: p, to: o.Agent + ":" + o.Session, replyTo: "opencode:shadow-host"}, nil
	}
	// Allow the protocol subprocess to initialize without weakening live timeouts.
	args[len(args)-1] = "10s"
	var stderr bytes.Buffer
	if code := mainRunWithShadowDeps(args, shadowNeverDaemon(t), &stderr, &deps); code != 0 {
		t.Fatalf("MCP CLI exit=%d: %s", code, stderr.String())
	}
	var report shadowPilotReport
	decodeShadowArtifact(t, output, "REPORT.json", &report)
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestShadowMissingDimensionsAndUnsealedEvidenceCannotReachReport(t *testing.T) {
	deps, _, args, output := shadowTestDeps(t)
	if code := mainRunWithShadowDeps(args, shadowNeverDaemon(t), io.Discard, &deps); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	var a shadowpilot.ShadowAssignment
	var runtime shadowRuntimeEvidence
	var comparison shadowpilot.ComparisonResult
	var bundle shadowpilot.EvidenceBundle
	var report shadowPilotReport
	decodeShadowArtifact(t, output, "ASSIGNMENT.yaml", &a)
	decodeShadowArtifact(t, output, "RUNTIME-EVIDENCE.yaml", &runtime)
	decodeShadowArtifact(t, output, "COMPARISON.json", &comparison)
	if _, err := shadowpilot.NewComparisonResult(comparison.ShadowRunID, comparison.Dimensions[:9]); err == nil {
		t.Fatal("comparison constructor accepted missing dimension")
	}
	decodeShadowArtifact(t, output, "EVIDENCE-MANIFEST.yaml", &bundle)
	decodeShadowArtifact(t, output, "REPORT.json", &report)
	for _, name := range []string{"unsealed", "wrong-run", "missing-dimension", "self-admission", "missing-axis", "unsupported-axis-pass"} {
		t.Run(name, func(t *testing.T) {
			sink, err := newShadowOutput(deps.Root, output+"-"+name)
			if err != nil {
				t.Fatal(err)
			}
			b, c, p := bundle, comparison, report
			switch name {
			case "unsealed":
				b.Provenance = ""
				b.BundleDigest = ""
			case "wrong-run":
				b.ShadowRunID = "foreign-run"
			case "missing-dimension":
				c.Dimensions = c.Dimensions[:9]
			case "self-admission":
				p.Recommendation = "ADMIT_P0_3"
			case "missing-axis":
				p.RuntimeDimensions = map[string]shadowAxis{}
			case "unsupported-axis-pass":
				p.RuntimeDimensions = make(map[string]shadowAxis, len(report.RuntimeDimensions))
				for k, v := range report.RuntimeDimensions {
					p.RuntimeDimensions[k] = v
				}
				p.RuntimeDimensions["independent_audit"] = shadowAxis{Status: "PASS"}
			}
			if name == "self-admission" || name == "missing-axis" || name == "unsupported-axis-pass" {
				err = p.Validate()
			} else {
				err = writeShadowReport(sink, a, runtime, c, shadowpilot.RecoverySummary{CrashPointsTested: 1}, b, deps.Now())
			}
			if err == nil {
				t.Fatalf("%s accepted", name)
			}
			if _, err := os.Stat(filepath.Join(sink.path, "REPORT.json")); !os.IsNotExist(err) {
				t.Fatalf("%s produced report", name)
			}
		})
	}
}

func TestShadowRuntimeBreachesPreventEvidenceSealing(t *testing.T) {
	for _, breach := range []string{"store", "lock", "production", "github", "credentials"} {
		t.Run(breach, func(t *testing.T) {
			deps, tools, args, output := shadowTestDeps(t)
			tools.beforeDispatch = func() {
				switch breach {
				case "store":
					deps.Probe.StoreOpened()
				case "lock":
					deps.Probe.DaemonLockAcquired()
				case "production":
					deps.Probe.ProductionWrites++
				case "github":
					deps.Probe.GitHubWrites++
				case "credentials":
					deps.Probe.CredentialMutations++
				}
			}
			if code := mainRunWithShadowDeps(args, shadowNeverDaemon(t), io.Discard, &deps); code != 2 {
				t.Fatalf("%s exit=%d", breach, code)
			}
			if _, err := os.Stat(filepath.Join(output, "EVIDENCE-MANIFEST.yaml")); !os.IsNotExist(err) {
				t.Fatalf("%s breach sealed evidence", breach)
			}
		})
	}
}
