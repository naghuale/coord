//go:build unix

package main

import (
	"bytes"
	"context"
	"coord/internal/shadowpilot"
	"coord/telepathy"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// shadowProvenanceFields decodes the sealed provenance of a completed run.
func shadowProvenanceFields(t *testing.T, output string) (string, map[string]any) {
	t.Helper()
	var bundle shadowpilot.EvidenceBundle
	decodeShadowArtifact(t, output, "EVIDENCE-MANIFEST.yaml", &bundle)
	var fields map[string]any
	if err := json.Unmarshal([]byte(bundle.Provenance), &fields); err != nil {
		t.Fatal(err)
	}
	return bundle.Provenance, fields
}

func shadowMCPDeps(t *testing.T, serverVersion, declared string) (shadowDeps, []string, string) {
	t.Helper()
	deps, _, args, output := shadowTestDeps(t)
	deps.OpenAdapter = func(ctx context.Context, o shadowOptions) (shadowAgentAdapter, error) {
		env := append(os.Environ(), "COORD_SHADOW_MCP_HELPER=1", "COORD_SHADOW_MCP_VERSION="+serverVersion)
		p, err := telepathy.Start(ctx, []string{os.Args[0], "-test.run=^TestShadowMCPProtocolHelper$"}, deps.Root, env)
		if err != nil {
			return nil, err
		}
		return &telepathyShadowAgentAdapter{tools: p, process: p, to: o.Agent + ":" + o.Session, replyTo: "opencode:shadow-host", serverVersion: declared}, nil
	}
	args[len(args)-1] = "10s"
	return deps, args, output
}

func TestShadowProvenanceRecordsObservedServerVersion(t *testing.T) {
	for _, version := range []string{"0.8.0", "0.9.0"} {
		t.Run(version, func(t *testing.T) {
			// The declared fallback disagrees with the server; the handshake must win.
			declared := "0.9.0"
			if version == "0.9.0" {
				declared = "0.8.0"
			}
			deps, args, output := shadowMCPDeps(t, version, declared)
			var stderr bytes.Buffer
			if code := mainRunWithShadowDeps(args, shadowNeverDaemon(t), &stderr, &deps); code != 0 {
				t.Fatalf("exit=%d: %s", code, stderr.String())
			}
			raw, fields := shadowProvenanceFields(t, output)
			if fields["actualServerVersion"] != version {
				t.Fatalf("actualServerVersion=%v, want %s", fields["actualServerVersion"], version)
			}
			if contract, _ := json.Marshal(fields["acceptedClientContract"]); string(contract) != `["0.8.0","0.9.0"]` {
				t.Fatalf("acceptedClientContract=%s", contract)
			}
			if _, legacy := fields["mcpClientVersion"]; legacy {
				t.Fatal("legacy mcpClientVersion still written")
			}
			if err := validateShadowProvenance(raw); err != nil {
				t.Fatalf("updated v2 evidence rejected: %v", err)
			}
		})
	}
}

func TestShadowUnsupportedServerVersionRejected(t *testing.T) {
	deps, args, output := shadowMCPDeps(t, "0.10.0", "0.9.0")
	var stderr bytes.Buffer
	if code := mainRunWithShadowDeps(args, shadowNeverDaemon(t), &stderr, &deps); code != 2 {
		t.Fatalf("unsupported server exit=%d", code)
	}
	if !strings.Contains(stderr.String(), "unsupported MCP server telepathy 0.10.0") {
		t.Fatalf("stderr=%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(output, "EVIDENCE-MANIFEST.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("evidence sealed for unsupported server: %v", err)
	}
}

func TestShadowDeclaredVersionCannotSubstituteObserved(t *testing.T) {
	for _, declared := range []string{"", "telepathy>=0.8.0", "telepathy>=0.8.0/protocol2024-11-05", ">=0.8.0", "0.10.0"} {
		t.Run(declared, func(t *testing.T) {
			deps, tools, args, output := shadowTestDeps(t)
			deps.OpenAdapter = func(context.Context, shadowOptions) (shadowAgentAdapter, error) {
				return &telepathyShadowAgentAdapter{tools: tools, to: "codex:test-session", replyTo: "opencode:shadow-host", serverVersion: declared}, nil
			}
			var stderr bytes.Buffer
			if code := mainRunWithShadowDeps(args, shadowNeverDaemon(t), &stderr, &deps); code != 2 {
				t.Fatalf("exit=%d", code)
			}
			if !strings.Contains(stderr.String(), "actualServerVersion") {
				t.Fatalf("stderr=%s", stderr.String())
			}
			if tools.dispatch.DispatchID != "" {
				t.Fatal("dispatched without an observed server version")
			}
			if _, err := os.Stat(filepath.Join(output, "EVIDENCE-MANIFEST.yaml")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("evidence sealed: %v", err)
			}
		})
	}
}

func TestShadowProvenanceValidatorRejectsMissingDeclaredAndLegacyVersions(t *testing.T) {
	valid := map[string]any{"adapterKind": "telepathy", "acceptedClientContract": []string{"0.8.0", "0.9.0"}, "actualServerVersion": "0.9.0", "mcpProtocolVersion": "2024-11-05"}
	encode := func(change func(map[string]any)) string {
		m := map[string]any{}
		for k, v := range valid {
			m[k] = v
		}
		change(m)
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if err := validateShadowProvenance(encode(func(map[string]any) {})); err != nil {
		t.Fatalf("valid v2 provenance rejected: %v", err)
	}
	for name, tc := range map[string]struct {
		raw   string
		field string
	}{
		"missing actualServerVersion": {encode(func(m map[string]any) { delete(m, "actualServerVersion") }), "actualServerVersion"},
		"empty actualServerVersion":   {encode(func(m map[string]any) { m["actualServerVersion"] = "" }), "actualServerVersion"},
		"declared range as actual":    {encode(func(m map[string]any) { m["actualServerVersion"] = "telepathy>=0.8.0" }), "actualServerVersion"},
		"contract list as actual":     {encode(func(m map[string]any) { m["actualServerVersion"] = []string{"0.8.0", "0.9.0"} }), "provenance"},
		"unsupported actual":          {encode(func(m map[string]any) { m["actualServerVersion"] = "0.10.0" }), "actualServerVersion"},
		"narrowed contract":           {encode(func(m map[string]any) { m["acceptedClientContract"] = []string{"0.9.0"} }), "acceptedClientContract"},
		"widened contract":            {encode(func(m map[string]any) { m["acceptedClientContract"] = []string{"0.8.0", "0.9.0", "0.10.0"} }), "acceptedClientContract"},
		"missing contract":            {encode(func(m map[string]any) { delete(m, "acceptedClientContract") }), "acceptedClientContract"},
		"wrong protocol":              {encode(func(m map[string]any) { m["mcpProtocolVersion"] = "2025-03-26" }), "mcpProtocolVersion"},
		// v1 evidence (liveShadowPilot_v2/v2r) carried only the declared range.
		"legacy v1 provenance":   {`{"adapterKind":"telepathy","mcpClientVersion":"telepathy>=0.8.0/protocol2024-11-05"}`, "provenance"},
		"legacy field beside v2": {encode(func(m map[string]any) { m["mcpClientVersion"] = "telepathy>=0.8.0/protocol2024-11-05" }), "provenance"},
		"not an object":          {`"0.9.0"`, "provenance"},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateShadowProvenance(tc.raw)
			var refusal *ShadowRefusal
			if !errors.As(err, &refusal) || refusal.Field != tc.field {
				t.Fatalf("err=%v, want refusal on %s", err, tc.field)
			}
		})
	}
	if !slices.Equal(telepathy.AcceptedServerVersions(), []string{"0.8.0", "0.9.0"}) {
		t.Fatal("client contract drifted from PV2-F1")
	}
}
