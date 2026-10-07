package transport

import (
	"context"
	"coord/envelope"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"sync"
)

// SignedClient authenticates byte frames before Adapter sees them. Keys must be
// configured by a trusted registry and remain immutable while the client runs.
type SignedClient struct {
	Client     Client
	PrivateKey ed25519.PrivateKey
	Peers      map[string]ed25519.PublicKey
	Rejected   func()
	once       sync.Once
	in         chan []byte
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
