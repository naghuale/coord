// This example changes role availability without executing a real agent task.
package main

import (
	"coord/roles"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

func main() {
	dir, err := os.MkdirTemp("", "coord-roles-")
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintln(os.Stderr, "remove example state:", err)
		}
	}()
	registry, err := roles.New(filepath.Join(dir, "roles.json"))
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := registry.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "close example snapshot:", err)
		}
	}()
	const session = "session://agent-nemotron-1"
	err = registry.Register("agent-nemotron", session, []roles.Profile{
		{Role: roles.Executor, Model: "provider/model-v1", Capabilities: []string{"go"}},
		{Role: roles.Reviewer, Model: "provider/review-model", Capabilities: []string{"code_review"}},
	})
	if err != nil {
		log.Fatal(err)
	}
	before, _ := registry.Agent(session)
	fmt.Printf("before: revision=%d executor=%s\n", before.Revision, before.Roles[roles.Executor].Model)
	model := "provider/model-v2"
	err = registry.Apply(session, "example-role-update-1", roles.Update{
		Revision: before.Revision,
		Added:    []roles.Profile{{Role: roles.Auditor, Model: "provider/audit-model", Capabilities: []string{"security_audit"}}},
		Removed:  []roles.Role{roles.Reviewer},
		Updated:  []roles.Patch{{Role: roles.Executor, Model: &model}},
	}, time.Now().UTC())
	if err != nil {
		log.Fatal(err)
	}
	after, _ := registry.Agent(session)
	executor := after.Roles[roles.Executor]
	fmt.Printf("after: revision=%d executor=%s capabilities=%v\n", after.Revision, executor.Model, executor.Capabilities)
	auditor, err := registry.Select(session, roles.Requirement{Role: roles.Auditor, Capabilities: []string{"security_audit"}})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("auditor: profile=%s capabilities=%v\n", auditor.Profile.Model, auditor.Profile.Capabilities)
	if _, exists := after.Roles[roles.Reviewer]; !exists {
		fmt.Println("reviewer: removed")
	}
	fmt.Printf("audit events: %d\n", len(registry.Events()))
}
