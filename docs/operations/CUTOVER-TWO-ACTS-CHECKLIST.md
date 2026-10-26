# Cutover two-acts checklist (OWNER NQ-6)

Status: checklist only. Do not perform these acts from AND-15.
Aligned to `docs/decisions/OWNER-DECISION-CUTOVER-NQ-2-3-4-6.yaml`.

Assignment-ledger operational cutover remains
`BLOCKED_ON_WRITE_AUTHORITY` until act 1 is separately authorized
(`docs/operations/ASSIGNMENT-LEDGER-CUTOVER.md`).

Const gates on `757d6a1822dfff62546ea504ceb638fde88df8e6`:

- `CoordinatorDelegationEnabled=false`
- `AuthenticatedAgentIngressEnabled=false`
- `orchestrationEnabled=false`

## Act 1 — assignment ledger + first OfferAssignment

Command boundary: Owner / coordinator typed command. Not CLI-as-owner.
Not `CanonicalStore.Update`. Not `store.New(runs.json)`.

- [ ] Independent review and proportional audit of the write path are complete.
- [ ] Backup of `state_dir` recorded (same policy as the v5→v6 runbook).
- [ ] `assignment_ledger` is still absent, or existing records are explicitly
      reissued; old journals are not imported.
- [ ] First `OfferAssignment` goes through the authenticated Owner/coordinator
      command boundary.
- [ ] Exact ACK, then explicit ACTIVATE, follow
      `docs/operations/ASSIGNMENT-LEDGER-CUTOVER.md`.
- [ ] Record first assignment ID and cutover time in the operations ledger.
- [ ] v1 RESULT path remains in production until act 2.

## Act 2 — authenticated ingress / v2 RESULT + bounded delegation

Both const flips ride existing typed writers. They are one act.

- [ ] Act 1 is recorded complete.
- [ ] Independent review and proportional audit of ingress and delegation
      enablement are complete.
- [ ] Flip `AuthenticatedAgentIngressEnabled` together with
      `CoordinatorDelegationEnabled` on the same authorized change.
- [ ] Production composition mounts authenticated agent RESULT v2.
- [ ] v1 RESULT is withdrawn from the production path as part of this act.
- [ ] Bounded delegation remains scoped to ADR-0010; cutover of delegation
      is still a separate recorded enablement, not implied by schema 6.
- [ ] Do not flip `orchestrationEnabled` as part of this act unless OWNER
      later authorizes it.

## Never in these acts

- Declaring S0Exit from this checklist.
- Mixing Runs/Outbox adapter writes with assignment/agent/owner aggregates
  (OWNER NQ-2).
- Treating schema 5→6 advance as either act (OWNER NQ-4).
