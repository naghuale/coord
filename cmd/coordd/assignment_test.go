//go:build unix

package main

import (
	"context"
	"coord"
	"coord/assignment"
	"coord/checkpoint"
	"coord/internal/coorddconfig"
	"coord/roles"
	"coord/store"
	"coord/transport"
	"errors"
	"io"
	"testing"
)

type agentIngressConfigurationSpy struct {
	transport.Transport
	configured int
}

func (s *agentIngressConfigurationSpy) ConfigureAgentSessionIngress(transport.KeyResolver) error {
	s.configured++
	return nil
}

func TestProductionCompositionDoesNotConfigureAgentSessionIngress(t *testing.T) {
	if coord.AuthenticatedAgentIngressEnabled {
		t.Fatal("production authenticated ingress must remain disabled pending cutover")
	}
	spy := &agentIngressConfigurationSpy{}
	if err := configureAuthenticatedAgentIngress(spy, nil); err != nil {
		t.Fatal(err)
	}
	if spy.configured != 0 {
		t.Fatalf("normal production composition configured authenticated ingress %d times", spy.configured)
	}
}

func TestAssignmentFrozenModeConfiguredBeforeTransport(t *testing.T) {
	cfg := testConfig(t)
	cfg.AssignmentLedgerMode = "LEDGER_FROZEN"
	var canonical *store.CanonicalStore
	stop := errors.New("stop fixture before transport and listeners")
	err := runWithWriterFactories(context.Background(), cfg, func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
		if canonical == nil {
			t.Fatal("missing canonical store")
		}
		if err := canonical.OfferAssignment(assignment.Principal{}, assignment.Record{}, "fixture"); !errors.Is(err, store.ErrLedgerFrozen) {
			t.Fatalf("startup did not apply frozen mode: %v", err)
		}
		return nil, nil, stop
	}, func(path string) (store.RunStore, error) {
		var err error
		canonical, err = store.NewCanonical(checkpoint.CanonicalFile{Path: path})
		return canonical, err
	}, roles.New, io.Discard)
	if !errors.Is(err, stop) {
		t.Fatalf("err=%v", err)
	}
}
