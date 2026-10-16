package task

import (
	"coord/taskid"
	"encoding/json"
	"testing"
	"time"
)

func validDiscussionNode(nodeType DiscussionNodeType) DiscussionNode {
	n := DiscussionNode{NodeID: "node", Type: nodeType, TaskID: taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV"), Actor: "agent", Role: "EXECUTOR", CreatedAt: time.Unix(1, 0).UTC(), Summary: "summary", Refs: []Reference{}}
	switch nodeType {
	case DiscussionFact:
		n.Refs = []Reference{{Kind: ReferenceTask, ID: n.TaskID.String()}}
	case DiscussionFinding:
		n.Refs = []Reference{{Kind: ReferenceTask, ID: n.TaskID.String()}}
		n.ReviewResultRef = "review-event"
	case DiscussionDecision:
		n.ConflictID = "conflict"
	case DiscussionResult:
		n.ResultRef = "run/result"
	case DiscussionBlocker:
		n.ReasonCode = "WAITING"
	}
	return n
}

func TestDiscussionNodeTypesCarryRequiredFields(t *testing.T) {
	types := []DiscussionNodeType{DiscussionQuestion, DiscussionProposal, DiscussionFact, DiscussionFinding, DiscussionDecision, DiscussionAction, DiscussionResult, DiscussionBlocker}
	for _, nodeType := range types {
		t.Run(string(nodeType), func(t *testing.T) {
			if err := validDiscussionNode(nodeType).Validate(); err != nil {
				t.Fatalf("valid node: %v", err)
			}
		})
	}
	for name, mutate := range map[string]func(*DiscussionNode){
		"empty summary":              func(n *DiscussionNode) { n.Summary = " " },
		"nil refs":                   func(n *DiscussionNode) { n.Refs = nil },
		"unknown type":               func(n *DiscussionNode) { n.Type = "MESSAGE" },
		"unsupported type field":     func(n *DiscussionNode) { n.ResultRef = "caller-result" },
		"blocker missing reason":     func(n *DiscussionNode) { n.ReasonCode = "" },
		"decision missing reference": func(n *DiscussionNode) { n.ConflictID = "" },
		"result missing reference":   func(n *DiscussionNode) { n.ResultRef = "" },
	} {
		t.Run(name, func(t *testing.T) {
			node := validDiscussionNode(DiscussionQuestion)
			if name == "blocker missing reason" {
				node = validDiscussionNode(DiscussionBlocker)
			}
			if name == "decision missing reference" {
				node = validDiscussionNode(DiscussionDecision)
			}
			if name == "result missing reference" {
				node = validDiscussionNode(DiscussionResult)
			}
			mutate(&node)
			if err := node.Validate(); err == nil {
				t.Fatal("invalid node accepted")
			}
		})
	}
}

func TestDiscussionConflictFactRequiresR137References(t *testing.T) {
	node := validDiscussionNode(DiscussionFact)
	node.ConflictID, node.ReviewResultRef, node.AuditResultRef = "conflict", "review-event", "audit-event"
	if err := node.Validate(); err != nil {
		t.Fatalf("complete conflict FACT rejected: %v", err)
	}
	for _, clear := range []func(*DiscussionNode){
		func(n *DiscussionNode) { n.ConflictID = "" },
		func(n *DiscussionNode) { n.ReviewResultRef = "" },
		func(n *DiscussionNode) { n.AuditResultRef = "" },
	} {
		invalid := node
		clear(&invalid)
		if err := invalid.Validate(); err == nil {
			t.Fatal("incomplete conflict FACT accepted")
		}
	}
}

func TestDiscussionFindingRequiresExactlyOneResultReference(t *testing.T) {
	zero := validDiscussionNode(DiscussionFinding)
	zero.ReviewResultRef = ""
	if err := zero.Validate(); err == nil {
		t.Fatal("FINDING without result reference accepted")
	}
	two := validDiscussionNode(DiscussionFinding)
	two.AuditResultRef = "audit-event"
	if err := two.Validate(); err == nil {
		t.Fatal("FINDING with both result references accepted")
	}
	for _, set := range []func(*DiscussionNode){
		func(n *DiscussionNode) { n.AuditResultRef = "" },
		func(n *DiscussionNode) { n.ReviewResultRef = ""; n.AuditResultRef = "audit-event" },
	} {
		node := validDiscussionNode(DiscussionFinding)
		set(&node)
		if err := node.Validate(); err != nil {
			t.Fatalf("one result reference rejected: %v", err)
		}
	}
}

func TestDiscussionEdgeRejectsUnknownType(t *testing.T) {
	edge := DiscussionEdge{EdgeID: "edge", Type: DiscussionEdgeType("UNKNOWN"), FromNodeID: "from", ToNodeID: "to", TaskID: taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV"), Actor: "agent", CreatedAt: time.Unix(1, 0).UTC()}
	if err := edge.Validate(); err == nil {
		t.Fatal("unknown Discussion edge type accepted")
	}
}

func TestDiscussionRejectsUnknownFields(t *testing.T) {
	var node DiscussionNode
	if err := json.Unmarshal([]byte(`{"node_id":"n","type":"QUESTION","task_id":"task_01ARZ3NDEKTSV4RRFFQ69G5FAV","actor":"a","role":"EXECUTOR","created_at":"2026-10-10T00:00:00Z","summary":"s","refs":[],"unexpected":true}`), &node); err == nil {
		t.Fatal("unknown Discussion node field accepted")
	}
	var edge DiscussionEdge
	if err := json.Unmarshal([]byte(`{"edge_id":"e","type":"REFERS_TO","from_node_id":"n","to_node_id":"n","task_id":"task_01ARZ3NDEKTSV4RRFFQ69G5FAV","actor":"a","created_at":"2026-10-10T00:00:00Z","unexpected":true}`), &edge); err == nil {
		t.Fatal("unknown Discussion edge field accepted")
	}
}

func TestDiscussionProjectionRebuildsFromEvents(t *testing.T) {
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	node := validDiscussionNode(DiscussionFact)
	node.TaskID, node.NodeID = id, "node-id"
	edge := DiscussionEdge{EdgeID: "edge-id", Type: DiscussionRefersTo, FromNodeID: "node-id", ToNodeID: "node-id", TaskID: id, Actor: "agent", CreatedAt: time.Unix(2, 0).UTC()}
	events := []DomainEvent{{TaskID: id, Type: DiscussionNodeRecorded, DiscussionNode: &node}, {TaskID: id, Type: DiscussionEdgeRecorded, DiscussionEdge: &edge}}
	graph := ProjectDiscussion(events, id)
	if len(graph.Nodes) != 1 || graph.Nodes[0].NodeID != node.NodeID || len(graph.Edges) != 1 || graph.Edges[0].EdgeID != edge.EdgeID {
		t.Fatalf("projection=%+v", graph)
	}
	graph.Nodes[0].Refs[0].ID = "mutated"
	if events[0].DiscussionNode.Refs[0].ID == "mutated" {
		t.Fatal("projection aliases canonical event references")
	}
	if other := ProjectDiscussion(events, taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAW")); len(other.Nodes) != 0 || len(other.Edges) != 0 {
		t.Fatalf("cross-Task projection=%+v", other)
	}
}

// FindingID tests (WP-05 / M09)

func TestDiscussionNode_FindingIDAllowedOnFINDING(t *testing.T) {
	n := validDiscussionNode(DiscussionFinding)
	n.FindingID = "finding-001"
	if err := n.Validate(); err != nil {
		t.Errorf("FindingID on FINDING node must be accepted, got: %v", err)
	}
}

func TestDiscussionNode_FindingIDOptionalOnFINDING(t *testing.T) {
	n := validDiscussionNode(DiscussionFinding)
	n.FindingID = ""
	if err := n.Validate(); err != nil {
		t.Errorf("FindingID is optional on FINDING node, got: %v", err)
	}
}

// M09: FindingID on non-FINDING type must be rejected.
func TestMutation_M09_FindingIDOnNonFindingTypeRejected(t *testing.T) {
	for _, nodeType := range []DiscussionNodeType{
		DiscussionQuestion, DiscussionProposal, DiscussionAction,
		DiscussionFact, DiscussionDecision, DiscussionResult, DiscussionBlocker,
	} {
		n := validDiscussionNode(nodeType)
		n.FindingID = "finding-001"
		if err := n.Validate(); err == nil {
			t.Errorf("M09: FindingID on %q node must be rejected", nodeType)
		}
	}
}
