package orchestration

import (
	"coord/internal/externalaction"
	"coord/store"
	"fmt"
)

// ValidateCompletion applies to a workflow turn, not daemon sleep/shutdown.
// A durable blocker permits waiting for a defined external trigger. An action
// still awaiting delivery/admission/observation does not constitute completion.
func ValidateCompletion(s store.CanonicalState, futurePromise string, independentAvailable, independentPerformed bool) error {
	if futurePromise != "" {
		return fmt.Errorf("PASSIVE_FUTURE_PROMISE")
	}
	if independentAvailable && !independentPerformed {
		return fmt.Errorf("INDEPENDENT_WORK_NOT_PERFORMED")
	}
	for _, r := range s.Orchestration.Receipts {
		if r.AckPending && r.AckBlocker == "" {
			return fmt.Errorf("ACKNOWLEDGEMENT_PENDING")
		}
	}
	for _, a := range s.Orchestration.Actions {
		if !a.Terminal() {
			return fmt.Errorf("WORKFLOW_PENDING: %s %s", a.ID, a.Status)
		}
		if a.Status == externalaction.Blocked && a.Blocker == "" {
			return fmt.Errorf("BLOCKER_NOT_DURABLE")
		}
	}
	return nil
}
func (r *Runtime) Completion() error {
	s, err := r.Store.CanonicalSnapshot()
	if err != nil {
		return err
	}
	return ValidateCompletion(s, "", false, false)
}

// OwnsRun prevents the legacy Run mutation path from bypassing orchestration's
// canonical fences. No raw transport result/admission is accepted for these Runs.
func (r *Runtime) OwnsRun(id string) bool {
	s, err := r.Store.CanonicalSnapshot()
	if err != nil {
		return true
	} // fail closed
	for _, a := range s.Orchestration.Actions {
		if a.Binding.RunID == id {
			return true
		}
	}
	return false
}
