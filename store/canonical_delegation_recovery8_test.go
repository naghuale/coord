package store

import (
	"bytes"
	"coord/ownercredential"
	"errors"
	"strings"
	"testing"
)

const recovery8PreRestartVisibilityOrigin = "pre-restart loss must reuse restart visibility"

func recovery8AssertSeam(t *testing.T, before, next CanonicalState, grantID, want string) {
	t.Helper()
	bytesBefore, bytesNext := mustMarshalRecovery6(t, before), mustMarshalRecovery6(t, next)
	err := validateHandoffGrantDelta(before, next, before.CoordinatorDelegations[grantID], next.CoordinatorDelegations[grantID])
	if want == "" {
		if err != nil {
			t.Fatalf("seam positive control: %v", err)
		}
	} else if !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), want) {
		t.Fatalf("seam error = %v, want delegation rejection containing %q", err, want)
	}
	if !bytes.Equal(bytesBefore, mustMarshalRecovery6(t, before)) || !bytes.Equal(bytesNext, mustMarshalRecovery6(t, next)) {
		t.Fatal("seam validation changed its inputs")
	}
}

func TestRecovery8_P1PreRestartLossMustReuseRestartVisibility(t *testing.T) {
	t.Run("preRestartLossWithRestartVisibility", func(t *testing.T) {
		id := "del-r8-p1-restart-vis"
		f, now, a, a2, _ := recovery7Fixture(t, id, false)
		before := recovery6Clone(t, f.s.state)
		next := recovery6Clone(t, before)
		forgeRecoverySiblingWithoutReconsuming(t, &next, id, "asg-r4-x", a, a2, "vis-r5-primary-restart", now)
		recovery8AssertSeam(t, before, next, id, "")
		recovery6Reload(t, next, "")
		if err := recovery4Handoff(t, f, a, a2, "asg-r4-x", "p1-live-restart-visibility"); err != nil {
			t.Fatalf("live A-2 positive control: %v", err)
		}
		if f.s.state.CoordinatorDelegations[id].History[len(f.s.state.CoordinatorDelegations[id].History)-1].ReceiptDig != "vis-r5-primary-restart" {
			t.Fatal("live A-2 handoff did not retain the restart visibility")
		}
		recovery6Reload(t, recovery6Clone(t, f.s.state), "")
	})

	t.Run("postRestartLossWithFreshVisibility", func(t *testing.T) {
		id := "del-r8-p1-a3-fresh"
		f, now, _, a2, b := recovery7Fixture(t, id, true)
		before := recovery6Clone(t, f.s.state)
		next := recovery6Clone(t, before)
		forgeRecoveryHandoff(t, &next, id, "asg-r7-x", b, a2, "vis-r7-fresh", now)
		recovery8AssertSeam(t, before, next, id, "")
		recovery6Reload(t, next, "")
		if err := recovery4Handoff(t, f, b, a2, "asg-r7-x", "p1-live-a3-fresh"); err != nil {
			t.Fatalf("live A-3 positive control: %v", err)
		}
		if f.s.state.CoordinatorDelegations[id].History[len(f.s.state.CoordinatorDelegations[id].History)-1].ReceiptDig != "vis-r7-fresh" {
			t.Fatal("live A-3 capture did not consume the fresh visibility")
		}
		recovery6Reload(t, recovery6Clone(t, f.s.state), "")
	})

	t.Run("preRestartLossWithFreshUnrelatedVisibility", func(t *testing.T) {
		id := "del-r8-p1-fresh-unrelated"
		f, now, a, a2, _ := recovery7Fixture(t, id, false)
		confirmVisibility(t, f, id, "vis-r8-unrelated", ownercredential.DelegationPrincipalPayload{
			StableAgentID: a2.AgentID, SessionID: a2.SessionID, Generation: a2.Generation,
		}, now)
		before := recovery6Clone(t, f.s.state)
		bad := recovery6Clone(t, before)
		forgeRecoveryHandoff(t, &bad, id, "asg-r4-x", a, a2, "vis-r8-unrelated", now)
		recovery8AssertSeam(t, before, bad, id, recovery8PreRestartVisibilityOrigin)
		recovery6Reload(t, bad, recovery8PreRestartVisibilityOrigin)
		d := bad.CoordinatorDelegations[id]
		h := d.History[len(d.History)-1]
		if h.Kind != DelegationHistoryHandoff || h.ReceiptDig == "vis-r5-primary-restart" {
			t.Fatal("negative fixture did not send the pre-restart loss with a non-restart visibility")
		}
	})
}
