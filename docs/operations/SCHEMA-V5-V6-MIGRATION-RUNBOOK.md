# Schema v5 → v6 migration runbook

Status: documented. Do not run this procedure from AND-15.
This runbook is not cutover. OWNER NQ-4: schema advance is not cutover.

Subject decoder SHA: `757d6a1822dfff62546ea504ceb638fde88df8e6`.
`CanonicalSchemaVersion` is `6` (`store/canonical.go`).

## What the decoder already does

On `757d6a1`, `NewCanonical` loads schema 3, 4, 5, or 6 (`ValidateCanonicalState`).
The first mutating commit writes `next.SchemaVersion = CanonicalSchemaVersion`
(6) through an atomic durable Save (`atomicfile.Write`); the code does not
make a backup — the backup is the operator's precondition 4. Schema 7+ fails
closed. Schema 5 cannot contain `CoordinatorDelegations`. No downgrade from a
loaded v6 snapshot.

Tests that name this rule include
`TestSchemaFiveLoadsReadOnlyAndNextCommitPublishesSix` and
`TestSchemaFourLoadsReadOnlyAndNextCommitPublishesSix`.

## O-1 trust boundary (OWNER accepted)

Reload trusts persisted canonical integrity and the single writer `coordd`.
Load path: `NewCanonical` → `CanonicalFile.Load` → `NormalizeCanonicalState` →
`ValidateCanonicalState` (structural) plus aggregate `Validate`.
Load does not call `VerifyAssertion` on Owner receipts.
Cryptographic re-verification on load is a separate post-S0 item
(Operations Ledger ADR, AND-10).

## Preconditions (do not skip)

1. OWNER or operator command to run this runbook. AND-15 is not that command.
2. Single writer: stop extra `coordd` / host processes that hold
   `canonical.json.lock`.
3. Record current binary SHA, `state_dir`, and `canonical.json` path.
4. Take an independently verified, protected backup of the whole state
   directory after writers are closed. Follow `docs/OPERATIONS.md`
   «Snapshot, backup и restart»: stop host and writers, wait for Store close,
   copy the state directory. Do not print credentials.
5. Confirm the backup is readable independently of the live path.

## Procedure (not executed by this assignment)

1. Keep the same `state_dir` and the same `canonical.json`.
2. Start the v6 `coordd` against that path.
3. A v5 snapshot remains schema 5 until the first successful mutating commit.
   That commit publishes schema 6.
4. Read-only operations must not be treated as cutover, assignment offer, or
   delegation enablement.
5. Record the first published schema-6 revision in the operations ledger.

## Explicitly out of scope

- Flipping `CoordinatorDelegationEnabled`, `AuthenticatedAgentIngressEnabled`,
  or `orchestrationEnabled`.
- First `OfferAssignment` (cutover act 1).
- Authenticated ingress / v2 RESULT (cutover act 2).
- Declaring S0Exit.
- `store.New(runs.json)` fail-closed (AND-14 / NQ-3), not this runbook.

## Rollback

Do not start a second writer. Restore from the verified backup onto the same
`state_dir` only after confirming the live writer is stopped. Do not mix a
restored v5 snapshot with an in-flight v6 writer.
