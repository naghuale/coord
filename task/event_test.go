package task

import (
	"coord/taskid"
	"testing"
	"time"
)

func TestDomainEventRejectsReviewOrAuditPayloadForUnrelatedEventType(t *testing.T) {
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	now := time.Unix(100, 0).UTC()
	for _, tc := range []struct {
		name  string
		apply func(*DomainEvent)
	}{
		{"review payload", func(event *DomainEvent) {
			event.ReviewResult = &ReviewResult{Verdict: ReviewApproved, Summary: "reviewed", Findings: []Finding{}, InputArtifactRefs: []string{}, OutputArtifactRefs: []string{}, CreatedAt: now}
		}},
		{"audit payload", func(event *DomainEvent) {
			event.AuditResult = &AuditResult{Verdict: AuditPass, Summary: "audited", Findings: []Finding{}, InputArtifactRefs: []string{}, OutputArtifactRefs: []string{}, CreatedAt: now}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := DomainEvent{ID: EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1, TaskFence: 0, Type: TaskCreated, ActorID: "owner", CreatedAt: now}
			tc.apply(&event)
			if err := event.Validate(); err == nil {
				t.Fatal("non-result DomainEvent accepted Review/Audit payload")
			}
		})
	}
}
