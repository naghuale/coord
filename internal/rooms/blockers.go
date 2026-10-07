//go:build unix

package rooms

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// BlockedProviderQuota is the machine-readable blocker agreed in ALIGNED_UX.md §1.3.
const BlockedProviderQuota = "blocked_provider_quota"

var knownBlockers = map[string]bool{
	BlockedProviderQuota:        true,
	"blocked_model_unavailable": true,
	"blocked_local_environment": true,
}

// ErrInvalidRequest separates a malformed request from a state conflict: the
// caller cannot fix it by retrying the same payload, so the API answers 400.
var ErrInvalidRequest = errors.New("invalid request")

func invalidRequest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, args...))
}

// Blocker is a structured availability signal reported by owner, supervisor or
// integration. Coord never infers provider limits on its own; without an
// external signal availability stays unknown. A blocker stops new role
// assignments for the session but never touches checkpoint or journal state.
type Blocker struct {
	Reason     string    `json:"reason"`
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
	RetryAt    time.Time `json:"retry_at,omitempty"`
}

// SetBlocker records a machine-readable blocker on one logical session.
func (m *Manager) SetBlocker(sessionID, reason, source string, retryAt time.Time, as string) error {
	if !knownBlockers[reason] {
		return invalidRequest("неизвестная причина блокера %q; следующие допустимые команды: coord session unblock <session> либо список из docs/plans/ux-first-scenario/ALIGNED_UX.md", reason)
	}
	if strings.TrimSpace(source) == "" || len(source) > 256 {
		return invalidRequest("укажите источник блокера (owner|supervisor|integration, до 256 байт)")
	}
	if !retryAt.IsZero() && retryAt.Before(m.cfg.Now()) {
		return invalidRequest("--retry-at должен быть в будущем")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.update(func(d *snapshot) error {
		roomID, key, p, ok := lookup(d, sessionID)
		if !ok {
			return fmt.Errorf("сессия не найдена; выполните coord sessions")
		}
		r := d.Rooms[roomID]
		if _, _, err := roomAdmin(d, r, as); err != nil {
			return err
		}
		now := m.cfg.Now()
		if p.Blocker != nil && p.Blocker.Reason == reason && p.Blocker.Source == source && p.Blocker.RetryAt.Equal(retryAt.UTC()) {
			return nil
		}
		if err := capacity(r); err != nil {
			return err
		}
		p.Blocker = &Blocker{Reason: reason, Source: source, ObservedAt: now, RetryAt: retryAt.UTC()}
		r.Members[key] = p
		d.Rooms[roomID] = r
		appendEvent(d, &r, now, "blocked", p.AgentID, p.Role, fmt.Sprintf("Session %s blocked: %s (source %s)", p.SessionID, reason, source), false)
		d.Rooms[roomID] = r
		return nil
	})
}

// ClearBlocker removes a blocker only by explicit action; there is no automatic
// expiry because availability cannot be confirmed silently (watchdog policy).
func (m *Manager) ClearBlocker(sessionID, as string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.update(func(d *snapshot) error {
		roomID, key, p, ok := lookup(d, sessionID)
		if !ok {
			return fmt.Errorf("сессия не найдена; выполните coord sessions")
		}
		r := d.Rooms[roomID]
		if _, _, err := roomAdmin(d, r, as); err != nil {
			return err
		}
		if err := capacity(r); err != nil {
			return err
		}
		if p.Blocker == nil {
			return fmt.Errorf("у сессии нет блокера; следующее действие: coord sessions")
		}
		p.Blocker = nil
		r.Members[key] = p
		d.Rooms[roomID] = r
		appendEvent(d, &r, m.cfg.Now(), "unblocked", p.AgentID, p.Role, fmt.Sprintf("Session %s blocker cleared", p.SessionID), false)
		d.Rooms[roomID] = r
		return nil
	})
}
