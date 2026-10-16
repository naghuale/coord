package task

import (
	"bytes"
	"coord/taskid"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

var (
	ErrDiscussionNodeActorRequired = errors.New("DISCUSSION_NODE_ACTOR_REQUIRED")
	ErrDiscussionNodeRoleRequired  = errors.New("DISCUSSION_NODE_ROLE_REQUIRED")
	ErrDiscussionEdgeActorRequired = errors.New("DISCUSSION_EDGE_ACTOR_REQUIRED")
)

type DiscussionNodeType string

const (
	DiscussionQuestion DiscussionNodeType = "QUESTION"
	DiscussionProposal DiscussionNodeType = "PROPOSAL"
	DiscussionFact     DiscussionNodeType = "FACT"
	DiscussionFinding  DiscussionNodeType = "FINDING"
	DiscussionDecision DiscussionNodeType = "DECISION"
	DiscussionAction   DiscussionNodeType = "ACTION"
	DiscussionResult   DiscussionNodeType = "RESULT"
	DiscussionBlocker  DiscussionNodeType = "BLOCKER"
)

type DiscussionEdgeType string

const (
	DiscussionAnswers  DiscussionEdgeType = "ANSWERS"
	DiscussionSupports DiscussionEdgeType = "SUPPORTS"
	DiscussionCausedBy DiscussionEdgeType = "CAUSED_BY"
	DiscussionResultOf DiscussionEdgeType = "RESULT_OF"
	DiscussionBlocks   DiscussionEdgeType = "BLOCKS"
	DiscussionRefersTo DiscussionEdgeType = "REFERS_TO"
)

// DiscussionNode is the typed, event-backed fact stored in a Task's graph.
// NodeID, TaskID, Actor, Role and CreatedAt are assigned by Coord.
type DiscussionNode struct {
	NodeID           string             `json:"node_id"`
	Type             DiscussionNodeType `json:"type"`
	TaskID           taskid.ID          `json:"task_id"`
	Actor            string             `json:"actor"`
	Role             string             `json:"role"`
	CreatedAt        time.Time          `json:"created_at"`
	Summary          string             `json:"summary"`
	Refs             []Reference        `json:"refs"`
	ReviewResultRef  string             `json:"review_result_ref,omitempty"`
	AuditResultRef   string             `json:"audit_result_ref,omitempty"`
	OwnerDecisionRef string             `json:"owner_decision_ref,omitempty"`
	ConflictID       string             `json:"conflict_id,omitempty"`
	ResultRef        string             `json:"result_ref,omitempty"`
	ReasonCode       string             `json:"reason_code,omitempty"`
	// FindingID is the stable Finding.ID this node is anchored to.
	// Allowed only for FINDING nodes; points to a specific finding within
	// the ReviewResult or AuditResult named by ReviewResultRef/AuditResultRef.
	FindingID string `json:"finding_id,omitempty"`
}

// DiscussionEdge is the typed relation between two nodes in one Task graph.
// EdgeID, TaskID, Actor and CreatedAt are assigned by Coord.
type DiscussionEdge struct {
	EdgeID     string             `json:"edge_id"`
	Type       DiscussionEdgeType `json:"type"`
	FromNodeID string             `json:"from_node_id"`
	ToNodeID   string             `json:"to_node_id"`
	TaskID     taskid.ID          `json:"task_id"`
	Actor      string             `json:"actor"`
	CreatedAt  time.Time          `json:"created_at"`
}

type DiscussionGraph struct {
	Nodes []DiscussionNode `json:"nodes"`
	Edges []DiscussionEdge `json:"edges"`
}

func (n *DiscussionNode) UnmarshalJSON(data []byte) error {
	type plain DiscussionNode
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value plain
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("invalid trailing DiscussionNode JSON")
	}
	*n = DiscussionNode(value)
	return nil
}

func (e *DiscussionEdge) UnmarshalJSON(data []byte) error {
	type plain DiscussionEdge
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value plain
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("invalid trailing DiscussionEdge JSON")
	}
	*e = DiscussionEdge(value)
	return nil
}

func (n DiscussionNode) Validate() error {
	if n.NodeID == "" || n.TaskID.Validate() != nil || n.CreatedAt.IsZero() || strings.TrimSpace(n.Summary) == "" || n.Refs == nil {
		return fmt.Errorf("discussion node requires identity, timestamp, summary and refs")
	}
	if strings.TrimSpace(n.Actor) == "" {
		return ErrDiscussionNodeActorRequired
	}
	if strings.TrimSpace(n.Role) == "" {
		return ErrDiscussionNodeRoleRequired
	}
	for _, ref := range n.Refs {
		if strings.TrimSpace(ref.Kind) != ref.Kind || strings.TrimSpace(ref.ID) == "" {
			return fmt.Errorf("discussion node reference kind and ID are required")
		}
	}
	fields := []string{n.ReviewResultRef, n.AuditResultRef, n.OwnerDecisionRef, n.ConflictID, n.ResultRef, n.ReasonCode, n.FindingID}
	switch n.Type {
	case DiscussionQuestion, DiscussionProposal, DiscussionAction:
		if anyDiscussionField(fields) {
			return fmt.Errorf("discussion node type %q has unsupported fields", n.Type)
		}
	case DiscussionFact:
		if len(n.Refs) == 0 {
			return fmt.Errorf("FACT requires refs")
		}
		if n.ConflictID != "" {
			if n.ReviewResultRef == "" || n.AuditResultRef == "" {
				return fmt.Errorf("conflict FACT requires ReviewResultRef and AuditResultRef")
			}
			if n.OwnerDecisionRef != "" || n.ResultRef != "" || n.ReasonCode != "" || n.FindingID != "" {
				return fmt.Errorf("FACT has unsupported fields")
			}
		} else if anyDiscussionField([]string{n.ReviewResultRef, n.AuditResultRef, n.OwnerDecisionRef, n.ResultRef, n.ReasonCode, n.FindingID}) {
			return fmt.Errorf("FACT has unsupported fields")
		}
	case DiscussionFinding:
		if len(n.Refs) == 0 {
			return fmt.Errorf("FINDING requires refs")
		}
		if (n.ReviewResultRef == "") == (n.AuditResultRef == "") {
			return fmt.Errorf("FINDING requires exactly one ReviewResultRef or AuditResultRef")
		}
		if n.FindingID != "" {
			if n.ReviewResultRef == "" && n.AuditResultRef == "" {
				return fmt.Errorf("FINDING with FindingID requires ReviewResultRef or AuditResultRef")
			}
		}
		if anyDiscussionField([]string{n.OwnerDecisionRef, n.ConflictID, n.ResultRef, n.ReasonCode}) {
			return fmt.Errorf("FINDING has unsupported fields")
		}
	case DiscussionDecision:
		if n.OwnerDecisionRef == "" && n.ConflictID == "" {
			return fmt.Errorf("DECISION requires OwnerDecisionRef or ConflictID")
		}
		if anyDiscussionField([]string{n.ReviewResultRef, n.AuditResultRef, n.ResultRef, n.ReasonCode, n.FindingID}) {
			return fmt.Errorf("DECISION has unsupported fields")
		}
	case DiscussionResult:
		if n.ResultRef == "" {
			return fmt.Errorf("RESULT requires ResultRef")
		}
		if anyDiscussionField([]string{n.ReviewResultRef, n.AuditResultRef, n.OwnerDecisionRef, n.ConflictID, n.ReasonCode, n.FindingID}) {
			return fmt.Errorf("RESULT has unsupported fields")
		}
	case DiscussionBlocker:
		if strings.TrimSpace(n.ReasonCode) == "" {
			return fmt.Errorf("BLOCKER requires ReasonCode")
		}
		if anyDiscussionField([]string{n.ReviewResultRef, n.AuditResultRef, n.OwnerDecisionRef, n.ConflictID, n.ResultRef, n.FindingID}) {
			return fmt.Errorf("BLOCKER has unsupported fields")
		}
	default:
		return fmt.Errorf("unsupported discussion node type %q", n.Type)
	}
	return nil
}

func (e DiscussionEdge) Validate() error {
	if e.EdgeID == "" || e.TaskID.Validate() != nil || e.FromNodeID == "" || e.ToNodeID == "" || e.CreatedAt.IsZero() {
		return fmt.Errorf("discussion edge requires identity, endpoints and timestamp")
	}
	if strings.TrimSpace(e.Actor) == "" {
		return ErrDiscussionEdgeActorRequired
	}
	switch e.Type {
	case DiscussionAnswers, DiscussionSupports, DiscussionCausedBy, DiscussionResultOf, DiscussionBlocks, DiscussionRefersTo:
		return nil
	default:
		return fmt.Errorf("unsupported discussion edge type %q", e.Type)
	}
}

func anyDiscussionField(values []string) bool {
	for _, value := range values {
		if value != "" {
			return true
		}
	}
	return false
}

// ProjectDiscussion rebuilds the graph in canonical event order.
func ProjectDiscussion(events []DomainEvent, id taskid.ID) DiscussionGraph {
	graph := DiscussionGraph{Nodes: []DiscussionNode{}, Edges: []DiscussionEdge{}}
	for _, event := range events {
		if event.TaskID != id {
			continue
		}
		if event.DiscussionNode != nil {
			node := *event.DiscussionNode
			node.Refs = append([]Reference{}, node.Refs...)
			graph.Nodes = append(graph.Nodes, node)
		}
		if event.DiscussionEdge != nil {
			graph.Edges = append(graph.Edges, *event.DiscussionEdge)
		}
	}
	return graph
}
