// Package telepathy implements the verified Telepathy 0.8.0 MCP stdio boundary.
// It carries signed Coord frames; application acceptance belongs to Coord.
package telepathy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Tools is the MCP text-tool boundary, replaceable in transport tests.
type Tools interface {
	Call(context.Context, string, map[string]any) (string, error)
}

// Client carries byte frames. Wrap it in transport.SignedClient and Adapter.
// Incoming is one-shot. It replays the last 50 archived messages after restart;
// Telepathy does not provide an atomic read-and-ack inbox transaction.
type Client struct {
	tools   Tools
	session string
	poll    time.Duration
	once    sync.Once
	in      chan []byte
	mu      sync.Mutex
	err     error
}

// NewClient validates transport settings before any inbox is consumed.
func NewClient(tools Tools, session string, poll time.Duration) (*Client, error) {
	if tools == nil || session == "" || poll <= 0 {
		return nil, fmt.Errorf("tools, session and positive poll interval required")
	}
	return &Client{tools: tools, session: session, poll: poll}, nil
}

// Err returns the receive-loop failure after Incoming closes.
func (c *Client) Err() error { c.mu.Lock(); defer c.mu.Unlock(); return c.err }

// Send propagates MCP isError, including duplicate suppression and rate limits.
func (c *Client) Send(ctx context.Context, to string, raw []byte) error {
	if to == "" || len(raw) == 0 || len(raw) > 99000 {
		return fmt.Errorf("invalid Telepathy frame size or recipient")
	}
	_, err := c.tools.Call(ctx, "send_message", map[string]any{"to": to, "message": "BEGIN_COORD_ENVELOPE\n" + string(raw) + "\nEND_COORD_ENVELOPE"})
	if err != nil {
		return fmt.Errorf("telepathy send: %w", err)
	}
	return nil
}
func extract(text string) [][]byte {
	var frames [][]byte
	for {
		_, tail, ok := strings.Cut(text, "BEGIN_COORD_ENVELOPE\n")
		if !ok {
			break
		}
		body, rest, ok := strings.Cut(tail, "\nEND_COORD_ENVELOPE")
		if !ok {
			break
		}
		text = rest
		if len(body) > 0 && len(body) <= 99000 {
			frames = append(frames, []byte(body))
		}
	}
	return frames
}

// Incoming polls the actual read_messages tool and includes archived messages.
// Deduplication here is per connection; durable protocol deduplication stays in
// coordinator/executor checkpoints. No model-generated text is authenticated here.
func (c *Client) Incoming(ctx context.Context) <-chan []byte {
	c.once.Do(func() {
		c.in = make(chan []byte)
		go func() {
			defer close(c.in)
			seen := map[[32]byte]bool{}
			ticker := time.NewTicker(c.poll)
			defer ticker.Stop()
			for {
				text, err := c.tools.Call(ctx, "read_messages", map[string]any{"limit": 50})
				if err != nil {
					c.mu.Lock()
					c.err = err
					c.mu.Unlock()
					return
				}
				for _, frame := range extract(text) {
					hash := sha256.Sum256(frame)
					if seen[hash] {
						continue
					}
					select {
					case c.in <- frame:
						seen[hash] = true
					case <-ctx.Done():
						return
					}
				}
				// Bound connection memory. Application replay remains idempotent.
				if len(seen) > 4096 {
					seen = map[[32]byte]bool{}
				}
				select {
				case <-ticker.C:
				case <-ctx.Done():
					return
				}
			}
		}()
	})
	return c.in
}
