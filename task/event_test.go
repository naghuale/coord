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

func TestDomainEventValidatesTypedExternalActionObservationState(t *testing.T) {
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	now := time.Unix(100, 0).UTC()
	for _, tc := range []struct {
		name, status, observation string
		valid                     bool
	}{
		{"pending", "PENDING", "", true}, {"observed", "OBSERVED", "observation-1", true}, {"completed-without-observation", "COMPLETED", "", false}, {"unknown", "UNKNOWN", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := DomainEvent{ID: EventID(id, 1), TaskID: id, RunID: "run", EventSeq: 1, TaskRevision: 1, Type: ExternalActionStateRecorded, ActorID: "coordd", Summary: "action-1", ExternalActionID: "action-1", ExternalActionStatus: tc.status, ExternalObservationID: tc.observation, CreatedAt: now}
			err := e.Validate()
			if tc.valid && err != nil {
				t.Fatal(err)
			}
			if !tc.valid && err == nil {
				t.Fatal("invalid action state event accepted")
			}
		})
	}
}

func TestDomainEventRequiresTypedCoordinatorCompletionPayload(t *testing.T) {
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	now := time.Unix(100, 0).UTC()
	e := DomainEvent{ID: EventID(id, 1), TaskID: id, RunID: "run", EventSeq: 1, TaskRevision: 1, Type: CoordinatorCompletionRecorded, ActorID: "coordd", Summary: "bad", CreatedAt: now}
	if err := e.Validate(); err == nil {
		t.Fatal("untyped completion event accepted")
	}
}
