package roles

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestRegistryDoesNotRetainCallerOwnedProfilesAndEvents(t *testing.T) {
	r, err := New(filepath.Join(t.TempDir(), "roles.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("close test resource: %v", err)
		}
	})
	profiles := []Profile{{Role: Executor, Model: "model", Capabilities: []string{"go"}}}
	if err = r.Register("id", "session", profiles); err != nil {
		t.Fatal(err)
	}
	profiles[0].Capabilities[0] = "changed"
	a, _ := r.Agent("session")
	if a.Roles[Executor].Capabilities[0] != "go" {
		t.Fatal("profile aliases caller data")
	}
	model := "next"
	u := Update{Revision: 1, Updated: []Patch{{Role: Executor, Model: &model}}}
	if err = r.Apply("session", "update", u, time.Now()); err != nil {
		t.Fatal(err)
	}
	model = "mutated"
	if *r.Events()[0].Update.Updated[0].Model != "next" {
		t.Fatal("event aliases caller input")
	}
}
func TestRevisionOverflowIsRejected(t *testing.T) {
	r, err := New(filepath.Join(t.TempDir(), "roles.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("close test resource: %v", err)
		}
	})
	if err = r.Register("id", "session", []Profile{{Role: Executor, Model: "model"}}); err != nil {
		t.Fatal(err)
	}
	a := r.state.Agents["session"]
	a.Revision = math.MaxUint64
	r.state.Agents["session"] = a
	model := "next"
	if err = r.Apply("session", "overflow", Update{Revision: math.MaxUint64, Updated: []Patch{{Role: Executor, Model: &model}}}, time.Now()); err == nil {
		t.Fatal("revision wrapped")
	}
	a, _ = r.Agent("session")
	if a.Revision != math.MaxUint64 || len(r.Events()) != 0 {
		t.Fatal("overflow changed registry")
	}
}
