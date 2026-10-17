# WP-P03-01: P0-3 Integration Map

**Decision ref:** P0-3-PRODUCTION-WRITE-BOUNDARY-R1  
**Issue:** #7 (Owner recovery command for Run without AgentSession, R123 B)  
**Base SHA:** 9fa76c17e817b10a689c5ee3fc011c4d32f51369  
**Class:** TD — read-only discovery; no production changes  
**Production writes:** 0

---

## 1. Canonical Write Gate (sole production writer)

`store.CanonicalStore.ApplyInboxCommand` is the only function that commits
canonical state. No other code path may mutate `CanonicalState` durably.

```
actorID + messageID  →  Inbox dedup key
expectedRevision      →  CAS before apply closure
apply(CanonicalState)  →  state mutation under lock
ValidateCanonicalState + ValidateCanonicalTransition  →  pre-persist checks
s.p.Save(next)        →  durable commit (persistence boundary)
```

**Invariants enforced by `ApplyInboxCommand`:**
- Replay-safe: second call with same `(actorID, messageID)` returns cached receipt
- Revision check fires before apply closure → stale revision → no mutation
- Inbox receipts are append-only and immutable
- `ValidateCanonicalTransition` enforces append-only event history and
  monotonic AgentSession transitions

`ApplyAgentCommand` is a thin wrapper that adds AgentSession identity checks
(public key, session generation, role/stage) before delegating to
`ApplyInboxCommand`.

---

## 2. External Action Write Path (GitHub + future adapters)

```
coordinator.Handle → EnqueueGitHubObservation (canonical intent)
→ store.EnqueueAction → durable PENDING in Orchestration.Actions
→ orchestration.Runtime.Tick
    → ClaimAction (epoch)
    → StartAction (durable DISPATCHING)
    → Adapter.Execute(ctx, action)   ← credential injection happens here only
    → store.RecordObservation
    → store.AcceptObservation
→ Adapter.Acknowledge(ctx, receipt)
```

**Key properties:**
- Agent creates a typed `GitHubActionPayload` (canonical command); it never
  touches credentials directly
- Credentials exist only inside the `Adapter.Execute` boundary; they do not
  enter model context, task payload, or canonical event body
- `DISPATCHING` state after crash → `RecoverActions` at restart → observe
  before retry (never blind redispatch)
- Current adapters: `FakeGitHubAdapter` (fixture/test only); no live GitHub
  adapter is implemented or injected at runtime (`Adapter == nil` → BLOCKED_EXTERNAL)

**Defined GitHub operations** (`internal/externalaction`):  
CREATE_ISSUE, UPDATE_ISSUE, COMMENT_ISSUE, CLOSE_ISSUE, REOPEN_ISSUE,
CREATE_PULL_REQUEST, UPDATE_PULL_REQUEST, COMMENT_PULL_REQUEST,
MERGE_PULL_REQUEST, OBSERVE_ISSUE, OBSERVE_PULL_REQUEST, OBSERVE_PIPELINE

**Gap for P0-3 Phase 1:** local git operations (CREATE_ISOLATED_BRANCH,
WRITE_ALLOWLISTED_FILES, CREATE_COMMIT) are NOT in the `GitHubOperation` enum
and do not require the GitHub adapter. They require a separate local git write
path (Phase 1 scope: no credentials, no push).

---

## 3. Adapter Interface and Credential Boundary

```go
type Adapter interface {
    SourceID() string
    Execute(context.Context, externalaction.Action) (externalaction.Result, error)
    Observe(context.Context, externalaction.Action) (externalaction.Result, error)
    Acknowledge(context.Context, externalaction.Receipt) error
}
```

- Credentials are injected by the adapter process, not passed in `Action`
- `Action` contains only the typed payload digest and idempotency key
- No live adapter exists today; adding one requires explicit registration in
  `AdapterRegistry` by an authorized process

**Prohibited (enforced by architecture, not yet by code gate):**
- Credentials in `GitHubActionPayload`, `GitHubRequest`, or `GitHubTarget`
- Credentials in canonical events, task payload, assignment, or test fixtures
- Direct agent access to tokens

---

## 4. Owner Recovery Command Gap (Issue #7 scope)

**Current state:** `ApplyAgentCommand` requires a live `AgentCommandFrame` —
authenticated transport evidence that includes `VerifiedPublicKey`,
`SessionGeneration`, and `ReceivedBy`. When a Run has no active `AgentSession`
(session expired, agent unreachable), no command can be applied to that Run
via the current agent command path.

**Missing:** A canonical command that the coordinator/OWNER can issue to:
1. Terminate the orphaned Run with a supervisor stop proof (commitment that
   the agent will not write again, i.e. a fence event)
2. Create a new assignment from a fresh `AgentSession`

**Existing wiring that the new command must respect:**
- `validateCanonicalAgentSessionBindings` — checks `OwnerSession` fields of
  Runs against registered AgentSessions
- `agentsession.ValidateTransition` — enforces append-only AgentSession history
- `ValidateCanonicalTransition` — enforces `requireOwnershipTransferEvent`
  when a Run's `OwnerSession` changes

**Proposed command surface (to be designed in WP-P03-02):**
```
ApplyOwnerRecoveryCommand(
    commandID string,          // idempotency
    taskID    taskid.ID,
    runID     string,          // the orphaned Run
    expectedRevision uint64,   // CAS
    reason    string,          // supervisor stop proof narrative
    now       time.Time,
) (InboxReceipt, bool, error)
```

This command must produce a `DomainEvent` of a new type
(e.g. `OwnerFenceEvent`) that satisfies `requireOwnershipTransferEvent` and
puts the Run in a terminal state without requiring a live `AgentSession`.

---

## 5. LOCAL_GIT Family Compatibility Check

**Decision:** EXTERNAL-ACTION-CONTRACT-R1 explicitly fixes `allowedFamilies: [AGENT_SESSION, GITHUB]`.  
**Implementation (model.go:173):** `if a.Family != AgentSession && a.Family != GitHub { return "unknown action family" }` — exhaustive, fail-closed.

**Conclusion:** Adding `LOCAL_GIT` as a new ExternalAction family requires a new OWNER decision (class R). A TD-R2 correction is insufficient — this extends an exhaustive allowlist that is part of an accepted R norm.

**Impact on WP-P03-02A:** The local git write path MUST NOT model local git operations as ExternalActions under the current schema. Two options require an OWNER decision:
1. Add `LOCAL_GIT` to `allowedFamilies` via a new R decision
2. Implement local git writes as a separate non-ExternalAction durable path (no family needed)

Until an OWNER decision resolves this, WP-P03-02A design is BLOCKED on family taxonomy.

---

## 6. Write Permission Boundary (P0-3-PRODUCTION-WRITE-BOUNDARY-R1)
<!-- formerly section 5 — renumbered after LOCAL_GIT compatibility section -->

| Phase | Operations | Credentials | Status |
|-------|-----------|-------------|--------|
| Phase 1 | CREATE_ISOLATED_BRANCH, WRITE_ALLOWLISTED_FILES, CREATE_COMMIT | None | Authorized |
| Phase 2 | PUSH_FEATURE_BRANCH, CREATE_PULL_REQUEST | Required | After separate activation |
| Phase 3 | COMMENT_ISSUE, CLOSE_ISSUE, MERGE_PULL_REQUEST, WRITE_TO_MAIN | Required | Deferred |

**Repository allowlist:** `naghuale/coord`, feature/* branches only.  
**Protected branches:** main — direct write prohibited.  
**File boundary:** exact path allowlist per assignment; `git add -A` prohibited.

---

## 7. Crash-Restart Safety on Write Path

```
canonical intent (durable) → PENDING
claim (epoch) → DISPATCHING  
execute → observation recorded
crash after DISPATCHING:
    RecoverActions at restart → AMBIGUOUS
    → Adapter.Observe (not Execute)
    → reconcile before retry or blocker
```

**Duplicate commit prevention:**  
The Inbox dedup key `(actorID, messageID)` prevents re-application of the same
canonical command after restart. A local git operation must also carry an
idempotency key matched against `expectedHeadSha` before committing.

---

## 8. Capability Permission Map

| Capability | Current maturity | Required for live activation |
|-----------|-----------------|------------------------------|
| `production.repository-write-path` | NOT_EVALUATED | Separate gate after shadow write comparison |
| Live GitHub adapter | Not implemented | Phase 2 activation + credential design |
| Owner recovery command | Not implemented | WP-P03-02 + WP-P03-04 |

---

## 9. Identified Gaps for WP-P03-02 through WP-P03-06

| WP | Gap |
|----|-----|
| WP-P03-02 | Typed write intent (LocalGitAction enum + durable lifecycle) |
| WP-P03-02 | `ApplyOwnerRecoveryCommand` canonical command |
| WP-P03-03 | Local isolated writer: branch + commit, no push, crash-safe |
| WP-P03-04 | Write admission gate: base SHA check, expected tree, unknown-work protection |
| WP-P03-05 | Shadow write comparison: expected tree vs produced tree |
| WP-P03-06 | Activation decision packet: credential design + live gate evidence |

---

## 10. What Does NOT Change in Phase 1

- No changes to `ApplyInboxCommand` or `ApplyAgentCommand`
- No changes to existing `GitHubOperation` enum
- No new credential injection points
- No changes to `AdapterRegistry` or live adapter registration
- No push, no PR creation, no issue writes
- `CanonicalPersistence` implementation unchanged
