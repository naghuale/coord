# WP-P03-02B: ApplyRunRecoveryCommand — Design Inventory

**Status:** READ_ONLY_DESIGN (no production code)  
**Decision:** P0-3-LOCAL-GIT-AND-RUN-RECOVERY-R1  
**Class:** TD-R2 (requires Design Review ACCEPTED + TD-R2 Design Audit ACCEPTED before implementation)  
**Issue:** #7

---

## 1. Run State Model (actual)

Source: `fsm/fsm.go`

```go
type Run struct {
    Task    string
    TaskID  taskid.ID
    RunID   string
    Attempt uint64
    // ...
    OwnerSession           string
    AgentSessionGeneration uint64
    Fence                  uint64
    Status                 Status
    ResumeStatus           Status
    RecoveryNeeded         bool
    RecoveryClaim          string
    // ...
    Result *Result
}
```

**Status enum:** `Pending | Accepted | Started | Blocked | Stalled | ResultReady | Done | Failed`

**Terminal:** `Status == Done || Status == Failed`

**Stalled semantics:**
- Entered via `Run.Stall()`: sets `ResumeStatus = Status`, `Status = Stalled`
- Exit requires a heartbeat or transition via `Run.Transition()`
- `Stalled` is not terminal — no direct transition to terminal from outside a heartbeat

**Recovery fields:**
- `RecoveryNeeded=true` is valid only when `Status == Stalled`
- `RecoveryClaim != ""` is valid only when `RecoveryNeeded && Status == Stalled`
- Terminal Run with `RecoveryNeeded` or `RecoveryClaim` is invalid (enforced by `Run.Validate()`)
- After recovery transition: `AgentSessionGeneration`, `RecoveryNeeded`, `RecoveryClaim` are cleared at `Run.Transition()` (`r.RecoveryNeeded = false`)

---

## 2. AgentSession Relationship

Source: `agentsession/session.go`, `store/canonical_agentsession.go`

```go
func (s Session) Active() bool { return s.RevokedAt.IsZero() }
```

A `Session` is active iff `RevokedAt.IsZero()`. Revocation sets `RevokedAt = event.CreatedAt`.

**AgentSession lifecycle:**
1. `AGENT_SESSION_REGISTERED` — address + agentID + publicKey + Generation=1
2. `AGENT_SESSION_KEY_ROTATED` — same address, new key, Generation++
3. `AGENT_SESSION_REVOKED` — address retired, `RevokedAt` set

`Run.OwnerSession` is the transport address of the owning AgentSession.

**Orphaned Run definition** (for ApplyRunRecoveryCommand):
- Run exists and is non-terminal
- `state.AgentSessions[run.OwnerSession]` is absent OR `!session.Active()`
- OR `sessionGeneration != run.AgentSessionGeneration`

Note: a generation mismatch (`AgentSessionGeneration` on Run != current `session.Generation`) also orphans the Run even if the AgentSession record is technically active.

---

## 3. Current Recovery Path (existing, requires live session)

Source: `store/canonical.go:1017–1105`, `store/canonical_agent_command.go`

**Existing recovery transfer** (`ValidateCanonicalTransition`):
```
Preconditions:
  prior.Status == Stalled
  prior.RecoveryNeeded == true
  prior.RecoveryClaim != ""
  newRun.OwnerSession != prior.OwnerSession
  newRun.Attempt == prior.Attempt          (no increment)
  newRun.RecoveryNeeded == false           (cleared)
  newRun.RecoveryClaim == ""               (cleared)
  newTask.Fence == oldTask.Fence + 1
  newRun.Fence == newTask.Fence
  TASK_OWNERSHIP_TRANSFERRED event required
```

**Gap:** this path requires `RecoveryClaim != ""`, which must be set by the current OwnerSession via `ApplyAgentCommand`. An orphaned Run (no live AgentSession) cannot reach `RecoveryClaim != ""` through this path.

`ApplyAgentCommand` requires:
- A live authenticated `AgentCommandFrame` from the current OwnerSession
- `session.Active() == true` (checked under store lock)
- `sessionGeneration == session.Generation`

Therefore: if the AgentSession is revoked, absent, or its generation is stale, the existing path is completely blocked. `ApplyRunRecoveryCommand` is the new Coordinator-driven path for this gap.

---

## 4. Command Boundary

`ApplyRunRecoveryCommand` is a new method on `*CanonicalStore`. It does not go through `ApplyAgentCommand` (which requires a live session). It is invoked by the Coordinator directly.

**Proposed signature (design only):**
```go
func (s *CanonicalStore) ApplyRunRecoveryCommand(
    cmd RunRecoveryCommand,
) (RunRecoveryReceipt, bool, error)
```

Where:
```go
type RunRecoveryCommand struct {
    MessageID          string
    RunID              string
    TaskID             taskid.ID
    ExpectedRevision   uint64     // Task.Revision CAS point
    ExpectedGeneration uint64     // Run.AgentSessionGeneration CAS point
    ReasonCode         RecoveryReasonCode
    StopProofEvidenceID string    // ExternalAction ID with positive observation
    AllowedTransitions  []RunRecoveryTransition
}
```

The `MessageID` is the idempotency key (same replay-before-CAS pattern as `applyInboxCommandLocked`).

---

## 5. Canonical Event Ownership

New event types required (decision doc lists):

| Event | Writer | When |
|---|---|---|
| `RUN_RECOVERY_REQUESTED` | Coordinator | ApplyRunRecoveryCommand: initial admission |
| `RUN_GENERATION_FENCED` | Coordinator | Generation CAS committed, old writer fenced |
| `RUN_STOP_PROOF_RECORDED` | Coordinator | StopProof ExternalAction bound |
| `RUN_RECOVERY_COMPLETED` | Coordinator | Recovery transition applied |
| `RUN_RECOVERY_BLOCKED` | Coordinator | Guard failed, command rejected |

Prohibited events (must not be emitted by ApplyRunRecoveryCommand):
- `OWNER_FENCE_EVENT`
- `OWNER_RECOVERY_APPLIED`

These are reserved for an OWNER-level path that was explicitly rejected.

---

## 6. Revision and Generation CAS

**Task.Revision CAS** (existing pattern):
- `ApplyAgentCommand` takes `expectedRevision uint64`
- Checked against `currentTask.Revision` under store lock
- Returns `RevisionConflictError` on mismatch (enables caller retry)
- `ApplyRunRecoveryCommand` MUST use the same CAS pattern

**Generation CAS** (new for recovery):
- `Run.AgentSessionGeneration` records which key generation the owner had at assignment time
- If the AgentSession has been key-rotated after the Run started, generation mismatch is a recovery signal
- `ApplyRunRecoveryCommand` takes `ExpectedGeneration`
- Must reject if `run.AgentSessionGeneration != cmd.ExpectedGeneration` (fail-closed)
- This prevents a race where the original agent is still live with a new key

**Fence CAS** (Task.Fence):
- Ownership transfer increments Task.Fence by exactly 1
- Run.Fence must equal new Task.Fence after recovery transfer
- `ErrFenceExhausted` if Task.Fence == MaxUint64 — recovery blocked, OWNER required
- The fence commit must be durable before recovery transition proceeds (guard: `logicalFenceCommitted`)

---

## 7. Stop-Proof Contract

Source: decision doc `runRecovery.requiredStopProofSatisfied`, `FENCE_ONLY` transition

**Purpose:** before taking ownership of a Run from a potentially-live agent, Coordinator must have evidence that the old agent has stopped writing.

**Critical distinction — session-level evidence vs. process-level evidence:**

An absent or revoked AgentSession is a *necessary* condition for recovery but is NOT sufficient to prove that the agent process has stopped. A live process may retain mutation authority after its session is revoked: the process may still be mid-write, have buffered commands, or hold a store lock. `AgentSession.Active() == false` proves only that the *canonical identity* of the session has been revoked, not that the *process* has terminated.

For `FENCE_ONLY`, session-level evidence is treated as operationally sufficient because the fence CAS itself provides the write-blocking guarantee: after the fence increment is committed, any in-flight `ApplyAgentCommand` from the old process will fail with `ErrAgentCommandStaleFence` (fence check in `ApplyAgentCommand` line 118) regardless of whether the process is still live. The fence CAS is the durable write barrier, not the session revocation. Session-level evidence is the trigger for initiating recovery; the fence is the actual write protection.

`unknownWriterMayStillHaveMutationAuthority` is the guard covering cases where this uncertainty cannot be resolved at the Coordinator level — for example, a split-brain scenario where the old process holds an external lock, or a forced takeover is requested without sufficient stop proof evidence. These cases escalate to OWNER (see section 10A).

**Stop-proof types:**
1. `RUN_ORPHANED` — AgentSession absent from canonical state (hard revocation)
2. `RUN_SESSION_UNAVAILABLE` — AgentSession revoked (RevokedAt set)
3. `RUN_SESSION_TERMINATED` — Coordinator observed terminal AgentSession event
4. `RUN_GENERATION_STALE` — current session.Generation > run.AgentSessionGeneration
5. `RUN_RESULT_ALREADY_OBSERVED` — external observation present for the run's work
6. `RUN_REPLACEMENT_REQUIRED` — explicit assignment to replacement agent

Stop-proof sufficiency:
- **Sufficient** for `FENCE_ONLY` if `RUN_ORPHANED || RUN_SESSION_UNAVAILABLE || RUN_SESSION_TERMINATED` — fence CAS provides write-blocking guarantee regardless of process liveness
- **Required but not sufficient alone** for `MARK_INTERRUPTED || PREPARE_REPLACEMENT`: must also have `externalObservationPresent`
- **Insufficient** (OWNER required) when `unknownWriterMayStillHaveMutationAuthority` — process-level uncertainty cannot be resolved by Coordinator

The `StopProofEvidenceID` in the command points to a committed ExternalAction with a positive observation that provides the stop-proof evidence.

---

## 8. Idempotency

Same replay-before-CAS pattern as `applyInboxCommandLocked`:
1. Look up `MessageID` in inbox replay log under store lock
2. If found: return cached receipt (idempotent replay)
3. If not found: run guard checks, apply transition, persist
4. Return receipt

**Duplicate command detection** (fail-closed violation `RUN_RECOVERY_DUPLICATE_COMMAND_CONFLICT`):
- Different `MessageID` with same `RunID + TaskID` but conflicting intent must be rejected
- Two recovery commands for the same RunID at different revisions: reject both until revision is current

---

## 9. Crash Matrix

| Crash point | State after restart | Recovery action |
|---|---|---|
| After `RUN_RECOVERY_REQUESTED` persisted, before fence committed | Fence not committed | Coordinator retries from same `MessageID` → idempotent replay returns cached receipt; re-checks `logicalFenceCommitted` |
| After fence committed, before `RUN_GENERATION_FENCED` event | Fence committed, event not written | Not possible: event is written in same atomic store commit as fence |
| After `RUN_GENERATION_FENCED`, before `RUN_RECOVERY_COMPLETED` | Generation fenced, new OwnerSession not set | Re-apply command → duplicate detection via `MessageID` replay → return prior receipt; caller sees prior result and completes |
| After `RUN_RECOVERY_COMPLETED` persisted | Recovery complete | `MessageID` replay returns completed receipt; no second write |
| StopProof ExternalAction in `DISPATCHING`, crash before `OBSERVED` | Stop proof not confirmed | ExternalAction crash recovery (existing path); ApplyRunRecoveryCommand blocks until StopProof ExternalAction reaches `OBSERVED` |

All crash paths are idempotent via `MessageID` replay. No second recovery write can occur after `RUN_RECOVERY_COMPLETED`.

---

## 10. Allowed Transitions and Guard Matrix

| Transition | Guards required | Prohibited when |
|---|---|---|
| `FENCE_ONLY` | orphanProven, fenceCommitted | terminal Run (Done or Failed); active live session |
| `MARK_INTERRUPTED` | orphanProven, stopProofRecorded, fenceCommitted | terminal Run (Done or Failed); Run not Stalled |
| `RESUME_OBSERVATION` | stopProofRecorded, resultObservationPresent | terminal Run (Done or Failed); no external observation |
| `PREPARE_REPLACEMENT` | orphanProven, stopProofRecorded, fenceCommitted | terminal Run (Done or Failed); prior run not terminal after fence |
| `COMPLETE_EXISTING` | resultObservationPresent, stopProofRecorded | terminal Run (Done or Failed) — explicit guard independent of result semantics; conflicting terminal state |

**Fail-closed violations** (always reject with error, never proceed):
- `RUN_RECOVERY_OBSERVATION_MISSING` — StopProof ExternalAction not in positive OBSERVED state
- `RUN_RECOVERY_GENERATION_MISMATCH` — `cmd.ExpectedGeneration != run.AgentSessionGeneration`
- `RUN_RECOVERY_REVISION_CONFLICT` — `cmd.ExpectedRevision != task.Revision`
- `RUN_RECOVERY_STOP_PROOF_INSUFFICIENT` — reason code requires stop proof evidence that is not present; this is a *structural pre-condition failure* (evidence not provided), distinct from `forcedTakeoverWithoutRequiredStopProof` (see 10A)
- `RUN_RECOVERY_FENCE_NOT_COMMITTED` — fence CAS not yet committed to durable state
- `RUN_RECOVERY_AUTHORITY_NOT_PROVEN` — `unknownWriterMayStillHaveMutationAuthority` — process-level uncertainty unresolvable by Coordinator
- `RUN_RECOVERY_DUPLICATE_COMMAND_CONFLICT` — conflicting concurrent recovery commands for same RunID

---

## 10A. OWNER Escalation Conditions

Source: decision doc `runRecovery.ownerRequiredWhenAny` (all five conditions, verbatim)

The following conditions require OWNER involvement. `ApplyRunRecoveryCommand` MUST NOT proceed past the pre-condition check when any of these applies; instead it returns a structured error indicating OWNER escalation is required.

| Condition | Meaning | Design response |
|---|---|---|
| `unknownWriterMayStillHaveMutationAuthority` | Process-level uncertainty: old process may still hold write authority despite session revocation | `RUN_RECOVERY_AUTHORITY_NOT_PROVEN` (fail-closed rejection); OWNER required to resolve |
| `forcedTakeoverWithoutRequiredStopProof` | Caller explicitly requests takeover despite insufficient stop proof evidence — this is distinct from `RUN_RECOVERY_STOP_PROOF_INSUFFICIENT`, which rejects commands missing required structural evidence. A forced takeover means the caller has a reason code but knowingly lacks the required observation. | OWNER required. The design's `RUN_RECOVERY_STOP_PROOF_INSUFFICIENT` handles the *structural absence* of evidence (no ExternalAction provided). A caller that presents an explicit forced-takeover intent with a reason it knows lacks sufficient proof cannot be handled fail-closed by the Coordinator — this requires OWNER authority. Implementation must distinguish these two cases. |
| `weakeningOfFencingContract` | Any change that would reduce the fence's write-blocking guarantee | OWNER required; `ApplyRunRecoveryCommand` must not permit this |
| `acceptedRecoveryNormConflict` | Recovery action conflicts with an accepted norm (e.g., an ADR, TASK-MODEL, or prior OWNER decision) | OWNER required; Coordinator cannot resolve norm conflicts autonomously |
| `riskAcceptanceRequired` | Recovery carries irreversible or high-impact risk that cannot be bounded by the Coordinator | OWNER required; Coordinator must surface the specific risk before proceeding |

**Important:** `RUN_RECOVERY_STOP_PROOF_INSUFFICIENT` and `forcedTakeoverWithoutRequiredStopProof` are NOT the same case:
- `RUN_RECOVERY_STOP_PROOF_INSUFFICIENT` = command does not include the required stop-proof ExternalAction ID, or the referenced action is not in a positive OBSERVED state. The command is structurally invalid. Coordinator rejects fail-closed.
- `forcedTakeoverWithoutRequiredStopProof` = caller provides a reason code that, per the decision, requires stop-proof evidence, but explicitly acknowledges the evidence is absent and requests a forced takeover. This intent cannot be satisfied by fail-closed rejection alone — it requires OWNER authority to accept the risk.

---

## 11. Required Design Scenarios

Per decision doc `wpP03_02B.testLimits.designScenariosRequired`:

| # | Scenario | Expected result |
|---|---|---|
| DS-1 | Session absent from state, no stop proof evidence ID supplied | `RUN_RECOVERY_OBSERVATION_MISSING` |
| DS-2 | Session present, generation matches (live writer) | `RUN_RECOVERY_AUTHORITY_NOT_PROVEN` |
| DS-3 | `cmd.ExpectedGeneration` != `run.AgentSessionGeneration` | `RUN_RECOVERY_GENERATION_MISMATCH` |
| DS-4 | `cmd.ExpectedRevision` != `task.Revision` | `RUN_RECOVERY_REVISION_CONFLICT` |
| DS-5 | Fence not yet committed to durable state | `RUN_RECOVERY_FENCE_NOT_COMMITTED` |
| DS-6 | Two concurrent ApplyRunRecoveryCommand calls for same RunID, different revisions | `RUN_RECOVERY_DUPLICATE_COMMAND_CONFLICT` |
| DS-7 | ExternalAction result observed after fence committed | `COMPLETE_EXISTING` succeeds |
| DS-8 | Full accepted orphan recovery: session revoked + stop proof + fence + replacement | `RUN_RECOVERY_COMPLETED`, new OwnerSession set |

---

## 12. Open Questions (require OWNER or Auditor resolution before implementation)

These questions were identified during inventory and cannot be resolved by read-only design alone:

**Q1: Stall precondition.** The existing recovery path requires `Status == Stalled && RecoveryNeeded == true`. Does `ApplyRunRecoveryCommand` need to force-stall an active (non-Stalled) Run, or is a non-Stalled Run an error? The decision doc does not specify. Recommendation: reject non-Stalled Run as `RUN_RECOVERY_STOP_PROOF_INSUFFICIENT` — caller must ensure the Run timed out first.

**Q2: Attempt increment.** The existing recovery transfer preserves `Attempt`. Does `ApplyRunRecoveryCommand` for `PREPARE_REPLACEMENT` increment Attempt? The decision doc says `PREPARE_REPLACEMENT` is allowed, but `Attempt` semantics are silent. Current AGENTS.md says "не меняйте attempt при повторной доставке — Recovery требует fencing старого writer." This means: Attempt is NOT incremented by recovery; it is incremented only on new attempt assignment.

**Q3: New OwnerSession source.** Where does the replacement OwnerSession address come from in `PREPARE_REPLACEMENT`? The command must carry it, or the caller supplies a pending AgentSession that is already registered. This is a command field that must be added to `RunRecoveryCommand`.

**Q4: Task.Revision vs Task.Fence.** The existing `ApplyAgentCommand` uses `Task.Revision` for optimistic CAS. Does `ApplyRunRecoveryCommand` use `Task.Revision`, `Task.Fence`, or both? Recommendation: use `Task.Revision` (same as existing write path) for the CAS optimistic lock, and validate Task.Fence separately as the generation-fence CAS.

---

## Summary

| Property | Actual value |
|---|---|
| Run state model | `fsm.Run` with 8 statuses, Fence, Generation, RecoveryNeeded/Claim |
| AgentSession link | `Run.OwnerSession` (address) + `Run.AgentSessionGeneration` |
| Orphan condition | `!session.Active() \|\| generationMismatch \|\| sessionAbsent` |
| Revision CAS | `Task.Revision` (existing replay-before-CAS pattern) |
| Generation CAS | `Run.AgentSessionGeneration` (new, fail-closed) |
| Fence CAS | `Task.Fence + 1`, `Run.Fence == newTask.Fence` |
| Stop-proof | ExternalAction in positive OBSERVED state, bound by `StopProofEvidenceID` |
| Idempotency | `MessageID` replay (same as ApplyAgentCommand) |
| New event types | 5 (RUN_RECOVERY_REQUESTED, RUN_GENERATION_FENCED, RUN_STOP_PROOF_RECORDED, RUN_RECOVERY_COMPLETED, RUN_RECOVERY_BLOCKED) |
| Prohibited events | OWNER_FENCE_EVENT, OWNER_RECOVERY_APPLIED |
| Fail-closed violations | 7 (see section 10) |
| OWNER escalation conditions | 5 (see section 10A) |
| Open design questions | 4 (Q1–Q4, section 12) |
| Implementation gate | Design Review ACCEPTED + TD-R2 Design Audit ACCEPTED |
