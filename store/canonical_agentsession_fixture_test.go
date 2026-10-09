package store

import (
	"coord/agentsession"
	"coord/fsm"
	"coord/roles"
	"fmt"
	"time"
)

func ensureCanonicalTestAgentSession(state *CanonicalState, address, agentID string, at time.Time) {
	if current, ok := state.AgentSessions[address]; ok {
		if current.AgentID != agentID {
			current.AgentID = agentID
			state.AgentSessions[address] = current
			for i := range state.AgentSessionEvents {
				if state.AgentSessionEvents[i].Address == address {
					state.AgentSessionEvents[i].AgentID = agentID
				}
			}
		}
		return
	}
	key := sessionTestKey(1)
	state.AgentSessions[address] = agentsession.Session{
		Address: address, AgentID: agentID, Generation: 1, PublicKey: key,
		RegisteredAt: at, UpdatedAt: at,
	}
	state.AgentSessionEvents = append(state.AgentSessionEvents, agentsession.Event{
		ID: agentsession.EventID(address, 1), Address: address, AgentID: agentID,
		Sequence: 1, Type: agentsession.Registered, Generation: 1, PublicKey: key, CreatedAt: at,
	})
}

func bindCanonicalTestRun(state *CanonicalState, run *fsm.Run, agentID string, at time.Time) {
	address := fmt.Sprintf("test-session:%s:%s", run.RunID, agentID)
	run.OwnerSession = address
	run.AgentSessionGeneration = 1
	selection := roles.Selection{AgentID: agentID, Session: address, Profile: roles.Profile{Role: run.RequiredRole.Role}}
	if run.RoleSelection != nil {
		selection = *run.RoleSelection
		selection.AgentID = agentID
		selection.Session = address
	}
	run.RoleSelection = &selection
	if len(run.RoleHistory) == 0 {
		run.RoleHistory = []roles.Selection{selection}
	} else {
		run.RoleHistory[len(run.RoleHistory)-1] = selection
	}
	ensureCanonicalTestAgentSession(state, address, agentID, at)
}
