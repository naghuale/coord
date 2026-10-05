package coord

import (
	"coord/fsm"
	"coord/roles"
	"fmt"
)

func (c *Coordinator) selectRole(r *fsm.Run) error {
	if r.RequiredRole.Role == "" {
		if c.RoleRegistry != nil {
			return fmt.Errorf("required role must be explicit")
		}
		r.RoleSelection = nil
		return nil
	}
	if c.RoleRegistry == nil {
		return fmt.Errorf("role registry required")
	}
	selection, err := c.RoleRegistry.Select(r.OwnerSession, r.RequiredRole)
	if err != nil {
		return err
	}
	for _, prior := range c.store.All() {
		if prior.Task != r.Task {
			continue
		}
		history := append([]roles.Selection{}, prior.RoleHistory...)
		if prior.RoleSelection != nil {
			history = append(history, *prior.RoleSelection)
		}
		for _, other := range history {
			if other.AgentID == selection.AgentID && ((selection.Profile.Role == roles.Auditor && other.Profile.Role == roles.Executor) || (selection.Profile.Role == roles.Executor && other.Profile.Role == roles.Auditor)) {
				return fmt.Errorf("auditor conflicts with task executor")
			}
		}
	}
	r.RoleHistory = append(r.RoleHistory, selection)
	r.RoleSelection = &selection
	return nil
}
