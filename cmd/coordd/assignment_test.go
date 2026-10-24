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
	"path/filepath"
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
	if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	canonicalPath := filepath.Join(cfg.StateDir, "canonical.json")
	legacyState := store.EmptyCanonicalState()
	legacyState.SchemaVersion = 4
	legacyJSON, err := json.Marshal(legacyState)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonicalPath, legacyJSON, 0600); err != nil {
		t.Fatal(err)
	}
	seed, err := store.NewCanonical(checkpoint.CanonicalFile{Path: canonicalPath})
	if err != nil {
		t.Fatal(err)
	}
	private := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	if err := seed.RegisterAgentSession("codex:worker", "worker", private.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	seedSnapshot, err := seed.CanonicalSnapshot()
	if err != nil || seedSnapshot.SchemaVersion != 5 || !seedSnapshot.AgentSessions["codex:worker"].Active() {
		t.Fatalf("fixture does not contain active schema-v5 AgentSession: schema=%d err=%v", seedSnapshot.SchemaVersion, err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "coordd.json")
	configJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configJSON, 0600); err != nil {
		t.Fatal(err)
	}
	spy := &agentIngressConfigurationSpy{
		session: cfg.Transport.Session, incoming: make(chan envelope.Envelope),
		authIncoming: make(chan transport.AuthenticatedFrame),
	}
	close(spy.incoming)
	exitCode := mainRunWithWriter([]string{"-config", configPath}, func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
		return spy, func() error { return nil }, nil
	}, io.Discard)
	if exitCode != 1 {
		t.Fatalf("normal CLI startup exit=%d, want transport-closed exit after entering legacy receive", exitCode)
	}
	if spy.configured != 0 || spy.resolver != nil || spy.legacyReads != 1 || spy.authenticatedRead != 0 {
		t.Fatalf("normal v5 production start: Configure=%d resolver=%T legacy=%d authenticated=%d",
			spy.configured, spy.resolver, spy.legacyReads, spy.authenticatedRead)
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
