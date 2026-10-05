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

func TestRolesIndependenceAndImmutableAssignment(t *testing.T) {
	f := setup(t)
	registry, err := roles.New(filepath.Join(t.TempDir(), "roles.json"))
	if err != nil {
		t.Fatal(err)
	}
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
