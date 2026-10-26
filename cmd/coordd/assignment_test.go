//go:build unix

package main

import (
	"context"
	"coord"
	"coord/assignment"
	"coord/checkpoint"
	"coord/envelope"
	"coord/internal/coorddconfig"
	"coord/roles"
	"coord/store"
	"coord/transport"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
)

type agentIngressConfigurationSpy struct {
	session           string
	cancel            context.CancelFunc
	configured        int
	legacyReads       int
	authenticatedRead int
	resolver          transport.KeyResolver
	incoming          chan envelope.Envelope
	authIncoming      chan transport.AuthenticatedFrame
}

func (s *agentIngressConfigurationSpy) WhoAmI() string { return s.session }
func (s *agentIngressConfigurationSpy) Send(context.Context, string, envelope.Envelope) error {
	return nil
}
func (s *agentIngressConfigurationSpy) Receive(context.Context) <-chan envelope.Envelope {
	s.legacyReads++
	if s.cancel != nil {
		s.cancel()
	}
	return s.incoming
}
func (s *agentIngressConfigurationSpy) AuthenticatedIngressConfigured() bool { return true }
func (s *agentIngressConfigurationSpy) ReceiveAuthenticated(context.Context) <-chan transport.AuthenticatedFrame {
	s.authenticatedRead++
	if s.cancel != nil {
		s.cancel()
	}
	return s.authIncoming
}
func (s *agentIngressConfigurationSpy) ConfigureAgentSessionIngress(resolver transport.KeyResolver) error {
	s.configured++
	s.resolver = resolver
	return nil
}

func TestProductionCompositionDoesNotConfigureAgentSessionIngress(t *testing.T) {
	t.Setenv("COORD_AGENT_INGRESS", "1")
	if coord.AuthenticatedAgentIngressEnabled {
		t.Fatal("production authenticated ingress must remain disabled pending cutover")
	}
	cfg := testConfig(t)
	cfg.AssignmentLedgerMode = "LEDGER_FROZEN"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	spy := &agentIngressConfigurationSpy{
		session: cfg.Transport.Session, cancel: cancel, incoming: make(chan envelope.Envelope),
		authIncoming: make(chan transport.AuthenticatedFrame),
	}
	activeV5 := false
	openCanonical := func(path string) (store.RunStore, error) {
		state := store.EmptyCanonicalState()
		state.SchemaVersion = 4
		raw, err := json.Marshal(state)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			return nil, err
		}
		canonical, err := store.NewCanonical(checkpoint.CanonicalFile{Path: path})
		if err != nil {
			return nil, err
		}
		private := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
		if err := canonical.RegisterAgentSession("codex:worker", "worker", private.Public().(ed25519.PublicKey)); err != nil {
			_ = canonical.Close()
			return nil, err
		}
		snapshot, err := canonical.CanonicalSnapshot()
		activeV5 = err == nil && snapshot.SchemaVersion == store.CanonicalSchemaVersion && snapshot.AgentSessions["codex:worker"].Active()
		return canonical, err
	}
	err := runWithWriterFactories(ctx, cfg, func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
		return spy, func() error { return nil }, nil
	}, openCanonical, roles.New, io.Discard)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("startup result=%v, want normal cancellation after receiver initialization", err)
	}
	if !activeV5 || spy.configured != 0 || spy.resolver != nil || spy.legacyReads != 1 || spy.authenticatedRead != 0 {
		t.Fatalf("normal v5 production start: activeV5=%v Configure=%d resolver=%T legacy=%d authenticated=%d",
			activeV5, spy.configured, spy.resolver, spy.legacyReads, spy.authenticatedRead)
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
