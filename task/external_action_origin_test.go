package task

import (
	"strings"
	"testing"
)

func validExternalOrigin() ExternalActionOrigin {
	return ExternalActionOrigin{Kind: OriginCIPipeline, ProjectID: "coord", TaskID: "task_01ARZ3NDEKTSV4RRFFQ69G5FAV", RunID: "run-1", AssignmentID: "assignment-1", Subject: SubjectBinding{RepositoryID: "naghuale/coord", CommitSHA: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40)}, CommandID: "cmd-1", EventID: "event-1"}
}

func TestExternalActionOriginRequiresKnownTypedKind(t *testing.T) {
	o := validExternalOrigin()
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	o.Kind = OriginKind("free-form")
	if err := o.Validate(); err == nil {
		t.Fatal("free-form origin accepted")
	}
}

func TestExternalActionOriginRequiresBoundSubjectAndCommandEvent(t *testing.T) {
	for _, mutate := range []func(*ExternalActionOrigin){
		func(o *ExternalActionOrigin) { o.Subject.TreeHash = "" },
		func(o *ExternalActionOrigin) { o.CommandID = "" },
		func(o *ExternalActionOrigin) { o.EventID = "" },
		func(o *ExternalActionOrigin) { o.AssignmentID = "" },
	} {
		o := validExternalOrigin()
		mutate(&o)
		if err := o.Validate(); err == nil {
			t.Fatalf("invalid origin accepted: %+v", o)
		}
	}
}
