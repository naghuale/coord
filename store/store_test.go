package store

import (
	"coord/envelope"
	"coord/fsm"
	"fmt"
	"testing"
)

func TestGetReturnsIsolatedRun(t *testing.T) {
	original := fsm.Run{RunID: "r", Seen: map[string]bool{"seen": true}, TaskMessages: []string{"task"}, Outbox: []envelope.Envelope{{MessageID: "message", ResultPayload: &envelope.ResultPayload{Outcome: "SUCCESS", Artifacts: []string{"artifact"}}}}}
	s := &Store{runs: map[string]fsm.Run{"r": original}}
	got, ok := s.Get("r")
	if !ok {
		t.Fatal("run missing")
	}
	got.Seen["seen"] = false
	got.TaskMessages[0] = "changed"
	got.Outbox[0].ResultPayload.Artifacts[0] = "changed"
	again, _ := s.Get("r")
	if !again.Seen["seen"] || again.TaskMessages[0] != "task" || again.Outbox[0].ResultPayload.Artifacts[0] != "artifact" {
		t.Fatalf("Get exposed internal state: %+v", again)
	}
	if _, ok = s.Get("missing"); ok {
		t.Fatal("missing run exists")
	}
}

func BenchmarkGet(b *testing.B) {
	for _, count := range []int{1, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			s := &Store{runs: map[string]fsm.Run{}}
			for i := 0; i < count; i++ {
				id := fmt.Sprint(i)
				s.runs[id] = fsm.Run{RunID: id, Task: "task", Fence: 1, Attempt: 1, OwnerSession: "exec", Status: fsm.Started}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, ok := s.Get("0"); !ok {
					b.Fatal("missing")
				}
			}
		})
	}
}
