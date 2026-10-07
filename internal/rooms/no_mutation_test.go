//go:build unix

package rooms

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// roomsFingerprint is the observable state of the manager: the serialized
// snapshot plus, per room, the event count and the last seq. DP-005 requires a
// rejected request to leave all of it byte-identical.
type roomsFingerprint struct {
	Snapshot string
	Rooms    map[string]roomFingerprint
}

type roomFingerprint struct {
	Events  int
	LastSeq uint64
}

func fingerprintRooms(t *testing.T, m *Manager) roomsFingerprint {
	t.Helper()
	raw, err := json.Marshal(m.data)
	if err != nil {
		t.Fatal(err)
	}
	fp := roomsFingerprint{Snapshot: string(raw), Rooms: map[string]roomFingerprint{}}
	for id, r := range m.data.Rooms {
		entry := roomFingerprint{Events: len(r.Events)}
		if n := len(r.Events); n > 0 {
			entry.LastSeq = r.Events[n-1].Seq
		}
		fp.Rooms[id] = entry
	}
	return fp
}

// assertNoMutation is the DP-005 helper: op must fail with ErrInvalidRequest and
// must not have touched state. Mutation that catches it: moving the blocker
// write before the retry_at check, or appending an event before returning
// invalidRequest.
func assertNoMutation(t *testing.T, m *Manager, op func() error) {
	t.Helper()
	before := fingerprintRooms(t, m)
	err := op()
	if err == nil {
		t.Fatal("ожидался ErrInvalidRequest, операция прошла успешно")
	}
	if !strings.Contains(err.Error(), ErrInvalidRequest.Error()) {
		t.Fatalf("ожидался ErrInvalidRequest, получено %v", err)
	}
	after := fingerprintRooms(t, m)
	if after.Snapshot != before.Snapshot {
		t.Errorf("отказ изменил snapshot:\nдо:   %s\nпосле: %s", before.Snapshot, after.Snapshot)
	}
	for id, was := range before.Rooms {
		now := after.Rooms[id]
		if now != was {
			t.Errorf("отказ изменил комнату %s: до %+v, после %+v", id, was, now)
		}
	}
	for id := range after.Rooms {
		if _, ok := before.Rooms[id]; !ok {
			t.Errorf("отказ создал комнату %s", id)
		}
	}
}

// TestRejectedBlockerLeavesNoTrace walks every path that returns invalidRequest
// in this package and pins that a malformed request mutates nothing.
func TestRejectedBlockerLeavesNoTrace(t *testing.T) {
	m, _ := fixture(t)
	room, err := m.Create("TASK-A", "Implement", "")
	if err != nil {
		t.Fatal(err)
	}
	member := sessionFixture(t, m, room.ID, "agent-a", "executor")
	tooLong := strings.Repeat("s", 257)
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	for _, tc := range []struct {
		name string
		op   func() error
	}{
		{"unknown reason", func() error {
			return m.SetBlocker(member.SessionID, "made_up_reason", "owner", time.Time{}, "")
		}},
		{"empty source", func() error {
			return m.SetBlocker(member.SessionID, BlockedProviderQuota, "", time.Time{}, "")
		}},
		{"source longer than 256 bytes", func() error {
			return m.SetBlocker(member.SessionID, BlockedProviderQuota, tooLong, time.Time{}, "")
		}},
		{"retry_at in the past", func() error {
			return m.SetBlocker(member.SessionID, BlockedProviderQuota, "owner", past, "")
		}},
		{"unknown reason for unknown session", func() error {
			return m.SetBlocker("no-such-session", "made_up_reason", "owner", time.Time{}, "")
		}},
		{"valid request still mutates", nil},
	} {
		if tc.op == nil {
			continue
		}
		t.Run(tc.name, func(t *testing.T) { assertNoMutation(t, m, tc.op) })
	}

	// Контроль: принятый запрос обязан оставить след, иначе проверка выше
	// проходила бы на no-op реализации.
	clean := fingerprintRooms(t, m)
	if err = m.SetBlocker(member.SessionID, BlockedProviderQuota, "owner", future, ""); err != nil {
		t.Fatal(err)
	}
	blocked := fingerprintRooms(t, m)
	was, now := clean.Rooms[room.ID], blocked.Rooms[room.ID]
	if now.Events != was.Events+1 || now.LastSeq <= was.LastSeq {
		t.Fatalf("валидный блокер не записан: до %+v, после %+v", was, now)
	}
	if blocked.Snapshot == clean.Snapshot {
		t.Fatal("валидный блокер не изменил snapshot")
	}
	assertNoMutation(t, m, func() error {
		return m.SetBlocker(member.SessionID, "made_up_reason", "owner", time.Time{}, "")
	})
}
