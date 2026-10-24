package transport

import (
	"context"
	"coord/envelope"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"sync"
)

// VerificationKey is resolved from the trusted AgentSession registry. The
// generation is captured with the key so a store command can reject a frame
// if that binding changed after signature verification.
type VerificationKey struct {
	PublicKey  ed25519.PublicKey
	Generation uint64
}

// KeyResolver supplies the current verification key for a claimed sender
// address. Implementations are injected by the host; transport does not depend
// on store.
type KeyResolver interface {
	ResolveVerificationKey(context.Context, string) (VerificationKey, error)
}

// AuthenticatedFrame is created only after signature verification. Its fields
// are private so callers cannot fabricate a populated verified frame.
type AuthenticatedFrame struct {
	envelope      envelope.Envelope
	rawSigned     []byte
	senderAddress string
	publicKey     ed25519.PublicKey
	generation    uint64
	receivedBy    string
}

func (f AuthenticatedFrame) Envelope() envelope.Envelope {
	raw, err := json.Marshal(f.envelope)
	if err != nil {
		return envelope.Envelope{}
	}
	cloned, err := envelope.Decode(raw)
	if err != nil {
		return envelope.Envelope{}
	}
	return cloned
}
func (f AuthenticatedFrame) RawSignedFrame() []byte {
	return append([]byte(nil), f.rawSigned...)
}
func (f AuthenticatedFrame) SenderAddress() string { return f.senderAddress }
func (f AuthenticatedFrame) VerifiedPublicKey() ed25519.PublicKey {
	return append(ed25519.PublicKey(nil), f.publicKey...)
}
func (f AuthenticatedFrame) SessionGeneration() uint64 { return f.generation }
func (f AuthenticatedFrame) ReceivedBy() string        { return f.receivedBy }

// SignedClient authenticates byte frames before an adapter sees them. Legacy
// Incoming uses Peers; the AgentSession path uses Resolver and never falls back
// to the legacy map.
type SignedClient struct {
	Client     Client
	PrivateKey ed25519.PrivateKey
	Peers      map[string]ed25519.PublicKey
	Resolver   KeyResolver
	Rejected   func()
	once       sync.Once
	in         chan []byte
	authOnce   sync.Once
	authIn     chan AuthenticatedFrame
	modeMu     sync.Mutex
	inMode     signedIncomingMode
}

type signedIncomingMode uint8

const (
	signedIncomingLegacy signedIncomingMode = iota + 1
	signedIncomingAgentSession
)

func (s *SignedClient) claimIncomingMode(mode signedIncomingMode) bool {
	s.modeMu.Lock()
	defer s.modeMu.Unlock()
	if s.inMode != 0 && s.inMode != mode {
		return false
	}
	s.inMode = mode
	return true
}

func (s *SignedClient) Send(ctx context.Context, to string, raw []byte) error {
	if s.Client == nil {
		return fmt.Errorf("signed transport client required")
	}
	e, err := envelope.Decode(raw)
	if err != nil {
		return err
	}
	if e.To != to {
		return fmt.Errorf("signed transport recipient mismatch")
	}
	signed, err := envelope.Sign(e, s.PrivateKey)
	if err != nil {
		return err
	}
	return s.Client.Send(ctx, to, signed)
}
func (s *SignedClient) Incoming(ctx context.Context) <-chan []byte {
	s.once.Do(func() {
		s.in = make(chan []byte)
		if !s.claimIncomingMode(signedIncomingLegacy) {
			close(s.in)
			if s.Rejected != nil {
				s.Rejected()
			}
			return
		}
		go func() {
			defer close(s.in)
			if s.Client == nil {
				if s.Rejected != nil {
					s.Rejected()
				}
				return
			}
			in := s.Client.Incoming(ctx)
			for {
				select {
				case <-ctx.Done():
					return
				case raw, ok := <-in:
					if !ok {
						return
					}
					e, err := envelope.Verify(raw, s.Peers)
					if err != nil {
						if s.Rejected != nil {
							s.Rejected()
						}
						continue
					}
					b, err := json.Marshal(e)
					if err != nil {
						continue
					}
					select {
					case s.in <- b:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	})
	return s.in
}

// IncomingAuthenticated verifies each signed frame with a key from Resolver
// and preserves the authenticated address, public key, and generation. The
// legacy Peers map is deliberately not a fallback for this path.
func (s *SignedClient) IncomingAuthenticated(ctx context.Context) <-chan AuthenticatedFrame {
	s.authOnce.Do(func() {
		s.authIn = make(chan AuthenticatedFrame)
		if !s.claimIncomingMode(signedIncomingAgentSession) {
			close(s.authIn)
			if s.Rejected != nil {
				s.Rejected()
			}
			return
		}
		go func() {
			defer close(s.authIn)
			if s.Client == nil || s.Resolver == nil {
				if s.Rejected != nil {
					s.Rejected()
				}
				return
			}
			for {
				select {
				case <-ctx.Done():
					return
				case raw, ok := <-s.Client.Incoming(ctx):
					if !ok {
						return
					}
					frame, err := s.verifyAuthenticatedFrame(ctx, raw)
					if err != nil {
						if s.Rejected != nil {
							s.Rejected()
						}
						continue
					}
					select {
					case s.authIn <- frame:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	})
	return s.authIn
}

func (s *SignedClient) verifyAuthenticatedFrame(ctx context.Context, raw []byte) (AuthenticatedFrame, error) {
	var signed envelope.Signed
	if err := json.Unmarshal(raw, &signed); err != nil {
		return AuthenticatedFrame{}, err
	}
	claimed, err := envelope.Decode(signed.Message)
	if err != nil {
		return AuthenticatedFrame{}, err
	}
	resolved, err := s.Resolver.ResolveVerificationKey(ctx, claimed.From)
	if err != nil {
		return AuthenticatedFrame{}, err
	}
	if len(resolved.PublicKey) != ed25519.PublicKeySize || resolved.Generation == 0 {
		return AuthenticatedFrame{}, fmt.Errorf("invalid resolved AgentSession key")
	}
	verified, err := envelope.Verify(raw, map[string]ed25519.PublicKey{claimed.From: resolved.PublicKey})
	if err != nil {
		return AuthenticatedFrame{}, err
	}
	if verified.From != claimed.From || verified.To != claimed.To {
		return AuthenticatedFrame{}, fmt.Errorf("verified envelope identity mismatch")
	}
	return AuthenticatedFrame{
		envelope: verified, rawSigned: append([]byte(nil), raw...), senderAddress: verified.From,
		publicKey: append(ed25519.PublicKey(nil), resolved.PublicKey...), generation: resolved.Generation,
	}, nil
}

type AuthenticatedClient interface {
	IncomingAuthenticated(context.Context) <-chan AuthenticatedFrame
}

// AuthenticatedReceiver exposes signed frames with verified AgentSession
// evidence to the daemon command boundary. It is optional so legacy/test
// transports retain their existing interface.
type AuthenticatedReceiver interface {
	ReceiveAuthenticated(context.Context) <-chan AuthenticatedFrame
}

// ConfiguredAuthenticatedReceiver distinguishes transports that support this
// path from transports whose AgentSession resolver has not been installed.
type ConfiguredAuthenticatedReceiver interface {
	AuthenticatedReceiver
	AuthenticatedIngressConfigured() bool
}

// AuthenticatedAdapter applies the same local-recipient check as Adapter while
// preserving verification evidence for the canonical command boundary. It sets
// ReceivedBy only after the signed recipient matches its configured Session;
// consumers may trust that field only on frames delivered by this adapter.
type AuthenticatedAdapter struct {
	Client   AuthenticatedClient
	Session  string
	Rejected func()
	once     sync.Once
	inbox    chan AuthenticatedFrame
}

func (a *AuthenticatedAdapter) ReceiveAuthenticated(ctx context.Context) <-chan AuthenticatedFrame {
	a.once.Do(func() {
		a.inbox = make(chan AuthenticatedFrame)
		go func() {
			defer close(a.inbox)
			if a.Client == nil || a.Session == "" {
				if a.Rejected != nil {
					a.Rejected()
				}
				return
			}
			for {
				select {
				case <-ctx.Done():
					return
				case frame, ok := <-a.Client.IncomingAuthenticated(ctx):
					if !ok {
						return
					}
					if frame.envelope.To != a.Session || frame.receivedBy != "" {
						if a.Rejected != nil {
							a.Rejected()
						}
						continue
					}
					frame.receivedBy = a.Session
					select {
					case a.inbox <- frame:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	})
	return a.inbox
}
