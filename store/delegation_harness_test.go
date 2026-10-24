package store

import "testing"

// enableDelegationHarness flips the test-only harness. Production const stays
// false; enablement never reads env/flag/config.
func enableDelegationHarness(t *testing.T) {
	t.Helper()
	if CoordinatorDelegationEnabled {
		t.Fatal("production CoordinatorDelegationEnabled must remain false")
	}
	prev := delegationHarnessEnabled
	delegationHarnessEnabled = true
	t.Cleanup(func() { delegationHarnessEnabled = prev })
}
