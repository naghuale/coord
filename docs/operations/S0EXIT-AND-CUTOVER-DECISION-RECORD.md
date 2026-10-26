# S0Exit and cutover decision record

Append-only. This file records OWNER decisions. It does not declare S0Exit.
It does not flip const gates. It does not perform cutover acts.

Aligned to `docs/decisions/OWNER-DECISION-CUTOVER-NQ-2-3-4-6.yaml`.
Subject SHA at decision: `757d6a1822dfff62546ea504ceb638fde88df8e6`.

## 2026-10-26 — OWNER cutover NQ-2/3/4/6

Recorded by AND-15 (`and15-tech-followups-grok-a89`). Source answers:
`workspace-01/.scratch/tmp/stabilization/OWNER-DECISION-CUTOVER-NQ-2-3-4-6.yaml`
(sha256 `ad85424b5154c008ee19ac6f59e96fa3c7b4e6c286555d03bf995428e2e65663`).

| NQ | Choice | Record |
|---|---|---|
| NQ-2 | `FREEZE_RUN_OUTBOX_ADAPTER_BY_ADR` | `CanonicalStore.Update` stays only for Runs/Outbox (Handle/Tick/supervisor). No new fields via this seam. Assignment/agent/owner writes stay on typed commands. Two seams are a documented duality. Requires a new ADR (AND-13: Proposed then OWNER Accepted) and tests that Update cannot touch OwnerCredentials, AssignmentLedger, CoordinatorDelegations, or new fields. |
| NQ-3 | `FAIL_CLOSED_LEGACY_STORE_IN_PRODUCTION_BUILDS` | `store.New(runs.json)` errors in production builds (telepathyhost, localchat). Tests/demo/integration keep a harness. Requires later implementation (AND-14), gate, review, and an AST/test guard. Not implemented by AND-15. |
| NQ-4 | `SCHEMA_ADVANCE_IS_NOT_CUTOVER_O1_SEPARATE` | A v5 canonical opened by a v6 binary advances to 6 under the existing decoder after backup. Schema advance is not cutover. O-1 accepted trust boundary: reload trusts persisted canonical integrity and single writer coordd. Cryptographic re-verify on load is post-S0 (Operations Ledger ADR, AND-10). |
| NQ-6 | `TWO_ACTS` | Act 1: assignment ledger + first OfferAssignment on the Owner/coordinator command boundary. Act 2: authenticated ingress/v2 RESULT together with bounded delegation const flips behind existing typed writers. v1 RESULT remains until act 2. |

### Explicitly not authorized

- flipping any const gate (`CoordinatorDelegationEnabled`, `AuthenticatedAgentIngressEnabled`, `orchestrationEnabled`)
- performing cutover acts
- declaring S0Exit

Assignment-ledger operational cutover remains `BLOCKED_ON_WRITE_AUTHORITY`
(`docs/operations/ASSIGNMENT-LEDGER-CUTOVER.md`).

### Technical follow-ups recorded with the same answers

- NQ-1: ApplyInboxCommand comment/docs aligned with code (AND-15). Enroll-boundary test remains a later follow-up; AND-15 does not change code behaviour.
- NQ-7: `COMPONENT-STATUS.yaml` `updatedFromCommit` set to `757d6a1`.
- ADR-0010 header: implementation IMPLEMENTED, production DISABLED (const), cutover NOT_PERFORMED.
- `integration/telepathy/checks.go:87` is integration-only (covered by the NQ-3 harness decision).
