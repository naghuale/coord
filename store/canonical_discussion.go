package store

import (
	"coord/envelope"
	"coord/fsm"
	"coord/task"
	"coord/taskid"
	"fmt"
	"time"
)

// RecordDiscussionNode writes a typed node through the authenticated agent
// command boundary. Identity and author fields are always assigned by Coord.
func (s *CanonicalStore) RecordDiscussionNode(frame AgentCommandFrame, id taskid.ID, expectedRevision uint64, node task.DiscussionNode) (InboxReceipt, bool, error) {
	if frame == nil {
		return InboxReceipt{}, false, ErrAgentCommandMalformed
	}
	if node.NodeID != "" || node.TaskID != "" || node.Actor != "" || node.Role != "" || !node.CreatedAt.IsZero() {
		return InboxReceipt{}, false, fmt.Errorf("discussion node identity and author metadata are Coord-generated")
	}
	node.Refs = append([]task.Reference{}, node.Refs...)
	now := s.now()
	fixedFrame := snapshotDiscussionCommandFrame{AgentCommandFrame: frame, command: frame.Envelope()}
	messageID := fixedFrame.command.MessageID
	return s.ApplyAgentCommand(fixedFrame, id, expectedRevision, func(state *CanonicalState, run fsm.Run) (InboxReceipt, error) {
		current := state.Tasks[id]
		if now.Before(current.UpdatedAt) {
			return InboxReceipt{}, fmt.Errorf("discussion timestamp precedes Task state")
		}
		seq := current.LastEventSeq + 1
		node.NodeID = fmt.Sprintf("%s/node", task.EventID(id, seq))
		node.TaskID, node.Actor, node.Role, node.CreatedAt = id, run.RoleSelection.AgentID, string(run.RoleSelection.Profile.Role), now
		if err := node.Validate(); err != nil {
			return InboxReceipt{}, err
		}
		if err := validateDiscussionNodeReferences(*state, node, seq); err != nil {
			return InboxReceipt{}, err
		}
		current.Revision++
		current.LastEventSeq = seq
		current.UpdatedAt = now
		state.Tasks[id] = current
		event := task.DomainEvent{ID: task.EventID(id, seq), TaskID: id, RunID: run.RunID, EventSeq: seq, TaskRevision: current.Revision, TaskFence: current.Fence, Type: task.DiscussionNodeRecorded, ActorID: node.Actor, Summary: node.Summary, CreatedAt: now, DiscussionNode: &node}
		state.Events = append(state.Events, event)
		ack, err := discussionCommandAck(run, messageID, current.Revision, now)
		if err != nil {
			return InboxReceipt{}, err
		}
		state.Outbox[run.RunID] = appendOutbox(state.Outbox[run.RunID], OutboxMessage{Envelope: ack, EventID: event.ID})
		return discussionReceipt(run, messageID, id, expectedRevision, current.Revision, event.ID, now), nil
	})
}

// RecordDiscussionEdge writes a typed edge through the same command boundary.
func (s *CanonicalStore) RecordDiscussionEdge(frame AgentCommandFrame, id taskid.ID, expectedRevision uint64, edge task.DiscussionEdge) (InboxReceipt, bool, error) {
	if frame == nil {
		return InboxReceipt{}, false, ErrAgentCommandMalformed
	}
	if edge.EdgeID != "" || edge.TaskID != "" || edge.Actor != "" || !edge.CreatedAt.IsZero() {
		return InboxReceipt{}, false, fmt.Errorf("discussion edge identity and author metadata are Coord-generated")
	}
	now := s.now()
	fixedFrame := snapshotDiscussionCommandFrame{AgentCommandFrame: frame, command: frame.Envelope()}
	messageID := fixedFrame.command.MessageID
	return s.ApplyAgentCommand(fixedFrame, id, expectedRevision, func(state *CanonicalState, run fsm.Run) (InboxReceipt, error) {
		current := state.Tasks[id]
		if now.Before(current.UpdatedAt) {
			return InboxReceipt{}, fmt.Errorf("discussion timestamp precedes Task state")
		}
		seq := current.LastEventSeq + 1
		edge.EdgeID = fmt.Sprintf("%s/edge", task.EventID(id, seq))
		edge.TaskID, edge.Actor, edge.CreatedAt = id, run.RoleSelection.AgentID, now
		if err := edge.Validate(); err != nil {
			return InboxReceipt{}, err
		}
		if err := validateDiscussionEdgeReferences(*state, edge, seq); err != nil {
			return InboxReceipt{}, err
		}
		current.Revision++
		current.LastEventSeq = seq
		current.UpdatedAt = now
		state.Tasks[id] = current
		event := task.DomainEvent{ID: task.EventID(id, seq), TaskID: id, RunID: run.RunID, EventSeq: seq, TaskRevision: current.Revision, TaskFence: current.Fence, Type: task.DiscussionEdgeRecorded, ActorID: edge.Actor, CreatedAt: now, DiscussionEdge: &edge}
		state.Events = append(state.Events, event)
		ack, err := discussionCommandAck(run, messageID, current.Revision, now)
		if err != nil {
			return InboxReceipt{}, err
		}
		state.Outbox[run.RunID] = appendOutbox(state.Outbox[run.RunID], OutboxMessage{Envelope: ack, EventID: event.ID})
		return discussionReceipt(run, messageID, id, expectedRevision, current.Revision, event.ID, now), nil
	})
}

type snapshotDiscussionCommandFrame struct {
	AgentCommandFrame
	command envelope.Envelope
}

func (f snapshotDiscussionCommandFrame) Envelope() envelope.Envelope { return f.command }

func discussionReceipt(run fsm.Run, messageID string, id taskid.ID, expected, committed uint64, eventID string, now time.Time) InboxReceipt {
	return InboxReceipt{ActorID: run.RoleSelection.AgentID, MessageID: messageID, TaskID: id, RunID: run.RunID, EventIDs: []string{eventID}, ExpectedRevision: expected, CommittedRevision: committed, RecordedAt: now}
}

func discussionCommandAck(run fsm.Run, messageID string, revision uint64, now time.Time) (envelope.Envelope, error) {
	if run.CoordinatorSession == "" || run.RoleSelection == nil || run.RoleSelection.Session == "" || run.TargetSHA == "" {
		return envelope.Envelope{}, fmt.Errorf("run coordinator, role session and target SHA are required for Discussion acknowledgement")
	}
	ack := envelope.Envelope{SchemaVersion: 1, MessageID: envelope.NewID(), InReplyTo: messageID, Timestamp: now, From: run.CoordinatorSession, To: run.RoleSelection.Session, Kind: envelope.RoleAck, Task: run.TaskID.String(), Run: run.RunID, Attempt: run.Attempt, TargetSHA: run.TargetSHA, Fence: run.Fence, RoleAckPayload: &envelope.RoleAckPayload{State: "ACCEPTED", Revision: revision}}
	return ack, ack.Validate()
}

func validateCanonicalDiscussion(state CanonicalState) error {
	seenNodes := make(map[string]task.DiscussionNode)
	seenEdges := make(map[string]bool)
	for _, event := range state.Events {
		switch event.Type {
		case task.DiscussionNodeRecorded:
			node := *event.DiscussionNode
			if node.NodeID != event.ID+"/node" {
				return fmt.Errorf("discussion node ID is not Coord-generated")
			}
			run, ok := state.Runs[event.RunID]
			if !ok || run.TaskID != event.TaskID || run.RoleSelection == nil || run.RoleSelection.AgentID != node.Actor || string(run.RoleSelection.Profile.Role) != node.Role || event.ActorID != node.Actor {
				return fmt.Errorf("discussion node author does not match canonical Run")
			}
			if _, exists := seenNodes[node.NodeID]; exists {
				return fmt.Errorf("duplicate Discussion node ID %q", node.NodeID)
			}
			if err := validateDiscussionNodeReferences(state, node, event.EventSeq); err != nil {
				return err
			}
			seenNodes[node.NodeID] = node
		case task.DiscussionEdgeRecorded:
			edge := *event.DiscussionEdge
			if edge.EdgeID != event.ID+"/edge" {
				return fmt.Errorf("discussion edge ID is not Coord-generated")
			}
			run, ok := state.Runs[event.RunID]
			if !ok || run.TaskID != event.TaskID || run.RoleSelection == nil || run.RoleSelection.AgentID != edge.Actor || event.ActorID != edge.Actor {
				return fmt.Errorf("discussion edge author does not match canonical Run")
			}
			if seenEdges[edge.EdgeID] {
				return fmt.Errorf("duplicate Discussion edge ID %q", edge.EdgeID)
			}
			if err := validateDiscussionEdgeReferences(state, edge, event.EventSeq); err != nil {
				return err
			}
			seenEdges[edge.EdgeID] = true
		}
	}
	return nil
}

func validateDiscussionNodeReferences(state CanonicalState, node task.DiscussionNode, currentSeq uint64) error {
	if node.OwnerDecisionRef != "" {
		return fmt.Errorf("owner decisions are not present in CanonicalState")
	}
	if node.ConflictID != "" {
		return fmt.Errorf("conflict Events are not present in CanonicalState")
	}
	if node.ResultRef != "" {
		return fmt.Errorf("canonical Executor Result is not available before #6")
	}
	for _, field := range []struct {
		name  string
		value string
		want  task.EventType
	}{{"ReviewResultRef", node.ReviewResultRef, task.ReviewResultRecorded}, {"AuditResultRef", node.AuditResultRef, task.AuditResultRecorded}} {
		if field.value == "" {
			continue
		}
		found := false
		for _, event := range state.Events {
			if event.ID == field.value && event.TaskID == node.TaskID && event.EventSeq < currentSeq && event.Type == field.want {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s must identify an existing same-Task canonical result event", field.name)
		}
	}
	for _, ref := range node.Refs {
		switch ref.Kind {
		case task.ReferenceTask:
			if ref.ID != node.TaskID.String() {
				return fmt.Errorf("discussion reference points outside Task")
			}
		case task.ReferenceRun:
			run, ok := state.Runs[ref.ID]
			if !ok || run.TaskID != node.TaskID {
				return fmt.Errorf("discussion reference points to missing or unrelated Run %q", ref.ID)
			}
		case task.ReferenceDomainEvent:
			found := false
			for _, event := range state.Events {
				if event.ID == ref.ID && event.TaskID == node.TaskID && event.EventSeq < currentSeq {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("discussion reference points to missing, future or unrelated DomainEvent %q", ref.ID)
			}
		case task.ReferenceArtifact:
			return fmt.Errorf("artifact metadata not defined")
		default:
			return fmt.Errorf("unsupported Discussion reference kind %q", ref.Kind)
		}
	}
	return nil
}

func validateDiscussionEdgeReferences(state CanonicalState, edge task.DiscussionEdge, currentSeq uint64) error {
	var from, to *task.DiscussionNode
	for _, event := range state.Events {
		if event.Type != task.DiscussionNodeRecorded || event.TaskID != edge.TaskID || event.EventSeq >= currentSeq {
			continue
		}
		node := event.DiscussionNode
		if node.NodeID == edge.FromNodeID {
			from = node
		}
		if node.NodeID == edge.ToNodeID {
			to = node
		}
	}
	if from == nil || to == nil {
		return fmt.Errorf("discussion edge references missing or cross-Task node")
	}
	if !allowedDiscussionEdgePair(edge.Type, from.Type, to.Type) {
		return fmt.Errorf("discussion edge type %q does not allow %s → %s", edge.Type, from.Type, to.Type)
	}
	return nil
}

func allowedDiscussionEdgePair(edge task.DiscussionEdgeType, from, to task.DiscussionNodeType) bool {
	switch edge {
	case task.DiscussionAnswers:
		return (from == task.DiscussionFact || from == task.DiscussionProposal || from == task.DiscussionDecision) && to == task.DiscussionQuestion
	case task.DiscussionSupports:
		return (from == task.DiscussionFact || from == task.DiscussionFinding || from == task.DiscussionResult) && (to == task.DiscussionProposal || to == task.DiscussionDecision || to == task.DiscussionFinding)
	case task.DiscussionCausedBy:
		return (from == task.DiscussionDecision && to == task.DiscussionFinding) || (from == task.DiscussionAction && to == task.DiscussionDecision) || (from == task.DiscussionBlocker && (to == task.DiscussionFact || to == task.DiscussionFinding))
	case task.DiscussionResultOf:
		return from == task.DiscussionResult && to == task.DiscussionAction
	case task.DiscussionBlocks:
		return from == task.DiscussionBlocker && (to == task.DiscussionAction || to == task.DiscussionDecision || to == task.DiscussionProposal)
	case task.DiscussionRefersTo:
		return true
	default:
		return false
	}
}

func (s *CanonicalStore) DiscussionGraph(id taskid.ID) (task.DiscussionGraph, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.state.Tasks[id]; !ok {
		return task.DiscussionGraph{}, fmt.Errorf("canonical Task %q not found", id)
	}
	return task.ProjectDiscussion(s.state.Events, id), nil
}
