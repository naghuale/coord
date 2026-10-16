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
			if other.AgentID != selection.AgentID {
				continue
			}
			switch {
			case selection.Profile.Role == roles.Auditor && other.Profile.Role == roles.Executor:
				return fmt.Errorf("auditor conflicts with task executor")
			case selection.Profile.Role == roles.Executor && other.Profile.Role == roles.Auditor:
				return fmt.Errorf("auditor conflicts with task executor")
			case selection.Profile.Role == roles.Reviewer && other.Profile.Role == roles.Executor:
				return fmt.Errorf("reviewer conflicts with task executor")
			case selection.Profile.Role == roles.Executor && other.Profile.Role == roles.Reviewer:
				return fmt.Errorf("reviewer conflicts with task executor")
			case selection.Profile.Role == roles.Auditor && other.Profile.Role == roles.Reviewer:
				return fmt.Errorf("auditor conflicts with task reviewer")
			case selection.Profile.Role == roles.Reviewer && other.Profile.Role == roles.Auditor:
				return fmt.Errorf("auditor conflicts with task reviewer")
			}
		}
	}
	r.RoleHistory = append(r.RoleHistory, selection)
	r.RoleSelection = &selection
	return nil
}
