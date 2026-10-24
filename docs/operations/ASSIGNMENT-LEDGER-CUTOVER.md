# Assignment Ledger MVP cutover and rollback (#90)

Profile: canonical coordd, existing JSON snapshot; Telepathy only.
Status: implementation runbook under OWNER #90 plan; execute after exact-SHA
Review, disclosed-conflict Audit, full checks and merge, not during implementation.

## Current cutover gate: BLOCKED_ON_WRITE_AUTHORITY

The core service API and read-only status do not yet supply an authenticated
coordinator-usable production write path for the first offer. Existing
RegisterAgentSession delegates authorization to a command boundary; it is not an
implemented canonical operator credential. ADR-0004 RT-04 prohibits direct CLI
writes. Legacy localchat owner.key and the supervisor/recovery HTTP token are
not promoted to assignment admission. OWNER chose option (a): #90 has no production write CLI/endpoint. Follow-up
Issue #93 implements COMMAND-BOUNDARY §3.2 with separate review/audit. Do not perform steps
3–5 until follow-up #93 implements and independently reviews/audits the authorized write boundary. No guessed
operator principal, path flag or OS-user claim substitutes for authorization.

## Cutover

1. Record merge SHA, role results, existing coordd state_dir and canonical.json path
   in the Completion Report / Operations Ledger. Take an independently verified,
   protected backup under existing operational policy. Do not print credentials.
2. Close or explicitly reissue open process assignments at the cutover boundary.
   Old manual journals remain immutable history, never imported or executable.
   No guessed Task/Run/model/generation/TTL backfill. Review/Audit output from an
   expired/VOID assignment is input evidence only with original provenance.
3. Run the ledger-aware binary against the SAME configured canonical store. The
   ledger starts only with the first assignment explicitly created after merge,
   through the authenticated coordinator service command, not transport receipt.
   Until that first offer `assignment_ledger` is absent; historical canonical
   Task/Run data remains readable. Once created, the ledger cannot be removed.
4. Register/verify exact AgentSession bindings using their existing operator
   approval boundary. Create a new OFFER with subject/role, exact session/stable
   identity/generations, immutable clock, admission/visibility evidence. Do not
   invent a Task for a standalone commit audit/analysis assignment.
5. Verify exact ACK, then explicitly ACTIVATE. Agent reads `coord --state <same
   coordd-state-dir> assignment status <id>` and compares immutable
   issuer/subject/target/current coordinator/generation before work/resume/result. Unknown/unavailable/corrupt
   state grants no permission. CLI read does not start a daemon or bypass locks.
6. An active observer records verified coordinator loss promptly; its principal is
   separate from the lost-coordinator subject. Normal handoff requires a current
   active predecessor. Recovery handoff requires already recorded loss, an active
   successor and verified nonempty evidence; the successor is the event actor.
   The authenticated command boundary must authorize observation/recovery (#93).
   Loss blocks work/results until verified handoff or a fresh assignment. Assignee host restart is VOID/new ID,
   not ALIVE or old ACK revival. Existing R154 incident clocks are not reset.
7. Record actual cutover time and first new assignment ID in Completion Report;
   roadmap tracks implementation versus operational cutover separately. No live
   cutover was performed by the implementation tests.

## Launch rollback: LEDGER_FROZEN (OWNER option b)

Use the SAME #90 ledger-aware binary and SAME state_dir. Stop/restart through the
ordinary authorized supervisor path; do not start a second writer or force-remove
locks. In the existing coordd configuration set:

```json
{"assignment_ledger_mode":"LEDGER_FROZEN"}
```

This is an additional field of the complete config, not a replacement config.
Keep existing transport/token/key/path settings unchanged and private. Start
`coordd -config <config>` through the existing launch procedure. Frozen mode is
applied before runtime processing. Existing assignment queries, validation,
terminal-state enforcement and handoffs remain operational. Two cases must be
separated:

- Assignments already ACTIVE can continue with WORK_STARTED, result processing
  and terminal transitions, subject to the existing authority/TTL checks.
- Existing OFFERED/ACKED assignments can still reach ACTIVE in the current code:
  OFFERED first requires ACK, and ACKED can be explicitly ACTIVATED. They may then
  record WORK_STARTED. This is a description of current behaviour, not a policy
  approval. OWNER's position is "preserve existing work, forbid new work";
  whether this second case fits it is pending with OWNER. No frozen-mode code
  change is made by the coordinator-authority correction.

All existing state, issuer, session, generation, TTL and coordinator checks
remain in force as applicable. Non-ledger service functions remain available.
New OFFER/new IDs, ledger schema migration and new workflow-feature activation
are forbidden. No checks are disabled; records and history stay in
canonical.json. A deliberate later restart removing this mode returns to normal
operation only after the original blocking condition is resolved.

Test obligation LEDGER-ROLLBACK-01 is implemented by
`TestAssignmentLedgerRollbackFrozenMode`: ledger present -> restart frozen ->
existing status available, terminal unchanged, new offer rejected, existing result
accepted, unrelated session registration still available. Snapshot-read tests prove
no data stripping on inspection. The startup config path is independently tested.

Pre-#90 released binaries use strict unknown-field decoding and refuse a
ledger-enabled snapshot. Preserve this fail-closed behavior; never delete the
ledger field, rewrite schema metadata, partially migrate or restore an old backup
over later accepted results to make a downgrade appear successful. Bidirectional
old/new compatibility is not required. `UNSUPPORTED_LEDGER_FORMAT` identifies
unsupported ledger versions in new decoders; an existing unknown-field rejection
is the accepted behavior of already released old binaries. Diagnostic-only access
is useful but is not the approved rollback definition.

## Explicit integration limits

No new transport, Session Registry, model verifier, listener/pre-tool hook, plugin
manager, R155/R156 or public write CLI is added. Mutating canonical service methods
must be called by their authenticated coordinator/operator boundary; caller text
is not trusted authority. The old supervisor control token is not silently
promoted into general admission authority. Remaining production command exposure
is recorded in the coverage matrix/implementation report; no claim that arbitrary
Telepathy hosts automatically consult this ledger. The status command is available
offline against an atomic snapshot if the daemon is unavailable. Mutation/recovery
still respects canonical ownership and the accepted command boundary.

Required sequence: #90 merge -> #93 authorized write boundary -> first explicit
ledger offer/cutover. This blocked cutover is deliberate scope, not an UNKNOWN
implementation check to be waived. No write CLI or new authentication is in #90.
