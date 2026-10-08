package task

import (
	"coord/taskid"
	"testing"
	"time"
)

func validTask() Task {
	stage := Execution
	now := time.Unix(100, 0).UTC()
	return Task{
		ID: taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV"), Title: "title", OwnerID: "owner",
		Revision: 1, LastEventSeq: 1,
		StagePlan:    StagePlan{Stages: []Stage{Execution, Review, Audit}, PolicyVersion: StagePlanPolicyVersion},
		CurrentStage: &stage, Status: Pending, CreatedAt: now, UpdatedAt: now,
	}
}

func TestTaskValidationSeparatesStageAndStatus(t *testing.T) {
	value := validTask()
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	value.Status = InProgress
	if err := value.Validate(); err != nil {
		t.Fatalf("IN_PROGRESS / EXECUTION rejected: %v", err)
	}
	value.Status, value.CurrentStage = Blocked, stagePtr(OwnerDecision)
	if err := value.Validate(); err != nil {
		t.Fatalf("BLOCKED / OWNER_DECISION rejected: %v", err)
	}
}

func TestTaskValidationRejectsInvalidPlanAndTerminalStage(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Task)
	}{
		{"review before execution", func(v *Task) { v.StagePlan.Stages = []Stage{Review, Execution} }},
		{"audit before review", func(v *Task) { v.StagePlan.Stages = []Stage{Execution, Audit, Review} }},
		{"owner decision in plan", func(v *Task) { v.StagePlan.Stages = []Stage{Execution, OwnerDecision} }},
		{"terminal with stage", func(v *Task) { v.Status = Completed }},
		{"unknown policy", func(v *Task) { v.StagePlan.PolicyVersion++ }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := validTask()
			tc.change(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("invalid Task was accepted")
			}
		})
	}
}

func stagePtr(value Stage) *Stage { return &value }
