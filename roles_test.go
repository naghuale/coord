package coord

import (
	"context"
	"coord/envelope"
	"coord/fsm"
	"coord/roles"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSelectRoleRejectsReviewerWhoAlsoExecuted(t *testing.T) {
	f := setup(t)
	registry, err := roles.New(filepath.Join(t.TempDir(), "roles-reviewer-indep.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("close roles registry: %v", err)
		}
	})
	profiles := []roles.Profile{
		{Role: roles.Executor, Model: "exec-model", Capabilities: []string{"code"}},
		{Role: roles.Reviewer, Model: "review-model", Capabilities: []string{"review"}},
	}
	for _, session := range []string{"exec-session", "review-session"} {
		if err = registry.Register("agent-both", session, profiles); err != nil {
			t.Fatal(err)
		}
	}
	f.c.RoleRegistry = registry
	makeTestRun := func(id, owner string, role roles.Role) fsm.Run {
		return fsm.Run{
			Task: "REVIEW-INDEP-001", RunID: id, OwnerSession: owner,
			Attempt: 1, Fence: 1, TargetSHA: strings.Repeat("c", 40),
			Worktree: "wt", Deadline: f.now.Add(time.Hour),
			RequiredRole: roles.Requirement{Role: role},
		}
	}
	if err = f.c.Assign(context.Background(), makeTestRun("exec-run", "exec-session", roles.Executor)); err != nil {
		t.Fatalf("assign executor: %v", err)
	}
	if err = f.c.Assign(context.Background(), makeTestRun("review-run", "review-session", roles.Reviewer)); err == nil {
		t.Fatal("M06: executor became reviewer of own task — selectRole reviewer≠executor check missing")
	}
}

func TestSelectRoleRejectsReverseDirectionConflicts(t *testing.T) {
	tests := []struct {
		name       string
		firstRole  roles.Role
		secondRole roles.Role
		wantErrSub string
	}{
		{"executor_after_auditor", roles.Auditor, roles.Executor, "auditor conflicts with task executor"},
		{"executor_after_reviewer", roles.Reviewer, roles.Executor, "reviewer conflicts with task executor"},
		{"auditor_after_reviewer", roles.Reviewer, roles.Auditor, "auditor conflicts with task reviewer"},
		{"reviewer_after_auditor", roles.Auditor, roles.Reviewer, "auditor conflicts with task reviewer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			registry, err := roles.New(filepath.Join(t.TempDir(), "roles-"+tc.name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := registry.Close(); err != nil {
					t.Errorf("close roles registry: %v", err)
				}
			})
			profiles := []roles.Profile{
				{Role: tc.firstRole, Model: "model-a", Capabilities: []string{"x"}},
				{Role: tc.secondRole, Model: "model-b", Capabilities: []string{"x"}},
			}
			for _, session := range []string{"session-a", "session-b"} {
				if err = registry.Register("agent-dual", session, profiles); err != nil {
					t.Fatal(err)
				}
			}
			f.c.RoleRegistry = registry
			makeTestRun := func(id, owner string, role roles.Role) fsm.Run {
				return fsm.Run{
					Task: "CONFLICT-TEST", RunID: id, OwnerSession: owner,
					Attempt: 1, Fence: 1, TargetSHA: strings.Repeat("d", 40),
					Worktree: "wt", Deadline: f.now.Add(time.Hour),
					RequiredRole: roles.Requirement{Role: role},
				}
			}
			if err = f.c.Assign(context.Background(), makeTestRun("first-run", "session-a", tc.firstRole)); err != nil {
				t.Fatalf("assign first role %v: %v", tc.firstRole, err)
			}
			err = f.c.Assign(context.Background(), makeTestRun("second-run", "session-b", tc.secondRole))
			if err == nil {
				t.Fatalf("%v holder became %v of own task — independence check missing", tc.firstRole, tc.secondRole)
			}
			if !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Fatalf("error=%q, want to contain %q", err.Error(), tc.wantErrSub)
			}
		})
	}
}

func TestRolesIndependenceAndImmutableAssignment(t *testing.T) {
	f := setup(t)
	registry, err := roles.New(filepath.Join(t.TempDir(), "roles.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("close test resource: %v", err)
		}
	})
	profiles := []roles.Profile{{Role: roles.Executor, Model: "nemotron", Capabilities: []string{"go"}}, {Role: roles.Auditor, Model: "audit", Capabilities: []string{"security_audit"}}}
	for _, session := range []string{"exec", "alias"} {
		if err = registry.Register("same-agent", session, profiles); err != nil {
			t.Fatal(err)
		}
	}
	if err = registry.Register("independent", "auditor", profiles); err != nil {
		t.Fatal(err)
	}
	f.c.RoleRegistry = registry
	makeRun := func(id, owner string, role roles.Role) fsm.Run {
		return fsm.Run{Task: "CF-009", RunID: id, OwnerSession: owner, Attempt: 1, Fence: 1, TargetSHA: strings.Repeat("a", 40), Worktree: "wt", Deadline: f.now.Add(time.Hour), RequiredRole: roles.Requirement{Role: role}}
	}
	if err = f.c.Assign(context.Background(), makeRun("exec-run", "exec", roles.Executor)); err != nil {
		t.Fatal(err)
	}
	if err = f.c.Assign(context.Background(), makeRun("bad-audit", "alias", roles.Auditor)); err == nil {
		t.Fatal("executor audited own task using another session")
	}
	if err = f.c.Assign(context.Background(), makeRun("good-audit", "auditor", roles.Auditor)); err != nil {
		t.Fatal(err)
	}
	model := "new-model"
	e := envelope.Envelope{SchemaVersion: 1, MessageID: envelope.NewID(), Timestamp: *f.now, From: "exec", To: "coord", Kind: envelope.RoleUpdate, RoleUpdatePayload: &roles.Update{Revision: 1, Removed: []roles.Role{roles.Auditor}, Updated: []roles.Patch{{Role: roles.Executor, Model: &model}}}}
	f.handle(t, e)
	if f.tp.sent[len(f.tp.sent)-1].Kind != envelope.RoleAck {
		t.Fatal("missing application ACK for role update")
	}
	run, _ := f.s.Get("exec-run")
	if run.RoleSelection.Profile.Model != "nemotron" {
		t.Fatal("active assignment changed profile")
	}
	fresh := makeRun("next-run", "exec", roles.Executor)
	if err = f.c.Assign(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	run, _ = f.s.Get("next-run")
	if run.RoleSelection.Profile.Model != "new-model" {
		t.Fatal("new assignment used stale profile")
	}
}
