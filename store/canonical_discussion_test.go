package store

import (
	"coord/envelope"
	"coord/roles"
	"coord/task"
	"coord/taskid"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func discussionCommandState() CanonicalState {
	state := taskStateWithRun()
	run := state.Runs["run-1"]
	run.CoordinatorSession = "coordd"
	run.TargetSHA = "0000000000000000000000000000000000000000"
	state.Runs[run.RunID] = run
	return state
}

func TestDiscussionAgentCommandCommitsTypedNodeAndRebuildsProjection(t *testing.T) {
	state := discussionCommandState()
	p := &canonicalMemory{state: state}
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	s := newCanonicalWithTestClock(t, p, clock)
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	frame := validAgentCommandFrame(state, "discussion-question")
	before := p.saveCalls
	receipt, replayed, err := s.RecordDiscussionNode(frame, id, state.Tasks[id].Revision, task.DiscussionNode{Type: task.DiscussionQuestion, Summary: "Should we proceed?", Refs: []task.Reference{}})
	if err != nil || replayed {
		t.Fatalf("record node replay=%v err=%v", replayed, err)
	}
	if p.saveCalls != before+1 {
		t.Fatalf("durable saves=%d, want %d", p.saveCalls, before+1)
	}
	if len(receipt.EventIDs) != 1 || p.state.Events[len(p.state.Events)-1].Type != task.DiscussionNodeRecorded {
		t.Fatalf("receipt=%+v event=%+v", receipt, p.state.Events[len(p.state.Events)-1])
	}
	event := p.state.Events[len(p.state.Events)-1]
	node := event.DiscussionNode
	if node == nil || node.NodeID != event.ID+"/node" || node.Actor != "agent-executor" || node.Role != string(roles.Executor) || node.TaskID != id {
		t.Fatalf("Discussion node metadata not Coord-generated: %+v", node)
	}
	graph, err := s.DiscussionGraph(id)
	if err != nil || len(graph.Nodes) != 1 || graph.Nodes[0].NodeID != node.NodeID {
		t.Fatalf("graph=%+v err=%v", graph, err)
	}
	reopened := newCanonicalWithTestClock(t, &canonicalMemory{state: p.state}, clock)
	rebuilt, err := reopened.DiscussionGraph(id)
	if err != nil || len(rebuilt.Nodes) != 1 || rebuilt.Nodes[0].NodeID != node.NodeID {
		t.Fatalf("rebuilt graph=%+v err=%v", rebuilt, err)
	}
}

func TestDiscussionDoesNotPromoteTransportEvents(t *testing.T) {
	state := taskStateWithRun()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	graph := task.ProjectDiscussion(state.Events, id)
	if len(graph.Nodes) != 0 || len(graph.Edges) != 0 {
		t.Fatalf("non-Discussion DomainEvents became graph items: %+v", graph)
	}
}

func TestDiscussionRejectsDanglingOrCrossTaskEdges(t *testing.T) {
	for _, tc := range []struct {
		name     string
		from, to string
	}{
		{"dangling", "missing", "missing"},
		{"disallowed pair", "question", "question"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := discussionCommandState()
			p := &canonicalMemory{state: state}
			clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
			s := newCanonicalWithTestClock(t, p, clock)
			id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
			question := task.DiscussionNode{Type: task.DiscussionQuestion, Summary: "Question", Refs: []task.Reference{}}
			if _, _, err := s.RecordDiscussionNode(validAgentCommandFrame(p.state, "q1"), id, p.state.Tasks[id].Revision, question); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.RecordDiscussionNode(validAgentCommandFrame(p.state, "q2"), id, p.state.Tasks[id].Revision, question); err != nil {
				t.Fatal(err)
			}
			var from, to string
			if tc.name == "dangling" {
				from, to = "missing-node", "missing-target"
			} else {
				g, _ := s.DiscussionGraph(id)
				from, to = g.Nodes[0].NodeID, g.Nodes[1].NodeID
			}
			before, saves := p.state, p.saveCalls
			_, replayed, err := s.RecordDiscussionEdge(validAgentCommandFrame(p.state, "bad-edge"), id, p.state.Tasks[id].Revision, task.DiscussionEdge{Type: task.DiscussionAnswers, FromNodeID: from, ToNodeID: to})
			if err == nil || replayed || p.saveCalls != saves || len(p.state.Events) != len(before.Events) {
				t.Fatalf("invalid edge replay=%v err=%v saves=%d", replayed, err, p.saveCalls)
			}
		})
	}
}

func TestDiscussionDecisionAndResultReferencesFailClosed(t *testing.T) {
	for name, node := range map[string]task.DiscussionNode{
		"owner decision":  {Type: task.DiscussionDecision, Summary: "decision", Refs: []task.Reference{}, OwnerDecisionRef: "owner-decision-1"},
		"conflict":        {Type: task.DiscussionDecision, Summary: "decision", Refs: []task.Reference{}, ConflictID: "conflict-1"},
		"executor result": {Type: task.DiscussionResult, Summary: "result", Refs: []task.Reference{}, ResultRef: "missing-run/result"},
		"artifact":        {Type: task.DiscussionQuestion, Summary: "question", Refs: []task.Reference{{Kind: task.ReferenceArtifact, ID: "artifact-1"}}},
	} {
		t.Run(name, func(t *testing.T) {
			state := discussionCommandState()
			p := &canonicalMemory{state: state}
			clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
			s := newCanonicalWithTestClock(t, p, clock)
			id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
			saves := p.saveCalls
			_, replayed, err := s.RecordDiscussionNode(validAgentCommandFrame(state, "closed-ref"), id, state.Tasks[id].Revision, node)
			if err == nil || replayed || p.saveCalls != saves {
				t.Fatalf("fail-closed reference replay=%v err=%v saves=%d", replayed, err, p.saveCalls)
			}
		})
	}
}

func TestDiscussionResultReferenceRequiresMatchingEventType(t *testing.T) {
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	for _, tc := range []struct {
		name      string
		ref       string
		refType   task.EventType
		eventType task.EventType
	}{
		{"review ref cannot name audit event", "audit-event", task.ReviewResultRecorded, task.AuditResultRecorded},
		{"audit ref cannot name review event", "review-event", task.AuditResultRecorded, task.ReviewResultRecorded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := task.DiscussionNode{TaskID: id, Type: task.DiscussionFinding, Refs: []task.Reference{{Kind: task.ReferenceTask, ID: id.String()}}}
			if tc.refType == task.ReviewResultRecorded {
				node.ReviewResultRef = tc.ref
			} else {
				node.AuditResultRef = tc.ref
			}
			state := CanonicalState{Events: []task.DomainEvent{{ID: tc.ref, TaskID: id, EventSeq: 2, Type: tc.eventType}}}
			if err := validateDiscussionNodeReferences(state, node, 3); err == nil {
				t.Fatal("FINDING accepted a result event of the wrong type")
			}
		})
	}
}

func TestDiscussionReferencesRequireSameTaskAndPriorEvent(t *testing.T) {
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	other := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAW")
	state := CanonicalState{Events: []task.DomainEvent{
		{ID: "prior", TaskID: id, EventSeq: 2, Type: task.TaskCreated},
		{ID: "foreign", TaskID: other, EventSeq: 2, Type: task.TaskCreated},
		{ID: "future", TaskID: id, EventSeq: 3, Type: task.TaskCreated},
	}}
	for _, tc := range []struct {
		name    string
		ref     task.Reference
		wantErr bool
	}{
		{"same task", task.Reference{Kind: task.ReferenceTask, ID: id.String()}, false},
		{"foreign task", task.Reference{Kind: task.ReferenceTask, ID: other.String()}, true},
		{"prior event", task.Reference{Kind: task.ReferenceDomainEvent, ID: "prior"}, false},
		{"foreign event", task.Reference{Kind: task.ReferenceDomainEvent, ID: "foreign"}, true},
		{"future event", task.Reference{Kind: task.ReferenceDomainEvent, ID: "future"}, true},
		{"missing event", task.Reference{Kind: task.ReferenceDomainEvent, ID: "missing"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDiscussionNodeReferences(state, task.DiscussionNode{TaskID: id, Refs: []task.Reference{tc.ref}}, 3)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate refs err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestDiscussionSnapshotValidationRejectsInconsistentNode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*CanonicalState)
	}{
		{"node ID", func(s *CanonicalState) { s.Events[len(s.Events)-1].DiscussionNode.NodeID = "wrong/node" }},
		{"node author", func(s *CanonicalState) {
			e := &s.Events[len(s.Events)-1]
			e.ActorID, e.DiscussionNode.Actor = "forged", "forged"
		}},
		{"node summary metadata", func(s *CanonicalState) { s.Events[len(s.Events)-1].Summary = "different" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := recordedDiscussionNodeSnapshot(t)
			tc.mutate(&state)
			if err := ValidateCanonicalState(state); err == nil {
				t.Fatal("inconsistent Discussion snapshot accepted")
			}
		})
	}
	t.Run("node author does not match Run", func(t *testing.T) {
		state := recordedDiscussionNodeSnapshot(t)
		e := &state.Events[len(state.Events)-1]
		e.ActorID, e.DiscussionNode.Actor = "forged", "forged"
		if err := ValidateCanonicalState(state); err == nil {
			t.Fatal("snapshot with node author outside Run.RoleSelection accepted")
		}
	})
	t.Run("duplicate node ID in startup validator", func(t *testing.T) {
		state := recordedDiscussionNodeSnapshot(t)
		state.Events = append(state.Events, state.Events[len(state.Events)-1])
		if err := validateCanonicalDiscussion(state); err == nil {
			t.Fatal("startup discussion validator accepted duplicate node ID")
		}
	})
}

func TestDiscussionSnapshotValidationRejectsEdgeAuthorOutsideRun(t *testing.T) {
	state := recordedDiscussionEdgeSnapshot(t)
	e := &state.Events[len(state.Events)-1]
	e.ActorID, e.DiscussionEdge.Actor = "forged", "forged"
	if err := ValidateCanonicalState(state); err == nil {
		t.Fatal("snapshot with edge author outside Run.RoleSelection accepted")
	}
}

func TestDiscussionRunRoleAssignmentIsRequired(t *testing.T) {
	state := discussionCommandState()
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, &canonicalTestClock{now: time.Unix(5000, 0).UTC()})
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	run := s.state.Runs["run-1"]
	run.RoleSelection = nil
	s.state.Runs["run-1"] = run
	if _, _, err := s.RecordDiscussionNode(validAgentCommandFrame(state, "missing-node-role"), id, state.Tasks[id].Revision, task.DiscussionNode{Type: task.DiscussionQuestion, Summary: "q", Refs: []task.Reference{}}); err == nil {
		t.Fatal("Discussion node accepted without canonical Run role assignment")
	}
	if _, _, err := s.RecordDiscussionEdge(validAgentCommandFrame(state, "missing-edge-role"), id, state.Tasks[id].Revision, task.DiscussionEdge{Type: task.DiscussionRefersTo, FromNodeID: "n", ToNodeID: "n"}); err == nil {
		t.Fatal("Discussion edge accepted without canonical Run role assignment")
	}
}

func recordedDiscussionNodeSnapshot(t *testing.T) CanonicalState {
	t.Helper()
	state := discussionCommandState()
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, &canonicalTestClock{now: time.Unix(5000, 0).UTC()})
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if _, _, err := s.RecordDiscussionNode(validAgentCommandFrame(state, "snapshot-node"), id, state.Tasks[id].Revision, task.DiscussionNode{Type: task.DiscussionQuestion, Summary: "Question", Refs: []task.Reference{}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := copyCanonicalState(p.state)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func recordedDiscussionEdgeSnapshot(t *testing.T) CanonicalState {
	t.Helper()
	state := discussionCommandState()
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, &canonicalTestClock{now: time.Unix(5000, 0).UTC()})
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if _, _, err := s.RecordDiscussionNode(validAgentCommandFrame(p.state, "edge-node"), id, p.state.Tasks[id].Revision, task.DiscussionNode{Type: task.DiscussionQuestion, Summary: "Question", Refs: []task.Reference{}}); err != nil {
		t.Fatal(err)
	}
	nodeID := p.state.Events[len(p.state.Events)-1].DiscussionNode.NodeID
	if _, _, err := s.RecordDiscussionEdge(validAgentCommandFrame(p.state, "edge"), id, p.state.Tasks[id].Revision, task.DiscussionEdge{Type: task.DiscussionRefersTo, FromNodeID: nodeID, ToNodeID: nodeID}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := copyCanonicalState(p.state)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestDiscussionNodeRequiresActor(t *testing.T) {
	for _, actor := range []string{"", " \t"} {
		t.Run(map[string]string{"": "empty", " \t": "whitespace"}[actor], func(t *testing.T) {
			s, p, id, clock, _, diskBefore, graphBefore, saves := discussionValidationFixture(t)
			node := task.DiscussionNode{NodeID: "schema-node", Type: task.DiscussionQuestion, TaskID: id, Actor: actor, Role: string(roles.Executor), CreatedAt: time.Unix(6000, 0).UTC(), Summary: "question", Refs: []task.Reference{}}
			if err := node.Validate(); !errors.Is(err, task.ErrDiscussionNodeActorRequired) {
				t.Fatalf("Validate error=%v, want ErrDiscussionNodeActorRequired", err)
			}
			detachDiscussionMemoryState(t, s)
			setDiscussionCommandAgentID(s, actor)
			before, err := s.CanonicalSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			_, replayed, err := s.RecordDiscussionNode(validAgentCommandFrame(p.state, "bad-node-actor-"+map[string]string{"": "empty", " \t": "whitespace"}[actor]), id, before.Tasks[id].Revision, task.DiscussionNode{Type: task.DiscussionQuestion, Summary: "invalid author", Refs: []task.Reference{}})
			if actor == "" {
				if !errors.Is(err, ErrAgentCommandMissingAgentID) || replayed {
					t.Fatalf("empty AgentID command err=%v replayed=%v", err, replayed)
				}
			} else if !errors.Is(err, ErrAgentCommandTransitionForbidden) || !strings.Contains(err.Error(), task.ErrDiscussionNodeActorRequired.Error()) || replayed {
				t.Fatalf("whitespace Actor should fail in node schema: err=%v replayed=%v", err, replayed)
			}
			assertDiscussionValidationStoreUnchanged(t, s, p, id, clock, before, diskBefore, graphBefore, saves)
		})
	}
}

func TestDiscussionNodeRequiresRole(t *testing.T) {
	for _, role := range []string{"", " \t"} {
		t.Run(map[string]string{"": "empty", " \t": "whitespace"}[role], func(t *testing.T) {
			s, p, id, clock, _, diskBefore, graphBefore, saves := discussionValidationFixture(t)
			node := task.DiscussionNode{NodeID: "schema-node", Type: task.DiscussionQuestion, TaskID: id, Actor: "agent", Role: role, CreatedAt: time.Unix(6000, 0).UTC(), Summary: "question", Refs: []task.Reference{}}
			if err := node.Validate(); !errors.Is(err, task.ErrDiscussionNodeRoleRequired) {
				t.Fatalf("Validate error=%v, want ErrDiscussionNodeRoleRequired", err)
			}
			detachDiscussionMemoryState(t, s)
			run := s.state.Runs["run-1"]
			run.RoleSelection.Profile.Role = roles.Role(role)
			s.state.Runs[run.RunID] = run
			before, err := s.CanonicalSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			_, replayed, err := s.RecordDiscussionNode(validAgentCommandFrame(p.state, "bad-node-role-"+map[string]string{"": "empty", " \t": "whitespace"}[role]), id, before.Tasks[id].Revision, task.DiscussionNode{Type: task.DiscussionQuestion, Summary: "invalid role", Refs: []task.Reference{}})
			if !errors.Is(err, ErrAgentCommandWrongRole) || replayed {
				t.Fatalf("invalid Run role should fail before callback: err=%v replayed=%v", err, replayed)
			}
			assertDiscussionValidationStoreUnchanged(t, s, p, id, clock, before, diskBefore, graphBefore, saves)
		})
	}
}

func TestDiscussionEdgeRequiresActor(t *testing.T) {
	for _, actor := range []string{"", " \t"} {
		t.Run(map[string]string{"": "empty", " \t": "whitespace"}[actor], func(t *testing.T) {
			s, p, id, clock, _, diskBefore, graphBefore, saves := discussionValidationFixture(t)
			edge := task.DiscussionEdge{EdgeID: "schema-edge", Type: task.DiscussionRefersTo, FromNodeID: "from", ToNodeID: "to", TaskID: id, Actor: actor, CreatedAt: time.Unix(6000, 0).UTC()}
			if err := edge.Validate(); !errors.Is(err, task.ErrDiscussionEdgeActorRequired) {
				t.Fatalf("Validate error=%v, want ErrDiscussionEdgeActorRequired", err)
			}
			detachDiscussionMemoryState(t, s)
			setDiscussionCommandAgentID(s, actor)
			before, err := s.CanonicalSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			_, replayed, err := s.RecordDiscussionEdge(validAgentCommandFrame(p.state, "bad-edge-actor-"+map[string]string{"": "empty", " \t": "whitespace"}[actor]), id, before.Tasks[id].Revision, task.DiscussionEdge{Type: task.DiscussionRefersTo, FromNodeID: "from", ToNodeID: "to"})
			if actor == "" {
				if !errors.Is(err, ErrAgentCommandMissingAgentID) || replayed {
					t.Fatalf("empty AgentID command err=%v replayed=%v", err, replayed)
				}
			} else if !errors.Is(err, ErrAgentCommandTransitionForbidden) || !strings.Contains(err.Error(), task.ErrDiscussionEdgeActorRequired.Error()) || replayed {
				t.Fatalf("whitespace Actor should fail in edge schema: err=%v replayed=%v", err, replayed)
			}
			assertDiscussionValidationStoreUnchanged(t, s, p, id, clock, before, diskBefore, graphBefore, saves)
		})
	}
}

func detachDiscussionMemoryState(t *testing.T, s *CanonicalStore) {
	t.Helper()
	state, err := copyCanonicalState(s.state)
	if err != nil {
		t.Fatal(err)
	}
	s.state = state
}

func setDiscussionCommandAgentID(s *CanonicalStore, agentID string) {
	run := s.state.Runs["run-1"]
	run.RoleSelection.AgentID = agentID
	s.state.Runs[run.RunID] = run
	for address, session := range s.state.AgentSessions {
		if session.AgentID == "agent-executor" {
			session.AgentID = agentID
			s.state.AgentSessions[address] = session
		}
	}
}

func discussionValidationFixture(t *testing.T) (*CanonicalStore, *canonicalMemory, taskid.ID, *canonicalTestClock, CanonicalState, CanonicalState, task.DiscussionGraph, int) {
	t.Helper()
	state := discussionCommandState()
	p := &canonicalMemory{state: state}
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	s := newCanonicalWithTestClock(t, p, clock)
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if _, _, err := s.RecordDiscussionNode(validAgentCommandFrame(state, "schema-baseline"), id, state.Tasks[id].Revision, task.DiscussionNode{Type: task.DiscussionQuestion, Summary: "baseline", Refs: []task.Reference{}}); err != nil {
		t.Fatal(err)
	}
	before, err := s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	diskBefore, err := copyCanonicalState(p.state)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := s.DiscussionGraph(id)
	if err != nil {
		t.Fatal(err)
	}
	return s, p, id, clock, before, diskBefore, graph, p.saveCalls
}

func assertDiscussionValidationStoreUnchanged(t *testing.T, s *CanonicalStore, p *canonicalMemory, id taskid.ID, clock *canonicalTestClock, before, diskBefore CanonicalState, graphBefore task.DiscussionGraph, saves int) {
	t.Helper()
	after, err := s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Tasks[id], after.Tasks[id]) {
		t.Fatal("Task changed during schema validation")
	}
	if !reflect.DeepEqual(before.Events, after.Events) {
		t.Fatal("Events changed or a partial Discussion item was written")
	}
	if !reflect.DeepEqual(before.Inbox, after.Inbox) {
		t.Fatal("Inbox receipt changed or was added")
	}
	if !reflect.DeepEqual(before.Outbox, after.Outbox) {
		t.Fatal("Outbox changed or an acknowledgement was added")
	}
	if p.saveCalls != saves {
		t.Fatalf("durable saves=%d, want %d", p.saveCalls, saves)
	}
	diskAfter, err := copyCanonicalState(p.state)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(diskBefore, diskAfter) {
		t.Fatal("durable state changed during schema validation")
	}
	graphAfter, err := s.DiscussionGraph(id)
	if err != nil || !reflect.DeepEqual(graphBefore, graphAfter) {
		t.Fatalf("Discussion Graph changed: before=%+v after=%+v err=%v", graphBefore, graphAfter, err)
	}
	reopened := newCanonicalWithTestClock(t, &canonicalMemory{state: p.state}, clock)
	reopenedState, err := reopened.CanonicalSnapshot()
	if err != nil || !reflect.DeepEqual(diskBefore, reopenedState) {
		t.Fatalf("reopened durable state differs: err=%v", err)
	}
	reopenedGraph, err := reopened.DiscussionGraph(id)
	if err != nil || !reflect.DeepEqual(graphBefore, reopenedGraph) {
		t.Fatalf("reopened Discussion Graph differs: graph=%+v err=%v", reopenedGraph, err)
	}
}

func TestDiscussionResultReferenceFailsClosedUntilExecutorResultSlice(t *testing.T) {
	state := discussionCommandState()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	run := state.Runs["run-1"]
	run.Result = &envelope.ResultPayload{Outcome: "SUCCESS", Summary: "done"}
	state.Runs[run.RunID] = run
	p := &canonicalMemory{state: state}
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	s := newCanonicalWithTestClock(t, p, clock)
	saves := p.saveCalls
	_, _, err := s.RecordDiscussionNode(validAgentCommandFrame(state, "result-ref-before-6"), id, state.Tasks[id].Revision, task.DiscussionNode{Type: task.DiscussionResult, Summary: "result", Refs: []task.Reference{}, ResultRef: "run-1/result"})
	if err == nil || p.saveCalls != saves {
		t.Fatalf("ResultRef accepted before #6: err=%v saves=%d", err, p.saveCalls)
	}
}

func TestDiscussionRejectsCrossTaskEdgeEndpoint(t *testing.T) {
	state := discussionCommandState()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	other := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAW")
	foreign := task.DiscussionNode{NodeID: "foreign-node", Type: task.DiscussionQuestion, TaskID: other, Actor: "agent", Role: string(roles.Executor), CreatedAt: time.Unix(5000, 0).UTC(), Summary: "foreign", Refs: []task.Reference{}}
	state.Events = append(state.Events, task.DomainEvent{ID: task.EventID(other, 1), TaskID: other, EventSeq: 1, TaskRevision: 1, Type: task.DiscussionNodeRecorded, ActorID: foreign.Actor, Summary: foreign.Summary, CreatedAt: foreign.CreatedAt, DiscussionNode: &foreign})
	edge := task.DiscussionEdge{EdgeID: "edge", Type: task.DiscussionRefersTo, FromNodeID: "foreign-node", ToNodeID: "foreign-node", TaskID: id, Actor: "agent", CreatedAt: time.Unix(5001, 0).UTC()}
	if err := validateDiscussionEdgeReferences(state, edge, 10); err == nil {
		t.Fatal("cross-Task edge endpoint accepted")
	}
}

func TestDiscussionEdgeTypePairIsEnforced(t *testing.T) {
	types := []task.DiscussionNodeType{task.DiscussionQuestion, task.DiscussionProposal, task.DiscussionFact, task.DiscussionFinding, task.DiscussionDecision, task.DiscussionAction, task.DiscussionResult, task.DiscussionBlocker}
	allowed := map[task.DiscussionEdgeType]map[[2]task.DiscussionNodeType]bool{
		task.DiscussionAnswers:  {{task.DiscussionFact, task.DiscussionQuestion}: true, {task.DiscussionProposal, task.DiscussionQuestion}: true, {task.DiscussionDecision, task.DiscussionQuestion}: true},
		task.DiscussionSupports: {{task.DiscussionFact, task.DiscussionProposal}: true, {task.DiscussionFact, task.DiscussionDecision}: true, {task.DiscussionFact, task.DiscussionFinding}: true, {task.DiscussionFinding, task.DiscussionProposal}: true, {task.DiscussionFinding, task.DiscussionDecision}: true, {task.DiscussionFinding, task.DiscussionFinding}: true, {task.DiscussionResult, task.DiscussionProposal}: true, {task.DiscussionResult, task.DiscussionDecision}: true, {task.DiscussionResult, task.DiscussionFinding}: true},
		task.DiscussionCausedBy: {{task.DiscussionDecision, task.DiscussionFinding}: true, {task.DiscussionAction, task.DiscussionDecision}: true, {task.DiscussionBlocker, task.DiscussionFact}: true, {task.DiscussionBlocker, task.DiscussionFinding}: true},
		task.DiscussionResultOf: {{task.DiscussionResult, task.DiscussionAction}: true},
		task.DiscussionBlocks:   {{task.DiscussionBlocker, task.DiscussionAction}: true, {task.DiscussionBlocker, task.DiscussionDecision}: true, {task.DiscussionBlocker, task.DiscussionProposal}: true},
	}
	for edgeType, pairs := range allowed {
		for _, from := range types {
			for _, to := range types {
				if got, want := allowedDiscussionEdgePair(edgeType, from, to), pairs[[2]task.DiscussionNodeType{from, to}]; got != want {
					t.Errorf("%s %s→%s allowed=%v want %v", edgeType, from, to, got, want)
				}
			}
		}
	}
	for _, from := range types {
		for _, to := range types {
			if !allowedDiscussionEdgePair(task.DiscussionRefersTo, from, to) {
				t.Errorf("REFERS_TO rejected %s→%s", from, to)
			}
		}
	}
	if allowedDiscussionEdgePair(task.DiscussionEdgeType("UNKNOWN"), task.DiscussionQuestion, task.DiscussionQuestion) {
		t.Fatal("unknown edge type accepted")
	}
}

func TestDiscussionEventCannotChangeTaskWorkflow(t *testing.T) {
	previous := discussionCommandState()
	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	current := next.Tasks[id]
	current.Revision++
	current.LastEventSeq++
	current.UpdatedAt = current.UpdatedAt.Add(time.Second)
	current.Status = task.Blocked
	next.Tasks[id] = current
	node := task.DiscussionNode{NodeID: task.EventID(id, current.LastEventSeq) + "/node", Type: task.DiscussionQuestion, TaskID: id, Actor: "agent-executor", Role: string(roles.Executor), CreatedAt: current.UpdatedAt, Summary: "Question", Refs: []task.Reference{}}
	next.Events = append(next.Events, task.DomainEvent{ID: task.EventID(id, current.LastEventSeq), TaskID: id, RunID: "run-1", EventSeq: current.LastEventSeq, TaskRevision: current.Revision, TaskFence: current.Fence, Type: task.DiscussionNodeRecorded, ActorID: node.Actor, Summary: node.Summary, CreatedAt: node.CreatedAt, DiscussionNode: &node})
	if err := ValidateCanonicalTransition(previous, next); err == nil {
		t.Fatal("Discussion event changed workflow status")
	}
}

func TestDiscussionAgentCommandUsesVerifiedActorAndIsReplaySafe(t *testing.T) {
	state := discussionCommandState()
	p := &canonicalMemory{state: state}
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	s := newCanonicalWithTestClock(t, p, clock)
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	frame := validAgentCommandFrame(state, "same-discussion-command")
	node := task.DiscussionNode{Type: task.DiscussionQuestion, Summary: "Question", Refs: []task.Reference{}}
	receipt, replayed, err := s.RecordDiscussionNode(frame, id, state.Tasks[id].Revision, node)
	if err != nil || replayed {
		t.Fatalf("first command replay=%v err=%v", replayed, err)
	}
	saves := p.saveCalls
	replayedReceipt, replayed, err := s.RecordDiscussionNode(frame, id, state.Tasks[id].Revision, node)
	if err != nil || !replayed || saves != p.saveCalls || receipt.EventIDs[0] != replayedReceipt.EventIDs[0] {
		t.Fatalf("replay=%v err=%v saves=%d/%d receipt=%+v", replayed, err, saves, p.saveCalls, replayedReceipt)
	}
	if receipt.ActorID != "agent-executor" {
		t.Fatalf("actor=%q", receipt.ActorID)
	}
}

func TestDiscussionAgentCommandFailureDoesNotPublish(t *testing.T) {
	state := discussionCommandState()
	p := &canonicalMemory{state: state}
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	s := newCanonicalWithTestClock(t, p, clock)
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	before, _ := copyCanonicalState(p.state)
	saves := p.saveCalls
	_, replayed, err := s.RecordDiscussionNode(validAgentCommandFrame(state, "invalid-node"), id, state.Tasks[id].Revision, task.DiscussionNode{Type: task.DiscussionFact, Summary: "missing refs", Refs: []task.Reference{}})
	if err == nil || replayed || p.saveCalls != saves {
		t.Fatalf("failure replay=%v err=%v saves=%d", replayed, err, p.saveCalls)
	}
	actual, _ := s.CanonicalSnapshot()
	if !reflect.DeepEqual(actual, before) {
		t.Fatal("failed Discussion command published state")
	}
}

func TestDiscussionRejectsCallerGeneratedIdentity(t *testing.T) {
	state := discussionCommandState()
	p := &canonicalMemory{state: state}
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	s := newCanonicalWithTestClock(t, p, clock)
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	saves := p.saveCalls
	for name, set := range map[string]func(*task.DiscussionNode){
		"node ID":   func(n *task.DiscussionNode) { n.NodeID = "caller" },
		"TaskID":    func(n *task.DiscussionNode) { n.TaskID = id },
		"Actor":     func(n *task.DiscussionNode) { n.Actor = "forged" },
		"Role":      func(n *task.DiscussionNode) { n.Role = "OWNER" },
		"timestamp": func(n *task.DiscussionNode) { n.CreatedAt = clock.Now() },
	} {
		t.Run(name, func(t *testing.T) {
			node := task.DiscussionNode{Type: task.DiscussionQuestion, Summary: "Question", Refs: []task.Reference{}}
			set(&node)
			_, _, err := s.RecordDiscussionNode(validAgentCommandFrame(state, "caller-"+name), id, state.Tasks[id].Revision, node)
			if err == nil || p.saveCalls != saves {
				t.Fatalf("caller metadata accepted or persisted: err=%v saves=%d", err, p.saveCalls)
			}
		})
	}
}
