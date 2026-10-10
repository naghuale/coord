package supervisor

import (
	"context"
	"errors"
	"testing"

	"coord/internal/agentsession"
)

type mockRegistry struct {
	fenced    bool
	completed bool
	fenceErr  error
}

func (m *mockRegistry) Admit(_ context.Context, _ agentsession.AdmitRequest) (agentsession.Session, error) {
	return agentsession.Session{}, nil
}

func (m *mockRegistry) Heartbeat(_ context.Context, _ agentsession.HeartbeatRequest) (agentsession.HeartbeatResult, error) {
	return agentsession.HeartbeatResult{}, nil
}

func (m *mockRegistry) Complete(_ context.Context, _ agentsession.CompleteRequest) error {
	m.completed = true
	return nil
}

func (m *mockRegistry) Fence(_ context.Context, _ agentsession.FenceRequest) error {
	m.fenced = true
	return m.fenceErr
}

func TestReconcileProcessAliveNoOp(t *testing.T) {
	reg := &mockRegistry{}
	r := &Reconciler{Registry: reg}
	err := r.Reconcile(context.Background(), ReconcileRequest{
		Session:      agentsession.Session{Status: agentsession.StatusActive},
		ProcessAlive: true,
	})
	if err != nil {
		t.Fatalf("expected nil for alive process, got %v", err)
	}
	if reg.fenced || reg.completed {
		t.Fatal("should not fence/complete alive process")
	}
}

func TestReconcileOrphanedActiveSessionFencesClosed(t *testing.T) {
	reg := &mockRegistry{}
	r := &Reconciler{Registry: reg}
	err := r.Reconcile(context.Background(), ReconcileRequest{
		Session:      agentsession.Session{Status: agentsession.StatusActive, Generation: 1},
		ProjectID:    "p1",
		AssignmentID: "a1",
		ProcessAlive: false,
	})
	if !errors.Is(err, ErrSessionOrphaned) {
		t.Fatalf("expected ErrSessionOrphaned, got %v", err)
	}
	if !reg.fenced {
		t.Fatal("expected Fence to be called")
	}
	if !reg.completed {
		t.Fatal("expected Complete to be called")
	}
}

func TestReconcileTerminalSessionCompletes(t *testing.T) {
	reg := &mockRegistry{}
	r := &Reconciler{Registry: reg}
	err := r.Reconcile(context.Background(), ReconcileRequest{
		Session:      agentsession.Session{Status: agentsession.StatusFenced, Generation: 1},
		ProcessAlive: false,
	})
	if err != nil {
		t.Fatalf("expected nil for terminal status, got %v", err)
	}
	if reg.fenced {
		t.Fatal("should not fence already-terminal session")
	}
	if !reg.completed {
		t.Fatal("expected Complete to be called for terminal session")
	}
}
