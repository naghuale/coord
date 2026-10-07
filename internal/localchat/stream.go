//go:build unix

package localchat

import (
	"context"
	"coord/internal/rooms"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// Watch streams the shared room journal over the private Unix socket.
func (c *Client) Watch(ctx context.Context, room string, after uint64, receive func(rooms.Event) error) error {
	target := fmt.Sprintf("http://coord/watch?room=%s&after=%d", url.QueryEscape(room), after)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	if c.owner != "" {
		req.Header.Set("X-Coord-Owner", c.owner)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("room stream: HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(response.Body)
	for {
		var event rooms.Event
		if err = decoder.Decode(&event); err != nil {
			return err
		}
		if err = receive(event); err != nil {
			return err
		}
	}
}
