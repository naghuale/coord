// Package transport separates application ACKs from transport delivery.
package transport

import (
	"context"
	"coord/envelope"
	"encoding/json"
	"fmt"
	"sync"
)

type Transport interface {
	Send(context.Context, string, envelope.Envelope) error
	Receive(context.Context) <-chan envelope.Envelope
	WhoAmI() string
}

// Client is the byte-oriented boundary implemented by a real transport SDK.
type Client interface {
	Send(context.Context, string, []byte) error
	Incoming(context.Context) <-chan []byte
}

// Adapter wraps a byte client. Receive may be called once for its lifetime.
type Adapter struct {
	Client    Client
	Session   string
	Malformed func()
	once      sync.Once
	inbox     chan envelope.Envelope
}

func (a *Adapter) WhoAmI() string { return a.Session }
func (a *Adapter) Send(ctx context.Context, to string, e envelope.Envelope) error {
	if e.To != to || e.From != a.Session {
		return fmt.Errorf("transport identity mismatch")
	}
	if err := e.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return a.Client.Send(ctx, to, raw)
}
func (a *Adapter) Receive(ctx context.Context) <-chan envelope.Envelope {
	a.once.Do(func() {
		a.inbox = make(chan envelope.Envelope)
		go func() {
			defer close(a.inbox)
			raws := a.Client.Incoming(ctx)
			for {
				select {
				case <-ctx.Done():
					return
				case raw, ok := <-raws:
					if !ok {
						return
					}
					e, err := envelope.Decode(raw)
					if err != nil {
						if a.Malformed != nil {
							a.Malformed()
						}
						continue
					}
					select {
					case a.inbox <- e:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	})
	return a.inbox
}
