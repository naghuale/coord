//go:build unix

package localchat

import (
	"context"
	"coord/internal/rooms"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

func (s *Service) resolvePeer(ctx context.Context, address string) (Peer, error) {
	peers, err := s.Peers(ctx)
	if err != nil {
		return Peer{}, err
	}
	var matches []Peer
	for _, p := range peers {
		if p.Ref == address || p.Address == address {
			matches = append(matches, p)
		}
	}
	if len(matches) != 1 {
		return Peer{}, fmt.Errorf("агент не найден или имя неоднозначно; используйте coord peers")
	}
	return matches[0], nil
}
func (s *Service) watch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("room")
	cursor, err := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	if err != nil && r.URL.Query().Get("after") != "" {
		http.Error(w, "invalid cursor", http.StatusBadRequest)
		return
	}
	events, err := s.rooms.Events(id, cursor)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	encoder := json.NewEncoder(w)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		for _, e := range events {
			if err = encoder.Encode(e); err != nil {
				return
			}
			cursor = e.Seq
		}
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-s.life.Done():
			return
		case <-tick.C:
		}
		events, err = s.rooms.Events(id, cursor)
		if err != nil {
			return
		}
	}
}

// RoomManager exposes the shared collaboration state to the private API.
func (s *Service) RoomManager() *rooms.Manager { return s.rooms }
