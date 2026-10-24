package telepathy

import (
	"context"
	"coord/transport"
	"crypto/ed25519"
	"fmt"
	"strings"
	"time"
)

// TransportConfig describes the process and signing identities for one
// production Telepathy byte bridge.
type TransportConfig struct {
	Command      []string
	Directory    string
	Environment  []string
	Session      string
	PollInterval time.Duration
	PrivateKey   ed25519.PrivateKey
	PeerKeys     map[string]ed25519.PublicKey
}

type runtimeAdapter struct {
	*transport.Adapter
	client        *Client
	signed        *transport.SignedClient
	authenticated *transport.AuthenticatedAdapter
}

// Err reports the receive poller's terminal error, if any.
func (a *runtimeAdapter) Err() error { return a.client.Err() }

// ConfigureAgentSessionIngress binds inbound verification to the canonical
// AgentSession registry. The coordinator then consumes this single stream;
// it must not also start the legacy unauthenticated receiver.
func (a *runtimeAdapter) ConfigureAgentSessionIngress(resolver transport.KeyResolver) error {
	if a == nil || a.signed == nil || resolver == nil {
		return fmt.Errorf("AgentSession resolver required")
	}
	a.signed.Resolver = resolver
	a.authenticated = &transport.AuthenticatedAdapter{Client: a.signed, Session: a.Session}
	return nil
}

func (a *runtimeAdapter) ReceiveAuthenticated(ctx context.Context) <-chan transport.AuthenticatedFrame {
	if a == nil || a.authenticated == nil {
		closed := make(chan transport.AuthenticatedFrame)
		close(closed)
		return closed
	}
	return a.authenticated.ReceiveAuthenticated(ctx)
}

func (a *runtimeAdapter) AuthenticatedIngressConfigured() bool {
	return a != nil && a.authenticated != nil
}

// OpenTransport starts and validates the configured Telepathy MCP process and
// returns its signed Coord transport. The close function owns Process.Close.
func OpenTransport(ctx context.Context, cfg TransportConfig) (transport.Transport, func() error, error) {
	if len(cfg.Command) == 0 || cfg.Command[0] == "" || cfg.Session == "" || cfg.PollInterval <= 0 || len(cfg.PrivateKey) != ed25519.PrivateKeySize || len(cfg.PeerKeys) == 0 {
		return nil, nil, fmt.Errorf("invalid Telepathy transport configuration")
	}
	for session, key := range cfg.PeerKeys {
		if session == "" || len(key) != ed25519.PublicKeySize {
			return nil, nil, fmt.Errorf("invalid Telepathy peer key")
		}
	}
	p, err := Start(ctx, cfg.Command, cfg.Directory, cfg.Environment)
	if err != nil {
		return nil, nil, err
	}
	closeProcess := p.Close
	listed, err := p.Call(ctx, "list_peers", map[string]any{})
	if err != nil {
		return nil, closeProcess, err
	}
	first, _, _ := strings.Cut(listed, "\n")
	if !strings.Contains(first, "["+cfg.Session+"]") {
		return nil, closeProcess, fmt.Errorf("configured Telepathy session is not present: %s", first)
	}
	client, err := NewClient(p, cfg.Session, cfg.PollInterval)
	if err != nil {
		return nil, closeProcess, err
	}
	signed := &transport.SignedClient{Client: client, PrivateKey: cfg.PrivateKey, Peers: cfg.PeerKeys}
	return &runtimeAdapter{Adapter: &transport.Adapter{Client: signed, Session: cfg.Session}, client: client, signed: signed}, closeProcess, nil
}
