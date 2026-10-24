//go:build unix

package main

import (
	"bytes"
	"context"
	"coord/assignment"
	"coord/checkpoint"
	"coord/store"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func assignmentCLIFixture(t *testing.T, phase string, now time.Time) (string, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.NewCanonicalWithClock(checkpoint.CanonicalFile{Path: filepath.Join(dir, "canonical.json")}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	coord := assignment.Principal{SessionID: "claude:coordinator", AgentID: "coordinator", Generation: 1}
	agent := assignment.Principal{SessionID: "codex:worker", AgentID: "worker", Generation: 1}
	for _, p := range []assignment.Principal{coord, agent} {
		if err := s.RegisterAgentSession(p.SessionID, p.AgentID, ed25519.PublicKey(strings.Repeat("a", 32))); err != nil {
			t.Fatal(err)
		}
	}
	r := assignment.Record{AssignmentID: "new-assignment", State: assignment.Offered, Issuer: coord.SessionID, IssuerAgentID: coord.AgentID, IssuerGeneration: 1, CurrentCoordinator: coord.SessionID, CoordinatorAgentID: coord.AgentID, CoordinatorGeneration: 1, HandoffEvidence: []assignment.Handoff{}, Assignee: agent.SessionID, StableAgentID: agent.AgentID, SessionID: agent.SessionID, AssigneeGeneration: 1, Role: "ANALYST", SubjectSHA: strings.Repeat("a", 40), IssuedAt: now, ValidUntil: now.Add(time.Hour), AdmissionBasis: assignment.AdmissionBasis{Kind: "POOL", Reference: "verified-admission", TargetSessionID: agent.SessionID, TargetAgentID: agent.AgentID, Role: "ANALYST", SubjectSHA: strings.Repeat("a", 40)}, VisibilityEvidence: assignment.VisibilityEvidence{Reference: "owner-visible-session", SessionID: agent.SessionID, ObservedAt: now}, Revision: 1, UpdatedAt: now}
	if err := s.OfferAssignment(coord, r, "offer-evidence"); err != nil {
		t.Fatal(err)
	}
	if phase != "OFFERED" {
		if err := s.ChangeAssignment(agent, r.AssignmentID, 1, "ACK", "ack"); err != nil {
			t.Fatal(err)
		}
	}
	if phase != "OFFERED" && phase != "ACKED" {
		if err := s.ChangeAssignment(coord, r.AssignmentID, 2, "ACTIVATE", "activation"); err != nil {
			t.Fatal(err)
		}
	}
	if phase == "VOID" {
		if err := s.ChangeAssignment(coord, r.AssignmentID, 3, "VOID", "revoked"); err != nil {
			t.Fatal(err)
		}
	}
	if phase == "LOST" {
		if err := s.ObserveAssignmentCoordinatorLoss(coord, coord, "supervisor-loss"); err != nil {
			t.Fatal(err)
		}
	}
	return dir, r.AssignmentID
}

func TestAssignmentStatusReadOnlyAndFailClosed(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for _, phase := range []string{"OFFERED", "ACKED", "ACTIVE", "VOID", "LOST", "EXPIRED_TIME"} {
		t.Run(phase, func(t *testing.T) {
			dir, id := assignmentCLIFixture(t, phase, now)
			path := filepath.Join(dir, "canonical.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			at := now.Add(time.Minute)
			if phase == "EXPIRED_TIME" {
				at = now.Add(time.Hour)
			}
			var out bytes.Buffer
			if err := runAssignmentStatus([]string{"status", id, "--format", "json"}, dir, &out, at); err != nil {
				t.Fatal(err)
			}
			var status store.AssignmentStatus
			if err := json.Unmarshal(out.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			if status.WorkMayStart != (phase == "ACTIVE") {
				t.Fatalf("%s status=%+v", phase, status)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("status query changed ledger bytes")
			}
			t.Logf("sample %s: %s", phase, strings.TrimSpace(out.String()))
		})
	}
}

func TestAssignmentStatusUnknownUnavailableCorruptAreDistinct(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, reason string
		code         int
	}{{"unknown", "UNKNOWN_ASSIGNMENT", 3}, {"unavailable", "STORE_UNAVAILABLE", 4}, {"corrupt", "STORE_CORRUPT", 5}, {"corrupt-record", "STORE_CORRUPT", 5}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			id := "unknown-id"
			if tc.name == "unknown" || tc.name == "corrupt-record" {
				dir, id = assignmentCLIFixture(t, "ACTIVE", now)
				if tc.name == "unknown" {
					id = "unknown-id"
				} else {
					path := filepath.Join(dir, "canonical.json")
					raw, _ := os.ReadFile(path)
					raw = bytes.Replace(raw, []byte(`"state":"ACTIVE"`), []byte(`"state":"VOID"`), 1)
					if err := os.WriteFile(path, raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.name == "corrupt" {
				if err := os.WriteFile(filepath.Join(dir, "canonical.json"), []byte("broken JSON"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadDir(dir)
			var out bytes.Buffer
			err := runAssignmentStatus([]string{"status", id}, dir, &out, now)
			var failure *assignmentStatusError
			if !errors.As(err, &failure) || failure.ExitCode() != tc.code {
				t.Fatalf("err=%v code=%v", err, failure)
			}
			var status store.AssignmentStatus
			if err := json.Unmarshal(out.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			if status.Reason != tc.reason || status.WorkMayStart {
				t.Fatalf("status=%+v", status)
			}
			after, _ := os.ReadDir(dir)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed read created files/locks")
			}
			t.Logf("sample %s: %s", tc.name, strings.TrimSpace(out.String()))
		})
	}
}

func TestAssignmentStatusYAMLAndCLIBypassDaemon(t *testing.T) {
	now := time.Now().UTC()
	dir, id := assignmentCLIFixture(t, "ACTIVE", now)
	var out, diagnostic bytes.Buffer
	// A nonexistent server would fail if this command attempted daemon startup.
	err := run(context.Background(), []string{"--state", dir, "--server", "/does-not-exist/server.mjs", "assignment", "status", id, "--format", "yaml"}, strings.NewReader(""), &out, &diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := yaml.Unmarshal(out.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"assignmentId", "state", "validUntil", "subjectSha", "assignee", "issuer", "currentCoordinator", "coordinatorGeneration", "handoffValid", "workMayStart", "reason"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("missing YAML field %s", key)
		}
	}
	if fields["workMayStart"] != true || diagnostic.Len() != 0 {
		t.Fatalf("unexpected daemon path: %s %s", out.String(), diagnostic.String())
	}
}

func TestAssignmentStatusLaunchRollbackPreservesLedger(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	dir, id := assignmentCLIFixture(t, "VOID", now)
	path := filepath.Join(dir, "canonical.json")
	before, _ := os.ReadFile(path)
	for i := 0; i < 3; i++ {
		var out bytes.Buffer
		if err := runAssignmentStatus([]string{"status", id}, dir, &out, now); err != nil {
			t.Fatal(err)
		}
		var status store.AssignmentStatus
		_ = json.Unmarshal(out.Bytes(), &status)
		if status.State != assignment.Void || status.WorkMayStart {
			t.Fatal("status-only launch revived terminal")
		}
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("read path discarded ledger")
	}
	// This is the old strict-decoder behavior: it refuses the new top-level
	// field rather than silently decoding and later dropping assignment data.
	var old map[string]json.RawMessage
	_ = json.Unmarshal(before, &old)
	delete(old, "assignment_ledger")
	known, _ := json.Marshal(old)
	currentType := reflect.TypeOf(store.CanonicalState{})
	var oldFields []reflect.StructField
	for i := 0; i < currentType.NumField(); i++ {
		field := currentType.Field(i)
		if field.Name != "AssignmentLedger" {
			oldFields = append(oldFields, field)
		}
	}
	legacyType := reflect.StructOf(oldFields)
	decoder := json.NewDecoder(bytes.NewReader(before))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(reflect.New(legacyType).Interface())
	if err == nil || !strings.Contains(err.Error(), `unknown field "assignment_ledger"`) {
		t.Fatalf("pre-ledger strict decoder error=%v", err)
	}
	decoder = json.NewDecoder(bytes.NewReader(known))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(reflect.New(legacyType).Interface()); err != nil {
		t.Fatalf("old decoder also rejected old-shaped control: %v", err)
	}
	if bytes.Equal(known, before) {
		t.Fatal("fixture has no preserved ledger")
	}
}
