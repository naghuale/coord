# Assignment Ledger MVP (#90)

Status: implementation contract, subject to exact-SHA Review/Audit. Authority:
OWNER Issue #90 plan/additions, R148 and R152–R154. This is not R155/R156 or
full R153. Telepathy remains the only agent transport.

## One authority and exact identities

`CanonicalState.assignment_ledger` is an optional, version-1 ledger inside the
existing schema-3 JSON snapshot. It is omitted before cutover. The first explicit
`OfferAssignment` creates it; there is no journal import, guessed identity or
backfill. Old snapshots load without writes. New snapshots preserve all old
Task/Run/event/receipt fields. A future unsupported ledger version is refused
with `UNSUPPORTED_LEDGER_FORMAT`. Old strict decoders reject the added field;
never strip it to launch an old writer.

`assignment.Record` contains assignmentId, state, immutable issuer, currentCoordinator,
coordinatorGeneration, handoffEvidence, assignee/stableAgentId/sessionId and
assigneeGeneration, role, subjectSha, issuedAt, immutable validUntil,
admissionBasis, visibilityEvidence, revision, workStarted observation and one
accepted result receipt. Principal sessionId is the existing canonical
AgentSession address, not a friendly alias; in this MVP assignee equals that exact
canonical session key. A display ref/host observation is kept by evidence reference,
not inferred from cwd/model/PID. Admission basis binds exact target/role/SHA;
visibility receipt binds exact session and observation time. Their verification
is the trusted coordinator/operator command boundary's responsibility: a stored
reference is not an automated R153 eligibility or model check.

Process review/audit/analysis assignments need only subjectSha + role + exact
sessions, not invented Tasks. Optional RunBinding carries existing TaskID, RunID,
Fence and Attempt; its identity/role/subject/current fence is rechecked for work
and result acceptance. Bindings are immutable and one assignment per linked Run;
reissue to another session or subject needs a new assignment and, where relevant,
a new/recovered Run under its existing fencing rules.

## State machine (M2)

| From | Command | To | Guard |
|---|---|---|---|
| absent | OFFER | OFFERED | unique ID, current registered coordinator/target, exact evidence, valid immutable clock |
| OFFERED | verified exact ACK | ACKED | target principal/generation, current coordinator, deadline |
| ACKED | explicit coordinator ACTIVATE | ACTIVE | no work permission merely from ACK |
| nonterminal | SUSPEND | SUSPENDED | recorded coordinator reason/evidence |
| SUSPENDED | RECONCILE | OFFERED or ACKED | retain actual prior ACK only; explicit ACTIVATE required |
| nonterminal | EXPIRE | EXPIRED | now >= validUntil |
| nonterminal | VOID | VOID | explicit invalidation/replacement evidence |
| ACTIVE | accepted result | COMPLETED | exact ID/SHA/assignee/session/role/coordinator generation; active fresh sessions; one atomic acceptance |

COMPLETED, EXPIRED and VOID are terminal: no ACK, ALIVE, handoff, replay, new TTL
or old ID alias can revive them. IDs never become available for reuse. Every
transition appends a revisioned event and its exact immutable projection; startup
replays history and rejects mismatches/deletion. WorkStarted is an observation
only after ACTIVE. now == validUntil denies work/result even before EXPIRE is
persisted. Queries do not write EXPIRED.

A trusted `ObserveAssignmentCoordinatorLoss(observer, coordinator, evidence)`
checks the observer against its current active AgentSession, separately from the
lost-coordinator subject (which may be stale/revoked). The event actor is the
observer, not an impersonation of the lost subject. The observer cannot equal the
named coordinator or the target of any non-terminal assignment the batch would
mark; one such conflict rejects the whole call. It marks current authority lost;
state may still say ACTIVE but work/result checks refuse. `HandoffAssignment`
requires exact predecessor, a currently valid successor principal and verified
nonempty evidence, appends lineage, preserves original issuer and deadline.
Without recorded loss the predecessor must also be current and active. Only an
already CoordinatorLost record permits recovery from a stale predecessor; the
active successor is that recovery event actor. Recovery rejects the target and
the observer of the current open loss event as successors. That observer is
derived from existing append-only history, not a new record field; a handoff
closes the interval. Replay/load enforces the same exclusions and rejects repeated
loss events that would replace the open observer. Both observation and recovery
remain trusted service calls: principal freshness is not operator authentication.
These separation checks are not a privilege grant: #93 must reject an
unprivileged third-party loss observer before mutation (future test reference
`TestWriteAuthorityRejectsUnprivilegedLossObserver`). Blocking that observer
from later handing off to itself or the target does not authorize the first loss
observation. No #93 write authority is implemented here.
The service caller must authorize recovery/predecessor evidence, not accept a
bare transport claim. Session revocation or generation mismatch independently
blocks authority. Physical-process liveness detection is NOT implemented;
watchdog/operator observations must record loss. Assignee host restart requires
VOID/new assignment under R152/R154; no automatic physical host detector is claimed.
Incident escalation clocks remain with their existing authority and are not
recomputed or implemented by this MVP.

## Enforcement points and trust boundary

`CheckAssignmentWork` and `RecordAssignmentWorkStarted` compare actual expected
ID, immutable issuer address, SHA, role, session principal, current coordinator
and generation. An agent uses `coord assignment status`,
compares its exact expected values including issuer, and waits for ACTIVE before
work/resume/result.
A status response is a time-bounded observation, not a durable capability or a
pre-tool sandbox. External listeners that bypass preflight are not prevented by
this library; full runtime hook/receiver enforcement stays out of #90.

`AcceptAssignmentResult` handles standalone process receipts, not Task Review/Audit
verdict authority. It requires trusted coordinator and sender principals plus the
exact claim, validates under the canonical mutex, then commits receipt and
COMPLETED together. Concurrent/late duplicates cannot produce a second acceptance.
Optional Run results use `RecordExecutorResult`/`RecordReviewResult`/
`RecordAuditResult`; once the ledger is enabled, a linked active assignment and
explicit current coordinator principal are required before Inbox replay. The typed
Task event, receipt, Outbox and assignment completion share one durable snapshot.
A failed Save publishes neither part and poisons writes until reopen.

`ApplyAgentCommand` transport callbacks cannot mutate the ledger, including a
valid-looking ACK/activation. Only a coordinator command after verification can
change authority. Delivery is not ACK/admission/workStarted/result acceptance.
All mutating service APIs require the authenticated authorized command boundary,
as existing RegisterAgentSession does. A caller-provided principal/evidence string
by itself grants no external permission. No new transport or general supervisor
permission has been created. Production cutover is BLOCKED_ON_WRITE_AUTHORITY
until follow-up #93 provides the OWNER-authorized coordinator-usable write boundary. See the cutover runbook for actual integration limits.

## Read-only status (M3)

```
coord --state /absolute/coordd-state-dir assignment status <id> --format json
coord --state /absolute/coordd-state-dir assignment status <id> --format yaml
```

The path is the same `<state_dir>/canonical.json` configured for coordd. This
command runs before localchat/daemon startup. It reads one atomically published
snapshot, strictly validates it and does not acquire a writer lock, create a store,
migrate, change expiry or call Telepathy. JSON and YAML have the same field names:
assignmentId, state, validUntil, subjectSha, assignee, issuer, currentCoordinator,
coordinatorGeneration, handoffValid, workMayStart, reason. Exit 0 means a valid
record was read (including a terminal/inactive record with workMayStart=false).
Exit 3 UNKNOWN_ASSIGNMENT; exit 4 STORE_UNAVAILABLE (including missing file);
exit 5 STORE_CORRUPT (including invalid history/unsupported schema). All failures
emit workMayStart=false; they never bootstrap permission. Repeated reads do not
change snapshot bytes. A record can be stored ACTIVE yet deny work due to TTL,
lost coordinator, revoked session or optional Run fence mismatch.

## Ledger Freeze Mode

`coordd` config `assignment_ledger_mode: "LEDGER_FROZEN"` enables the OWNER-approved
launch rollback profile before transport/runtime processing. Same binary, same
store and ordinary non-ledger functions: existing status, validation, state
safety, handoff and results remain available. New OFFER/new ledger IDs and ledger
schema changes are refused. Frozen enforcement is checked in the common commit
paths as well as the OFFER service. No terminal, TTL, identity or replay guard is
disabled. Returning to normal mode is a deliberate operator restart with changed
config; there is no transport toggle. Diagnostic-only status access is not the
rollback definition. See [cutover](ASSIGNMENT-LEDGER-CUTOVER.md).
