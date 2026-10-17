package orchestration

import (
	"context"
	"fmt"

	"coord/internal/externalaction"
	"coord/store"
)

// AdapterRegistry routes an existing durable action through the existing
// Runtime interface. It owns no claims, retries, persistence or recovery.
type AdapterRegistry struct {
	store    *store.CanonicalStore
	byFamily map[externalaction.Family]Adapter
}

type AdapterRegistration struct {
	Family  externalaction.Family
	Adapter Adapter
}

func NewAdapterRegistry(s *store.CanonicalStore, adapters ...AdapterRegistration) (*AdapterRegistry, error) {
	if s == nil {
		return nil, fmt.Errorf("canonical store required")
	}
	r := &AdapterRegistry{store: s, byFamily: make(map[externalaction.Family]Adapter, len(adapters))}
	for _, registration := range adapters {
		if registration.Adapter == nil || registration.Adapter.SourceID() == "" {
			return nil, fmt.Errorf("valid adapter required")
		}
		if registration.Family != externalaction.AgentSession && registration.Family != externalaction.GitHub {
			return nil, fmt.Errorf("unknown adapter family %q", registration.Family)
		}
		if _, exists := r.byFamily[registration.Family]; exists {
			return nil, fmt.Errorf("duplicate adapter family %q", registration.Family)
		}
		r.byFamily[registration.Family] = registration.Adapter
	}
	return r, nil
}

func (r *AdapterRegistry) SourceID() string { return "coordd-adapter-registry" }

func (r *AdapterRegistry) route(a externalaction.Action) (Adapter, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	adapter := r.byFamily[a.Family]
	if adapter == nil {
		return nil, fmt.Errorf("no qualified adapter for action family %q", a.Family)
	}
	if a.Family == externalaction.GitHub {
		p, err := a.GitHubPayload()
		if err != nil {
			return nil, err
		}
		switch p.Operation {
		case externalaction.ObserveIssue, externalaction.ObservePullRequest, externalaction.ObservePipeline:
		default:
			return nil, fmt.Errorf("GitHub operation %q is disabled", p.Operation)
		}
	}
	return adapter, nil
}

func (r *AdapterRegistry) Execute(ctx context.Context, a externalaction.Action) (externalaction.Result, error) {
	adapter, err := r.route(a)
	if err != nil {
		return externalaction.Result{}, err
	}
	return adapter.Execute(ctx, a)
}

func (r *AdapterRegistry) Observe(ctx context.Context, a externalaction.Action) (externalaction.Result, error) {
	adapter, err := r.route(a)
	if err != nil {
		return externalaction.Result{}, err
	}
	return adapter.Observe(ctx, a)
}

func (r *AdapterRegistry) Acknowledge(ctx context.Context, receipt externalaction.Receipt) error {
	snapshot, err := r.store.CanonicalSnapshot()
	if err != nil {
		return err
	}
	action, ok := snapshot.Orchestration.Actions[receipt.ActionID]
	if !ok {
		return fmt.Errorf("receipt action missing")
	}
	adapter, err := r.route(action)
	if err != nil {
		return err
	}
	return adapter.Acknowledge(ctx, receipt)
}
